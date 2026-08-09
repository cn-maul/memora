// Package qa 问答模块
// 单文件/全局 RAG、上下文裁剪、会话管理
package qa

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"memora/internal/contract"
)

// IStorage qa 所需的 storage 接口
type IStorage interface {
	FilesGet(id int64) (*contract.FileInfo, error)
	ChunksByFile(fileID int64) ([]*contract.Chunk, error)
	ChunksGet(id int64) (*contract.Chunk, error)
	VectorsSearch(queryVec []float32, topK int) ([]contract.VectorEntry, error)
	QASessionsCreate(mode string, fileID int64) (int64, error)
	QASessionsList() ([]*contract.QASession, error)
	QASessionsDelete(id int64) error
	QAMessagesAppend(sessionID int64, role, content, sources string, createdAt int64) (int64, error)
	QAMessagesBySession(sessionID int64) ([]*contract.QAMessage, error)
}

// ILLM qa 所需的 llm 接口
type ILLM interface {
	Chat(system, user string, opts *contract.ChatOptions) (string, error)
	ChatStream(system, user string, opts *contract.ChatOptions, cancel <-chan struct{}) (<-chan string, error)
	Embed(texts []string) ([][]float32, error)
}

// IEvents qa 所需的事件接口
type IEvents interface {
	Notify(topic string, data interface{})
}

// Module 问答模块
type Module struct {
	storage         IStorage
	llm             ILLM
	events          IEvents
	maxContextChars int
}

// systemPrompt 问答系统提示词（Ask 与 AskStream 共用，精简以减少输入 token）
const systemPrompt = "你是 Memora 文档问答助手。依据下方 [文件=路径, 段落=序号] 标注的文档片段回答，引用时注明文件路径。用 Markdown 简洁作答（# 标题、- 列表、**加粗**），不要开场白与结束语。若片段中没有答案，直接说明\"根据现有文档，未找到相关信息\"，不要猜测。"

// fullTextDirectLimit 单文件模式"全文直发"的 rune 上限。
// 全文 ≤ 此值时直发（小文件保证准确）；超过则走向量检索（大文件保证速度与精准）。
const fullTextDirectLimit = 6000

// New 创建问答模块
func New(storage IStorage, llm ILLM, events IEvents, maxContextChars int) *Module {
	return &Module{
		storage:         storage,
		llm:             llm,
		events:          events,
		maxContextChars: maxContextChars,
	}
}

// Ask 问答（§4.12 内部流程）
func (m *Module) Ask(req *contract.QARequest) (*contract.QAResponse, error) {
	if req.Question == "" {
		return nil, fmt.Errorf("[qa] 问题不能为空")
	}

	// 创建或复用会话
	sessionID := req.SessionID
	newSession := sessionID == 0
	if newSession {
		var err error
		sessionID, err = m.storage.QASessionsCreate(req.Mode, req.FileID)
		if err != nil {
			return nil, fmt.Errorf("[qa] 创建会话失败: %w", err)
		}
	}

	// 用户消息延迟到模型成功后写入（修复 M-03：避免留下无回答的孤立消息/会话）

	contextBlocks, sources, err := m.buildContext(req)
	if err != nil {
		if newSession {
			_ = m.storage.QASessionsDelete(sessionID)
		}
		return nil, err
	}

	// 拼接上下文
	contextStr := strings.Join(contextBlocks, "\n\n")

	// 空上下文短路：检索阈值过滤后无相关内容，直接返回"未找到"，
	// 避免用空上下文调用 LLM 浪费一次请求与等待
	if len(contextBlocks) == 0 {
		notFound := "根据现有文档，未找到相关信息。"
		m.storage.QAMessagesAppend(sessionID, "user", req.Question, "", time.Now().UnixMilli())
		m.storage.QAMessagesAppend(sessionID, "assistant", notFound, "[]", time.Now().UnixMilli())
		m.events.Notify("qa_ready", map[string]interface{}{"sessionId": sessionID})
		return &contract.QAResponse{Answer: notFound, SessionID: sessionID}, nil
	}

	userPrompt := fmt.Sprintf("%s\n\n问题：%s", contextStr, req.Question)

	answer, err := m.llm.Chat(systemPrompt, userPrompt, &contract.ChatOptions{Temperature: 0.2, MaxTokens: 1600})
	if err != nil {
		// 失败回滚：若是本次新建的会话则整会话删除，避免留下孤立空会话（修复 M-03）
		if newSession {
			_ = m.storage.QASessionsDelete(sessionID)
		}
		return nil, fmt.Errorf("[qa] 问答失败: %w", err)
	}

	// 模型成功后，一次性写入用户与助手消息（避免孤立消息）
	m.storage.QAMessagesAppend(sessionID, "user", req.Question, "", time.Now().UnixMilli())

	// 保存助手消息
	sourcesJSON := marshalSources(sources)
	m.storage.QAMessagesAppend(sessionID, "assistant", answer, sourcesJSON, time.Now().UnixMilli())

	// 广播 qa_ready
	m.events.Notify("qa_ready", map[string]interface{}{
		"sessionId": sessionID,
	})

	return &contract.QAResponse{
		Answer:    answer,
		Sources:   sources,
		SessionID: sessionID,
	}, nil
}

// AskStream 流式问答。ch 返回内容增量，done 携带最终结果（含 sessionId 与 sources）。
// 调用方可通过 cancel 中止。
func (m *Module) AskStream(req *contract.QARequest, cancel <-chan struct{}) (<-chan string, <-chan *contract.QAResponse) {
	ch := make(chan string, 32)
	done := make(chan *contract.QAResponse, 1)

	go func() {
		defer close(ch)
		defer close(done)

		if req.Question == "" {
			done <- &contract.QAResponse{Error: "[qa] 问题不能为空"}
			return
		}

		// 创建或复用会话
		sessionID := req.SessionID
		newSession := sessionID == 0
		if newSession {
			id, err := m.storage.QASessionsCreate(req.Mode, req.FileID)
			if err != nil {
				done <- &contract.QAResponse{Error: fmt.Sprintf("[qa] 创建会话失败: %v", err)}
				return
			}
			sessionID = id
		}

		contextBlocks, sources, err := m.buildContext(req)
		if err != nil {
			if newSession {
				_ = m.storage.QASessionsDelete(sessionID)
			}
			done <- &contract.QAResponse{Error: err.Error()}
			return
		}

		contextStr := strings.Join(contextBlocks, "\n\n")

		// 空上下文短路：不调用 LLM，直接输出"未找到"
		if len(contextBlocks) == 0 {
			notFound := "根据现有文档，未找到相关信息。"
			m.storage.QAMessagesAppend(sessionID, "user", req.Question, "", time.Now().UnixMilli())
			m.storage.QAMessagesAppend(sessionID, "assistant", notFound, "[]", time.Now().UnixMilli())
			m.events.Notify("qa_ready", map[string]interface{}{"sessionId": sessionID})
			select {
			case ch <- notFound:
			case <-cancel:
				// 取消：发取消响应，避免 transport 走"成功完成"流程（修复 review should-fix）
				done <- &contract.QAResponse{Error: "已取消"}
				return
			}
			done <- &contract.QAResponse{Answer: notFound, Sources: sources, SessionID: sessionID}
			return
		}

		userPrompt := fmt.Sprintf("%s\n\n问题：%s", contextStr, req.Question)

		chunks, err := m.llm.ChatStream(systemPrompt, userPrompt, &contract.ChatOptions{Temperature: 0.2, MaxTokens: 1600}, cancel)
		if err != nil {
			if newSession {
				_ = m.storage.QASessionsDelete(sessionID)
			}
			done <- &contract.QAResponse{Error: err.Error()}
			return
		}

		var sb strings.Builder
		for chunk := range chunks {
			if strings.HasPrefix(chunk, "__ERROR__:") {
				sb.WriteString(strings.TrimPrefix(chunk, "__ERROR__:"))
				if newSession {
					_ = m.storage.QASessionsDelete(sessionID)
				}
				done <- &contract.QAResponse{Error: sb.String()}
				return
			}
			sb.WriteString(chunk)
			select {
			case ch <- chunk:
			case <-cancel:
				// 客户端取消：向 done 发送取消响应而非直接 return，
				// 否则 done 通道关闭时 transport 收到 nil 会 panic（修复审计高危）
				done <- &contract.QAResponse{Error: "已取消"}
				return
			}
		}

		answer := sb.String()

		// 保存消息（用户 + 助手）
		m.storage.QAMessagesAppend(sessionID, "user", req.Question, "", time.Now().UnixMilli())
		sourcesJSON := marshalSources(sources)
		m.storage.QAMessagesAppend(sessionID, "assistant", answer, sourcesJSON, time.Now().UnixMilli())

		m.events.Notify("qa_ready", map[string]interface{}{"sessionId": sessionID})

		done <- &contract.QAResponse{
			Answer:    answer,
			Sources:   sources,
			SessionID: sessionID,
		}
	}()

	return ch, done
}

// buildContext 构建问答上下文（单文件/全局 RAG），返回上下文块与来源
func (m *Module) buildContext(req *contract.QARequest) ([]string, []contract.QASource, error) {
	var contextBlocks []string
	var sources []contract.QASource

	if req.Mode == "file" && req.FileID > 0 {
		// 单文件模式
		chunks, err := m.storage.ChunksByFile(req.FileID)
		if err != nil {
			return nil, nil, fmt.Errorf("[qa] 获取文件分块失败: %w", err)
		}

		file, err := m.storage.FilesGet(req.FileID)
		if err != nil || file == nil {
			return nil, nil, fmt.Errorf("[qa] 文件不存在")
		}

		// 全文 ≤ 直发上限则直发（小文件）；否则向量检索（大文件）
		var fullText string
		for _, c := range chunks {
			fullText += c.Text + "\n"
		}

		directLimit := fullTextDirectLimit
		if m.maxContextChars < directLimit {
			directLimit = m.maxContextChars
		}

		if utf8.RuneCountInString(fullText) <= directLimit {
			contextBlocks = append(contextBlocks, fmt.Sprintf("[文件=%s, 段落=全文]\n%s", file.RelPath, fullText))
			sources = append(sources, contract.QASource{RelPath: file.RelPath, Seq: 0})
		} else {
			// 超限：嵌入查询后仅在该文件的分块中搜索 top-K
			vecs, err := m.llm.Embed([]string{req.Question})
			if err != nil {
				return nil, nil, fmt.Errorf("[qa] 嵌入问题失败: %w", err)
			}
			if len(vecs) > 0 {
				// 全库搜索取回 2×块数，容忍其他文件高分块挤占，再按 fileID 过滤
				entries, err := m.storage.VectorsSearch(vecs[0], len(chunks)*2)
				if err == nil {
					type scoredChunk struct {
						chunk *contract.Chunk
						score float64
					}
					var scored []scoredChunk
					for _, e := range entries {
						if e.ChunkID == 0 || e.Score < 0.3 {
							continue
						}
						chunk, err := m.storage.ChunksGet(e.ChunkID)
						if err != nil || chunk == nil || chunk.FileID != req.FileID {
							continue
						}
						scored = append(scored, scoredChunk{chunk: chunk, score: e.Score})
					}
					sort.Slice(scored, func(i, j int) bool {
						return scored[i].score > scored[j].score
					})
					topK := 8
					if len(scored) < topK {
						topK = len(scored)
					}
					// 按 maxContextChars 累计裁剪，防止上下文超限拖慢首 token
					usedRunes := 0
					for i := 0; i < topK; i++ {
						c := scored[i].chunk
						block := fmt.Sprintf("[文件=%s, 段落=%d]\n%s", file.RelPath, c.Seq, c.Text)
						usedRunes += utf8.RuneCountInString(block)
						if usedRunes > m.maxContextChars {
							break
						}
						contextBlocks = append(contextBlocks, block)
						sources = append(sources, contract.QASource{RelPath: file.RelPath, Seq: c.Seq})
					}
				}
			}
		}
	} else {
		// 全局模式：Embed 查询 + 全库 top-K（40块），按相似度阈值过滤
		vecs, err := m.llm.Embed([]string{req.Question})
		if err != nil {
			return nil, nil, fmt.Errorf("[qa] 嵌入问题失败: %w", err)
		}
		if len(vecs) > 0 {
			entries, err := m.storage.VectorsSearch(vecs[0], 40)
			if err == nil {
				type scored struct {
					chunk *contract.Chunk
					file  *contract.FileInfo
					score float64
				}
				// 按分数排序，过滤掉相似度极低的
				var scoredChunks []scored
				for _, e := range entries {
					if e.Score < 0.3 {
						continue
					}
					chunk, err := m.storage.ChunksGet(e.ChunkID)
					if err != nil || chunk == nil {
						continue
					}
					file, err := m.storage.FilesGet(chunk.FileID)
					if err != nil || file == nil {
						continue
					}
					scoredChunks = append(scoredChunks, scored{chunk: chunk, file: file, score: e.Score})
				}
				sort.Slice(scoredChunks, func(i, j int) bool {
					return scoredChunks[i].score > scoredChunks[j].score
				})
				// 最多取 8 块最相关的
				maxBlocks := 8
				if len(scoredChunks) < maxBlocks {
					maxBlocks = len(scoredChunks)
				}
				// 按 maxContextChars 累计裁剪，防止上下文超限拖慢首 token
				usedRunes := 0
				for i := 0; i < maxBlocks; i++ {
					c := scoredChunks[i]
					block := fmt.Sprintf("[文件=%s, 段落=%d]\n%s", c.file.RelPath, c.chunk.Seq, c.chunk.Text)
					usedRunes += utf8.RuneCountInString(block)
					if usedRunes > m.maxContextChars {
						break
					}
					contextBlocks = append(contextBlocks, block)
					sources = append(sources, contract.QASource{RelPath: c.file.RelPath, Seq: c.chunk.Seq})
				}
			}
		}
	}

	return contextBlocks, sources, nil
}

// Sessions 列出所有会话
func (m *Module) Sessions() ([]*contract.QASession, error) {
	return m.storage.QASessionsList()
}

// NewSesion 创建新会话
func (m *Module) NewSesion(mode string, fileID int64) (int64, error) {
	return m.storage.QASessionsCreate(mode, fileID)
}

// ClearSesion 清空会话（暂不实现，用删除代替）
func (m *Module) ClearSesion(id int64) error {
	return m.storage.QASessionsDelete(id)
}

// DeleteSession 删除会话
func (m *Module) DeleteSession(id int64) error {
	return m.storage.QASessionsDelete(id)
}

// marshalSources 序列化 sources 为 JSON
func marshalSources(sources []contract.QASource) string {
	data, err := json.Marshal(sources)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// Package qa 问答模块
// 单文件/全局 RAG、上下文裁剪、会话管理
package qa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"memora/internal/contract"
	"memora/internal/logx"
)

// IStorage qa 所需的 storage 接口
type IStorage interface {
	FilesGet(id int64) (*contract.FileInfo, error)
	FilesFindByName(keyword string, limit int) ([]*contract.FileInfo, error)
	ChunksByFile(fileID int64) ([]*contract.Chunk, error)
	ChunksGet(id int64) (*contract.Chunk, error)
	VectorsSearch(queryVec []float32, topK int) ([]contract.VectorEntry, error)
	QASessionsCreate(mode string, fileID int64) (int64, error)
	QASessionsList() ([]*contract.QASession, error)
	QASessionsDelete(id int64) error
	SaveExchange(sessionID int64, mode string, fileID int64, userMsg, assistantMsg, sources string, createdAt int64) (int64, int, error)
	QAMessagesBySession(sessionID int64) ([]*contract.QAMessage, error)
}

// ILLM qa 所需的 llm 接口
type ILLM interface {
	Chat(system, user string, opts *contract.ChatOptions) (string, error)
	ChatStream(system, user string, opts *contract.ChatOptions, cancel <-chan struct{}) (<-chan string, error)
	Embed(texts []string) ([][]float32, error)
	EmbedQuery(text string) ([]float32, error)             // 查询嵌入（自动加检索指令前缀）
	Rerank(query string, docs []string) ([]float64, error) // 可选：未配置时返回错误
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
const systemPrompt = "你是 Memora 文档问答助手。依据下方 [文件=路径, 段落=序号] 标注的文档片段回答。引用文件时务必用 [文件=路径, 段落=序号] 标记原样标出（如 [文件=预算表.xlsx, 段落=3]），不要用反引号或只写文件名。用 Markdown 简洁作答（# 标题、- 列表、**加粗**），不要开场白与结束语。若片段中没有答案，直接说明\"根据现有文档，未找到相关信息\"，不要猜测。"

// qaMaxTokens 问答最大输出 token。
// 推理模型（SenseNova reasoning 等）会先消耗大量 token 输出思维链（delta.reasoning）
// 再输出最终答案；上限过低会导致思维链还没结束就触发 finish_reason=length，
// 最终 delta.content 为空、只能走非流式重试（慢且非流式）。给足余量避免该场景。
const qaMaxTokens = 4096

// quotedKeywordRe 提取引号/书名号内文件名关键词（包级复用，避免每次编译，review nit）
var quotedKeywordRe = regexp.MustCompile(`[“"『「《]([^”"』」》]{1,30})[”"』」》]`)

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

// QASink 流式问答输出槽：把流水线产生的增量逐块交给调用方。
// OnChunk 收到每个原始块（内容块或带 ThinkChunkPrefix 的思考块，逐字透传）；
// Canceled 返回是否已取消（nil 视为永不取消）。sink == nil 表示非流式模式。
type QASink struct {
	OnChunk  func(chunk string)
	Canceled func() bool
}

// errCanceled 统一的"已取消"哨兵错误：流式管道在任意取消点返回它，
// 由 AskStream 包装层转换成 done{Error: "已取消"}（与取消响应线协议一致）。
var errCanceled = errors.New("已取消")

// Execute 统一问答流水线（Ask 与 AskStream 的单一实现，P2-06）。
// sink == nil → 非流式：走 LLM.Chat，错误直接返回（Ask 使用）；
// sink != nil → 流式：走 LLM.ChatStream 逐块输出，取消/错误由调用方转成 done 响应（AskStream 使用）。
// ctx 同样参与取消：任意环节 ctx 取消或 sink.Canceled() 为真时，回滚新建会话并返回 errCanceled。
func (m *Module) Execute(ctx context.Context, req *contract.QARequest, sink *QASink) (*contract.QAResponse, error) {
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
		if _, _, err := m.storage.SaveExchange(sessionID, req.Mode, req.FileID, req.Question, notFound, "[]", time.Now().UnixMilli()); err != nil {
			if newSession {
				_ = m.storage.QASessionsDelete(sessionID)
			}
			return nil, fmt.Errorf("[qa] 保存问答失败: %w", err)
		}
		m.events.Notify("qa_ready", map[string]interface{}{"sessionId": sessionID})
		// 流式：把"未找到"文本也按普通内容块推给 sink（AskStream 直通 ch 的等价物）
		if err := emitChunk(ctx, sink, notFound); err != nil {
			if newSession {
				_ = m.storage.QASessionsDelete(sessionID)
			}
			return nil, err
		}
		return &contract.QAResponse{Answer: notFound, SessionID: sessionID}, nil
	}

	userPrompt := fmt.Sprintf("%s\n\n问题：%s", contextStr, req.Question)

	// 模型回答：唯一的 LLM 环节——非流式单次 Chat；流式 ChatStream + 空回答重试
	answer, err := m.generateAnswer(ctx, req, userPrompt, sink)
	if err != nil {
		// 失败回滚：若是本次新建的会话则整会话删除，避免留下孤立空会话（修复 M-03）
		if newSession {
			_ = m.storage.QASessionsDelete(sessionID)
		}
		if err == errCanceled {
			return nil, errCanceled
		}
		return nil, fmt.Errorf("[qa] 问答失败: %w", err)
	}

	// 把回答里的文件路径（含反引号/裸路径）规整为 [文件=path] 标记，保证前端可点击（修复：检索到文件却没超链接）
	answer = linkifySourceRefs(answer, sources)

	// 模型成功后，单事务原子写入用户与助手消息（P1-12：失败不得返回成功）
	sourcesJSON := marshalSources(sources)
	sid, _, err := m.storage.SaveExchange(sessionID, req.Mode, req.FileID, req.Question, answer, sourcesJSON, time.Now().UnixMilli())
	if err != nil {
		if newSession {
			_ = m.storage.QASessionsDelete(sessionID)
		}
		return nil, fmt.Errorf("[qa] 保存问答失败: %w", err)
	}
	sessionID = sid

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

// generateAnswer 调 LLM 生成回答（Execute 内部的 LLM 环节）。
// 非流式（sink == nil）：单次 Chat，空回答给出明确提示；
// 流式：ChatStream 逐块输出（思考块透传不计数），空流用非流式 Chat 重试兜底。
func (m *Module) generateAnswer(ctx context.Context, req *contract.QARequest, userPrompt string, sink *QASink) (string, error) {
	// 非流式：单次 Chat
	if sink == nil {
		answer, err := m.llm.Chat(systemPrompt, userPrompt, &contract.ChatOptions{Temperature: 0.2, MaxTokens: qaMaxTokens})
		if err != nil {
			return "", err
		}
		// 空回答防御：LLM 返回空/纯空白时给出明确提示（review nit）
		if strings.TrimSpace(answer) == "" {
			return "（模型未返回内容。请确认模型服务正常、文件已成功索引，或换个问法重试。）", nil
		}
		return answer, nil
	}

	// 流式：ChatStream + 逐块输出，取消通道合并 ctx 与 sink.Canceled
	chunks, err := m.llm.ChatStream(systemPrompt, userPrompt, &contract.ChatOptions{Temperature: 0.2, MaxTokens: qaMaxTokens}, streamCancel(ctx, sink))
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for chunk := range chunks {
		if strings.HasPrefix(chunk, "__ERROR__:") {
			// __ERROR__: 哨兵块：中止，错误携带已累计的内容与上游错误信息
			sb.WriteString(strings.TrimPrefix(chunk, "__ERROR__:"))
			return "", errors.New(sb.String())
		}
		if strings.HasPrefix(chunk, contract.ThinkChunkPrefix) {
			// 思考过程块（推理模型思维链）：转发给前端实时渲染，但不计入最终回答
			// （answer 仅含 delta.content，避免思维链被持久化进会话消息）。
			if err := emitChunk(ctx, sink, chunk); err != nil {
				return "", err
			}
			continue
		}
		sb.WriteString(chunk)
		if err := emitChunk(ctx, sink, chunk); err != nil {
			return "", err
		}
	}

	answer := sb.String()

	// 空回答防御：流式返回空/纯空白时，用非流式 Chat 重试一次兜底。
	// 修复：部分端点流式格式不标准（如 message 字段、空 delta）导致流式内容为空，
	// 直接给提示无法自愈；非流式重试可拿到完整回答。
	if strings.TrimSpace(answer) == "" {
		// 重试前检查取消：非流式 Chat 无法中途打断，取消后不应再发起重试（review 发现）
		if canceled(ctx, sink) {
			return "", errCanceled
		}

		logx.Warn("qa", "流式回答为空，改用非流式重试", "question", req.Question)
		retried, rerr := m.llm.Chat(systemPrompt, userPrompt, &contract.ChatOptions{Temperature: 0.2, MaxTokens: qaMaxTokens})
		// 重试完成后再次检查取消：用户中止时丢弃重试结果，不写入会话（review 发现）
		if canceled(ctx, sink) {
			return "", errCanceled
		}
		if rerr != nil {
			// 非流式也失败：返回真实错误而非"未返回内容"，便于排查
			return "", fmt.Errorf("模型未返回内容且重试失败: %v", rerr)
		}
		if strings.TrimSpace(retried) != "" {
			// 保持 AskStream 既有线上行为：重试结果只在 done 携带，不重新流式输出
			answer = retried
		} else {
			answer = "（模型未返回内容。请确认模型服务正常、文件已成功索引，或换个问法重试。）"
		}
	}

	return answer, nil
}

// Ask 问答（§4.12 内部流程，非流式：sink 为空）
func (m *Module) Ask(req *contract.QARequest) (*contract.QAResponse, error) {
	return m.Execute(context.Background(), req, nil)
}

// AskStream 流式问答。ch 返回内容增量，done 携带最终结果（含 sessionId 与 sources）。
// 调用方可通过 cancel 中止。内部为 Execute 的流式包装：sink 把块转发到 ch、
// 取消状态透传给流水线，错误/结果统一转成 done 响应。
func (m *Module) AskStream(req *contract.QARequest, cancel <-chan struct{}) (<-chan string, <-chan *contract.QAResponse) {
	ch := make(chan string, 32)
	done := make(chan *contract.QAResponse, 1)

	go func() {
		defer close(ch)
		defer close(done)

		// 取消传播：调用方 cancel 触发时取消 ctx，供 Execute 的 ctx.Done() 与 sink.Canceled() 感知
		ctx, cancelCtx := context.WithCancel(context.Background())
		defer cancelCtx()
		go func() {
			select {
			case <-cancel:
				cancelCtx()
			case <-ctx.Done():
			}
		}()

		sink := &QASink{
			OnChunk: func(chunk string) {
				select {
				case ch <- chunk:
				case <-cancel:
					// 客户端中途断开：丢弃该块，由 Execute 的取消检测兜底转成"已取消"
				}
			},
			Canceled: func() bool {
				select {
				case <-cancel:
					return true
				default:
				}
				return false
			},
		}

		resp, err := m.Execute(ctx, req, sink)
		if err != nil {
			done <- &contract.QAResponse{Error: err.Error()}
			return
		}
		done <- &contract.QAResponse{
			Answer:    resp.Answer,
			Sources:   resp.Sources,
			SessionID: resp.SessionID,
		}
	}()

	return ch, done
}

// emitChunk 把流式块推给 sink，并在任意取消点中止：
// 发送前检查一次、发送后再检查一次，保证"取消 → 立即停止"的语义
// （等价于原 AskStream 每个 select 发送点的取消分支）。
func emitChunk(ctx context.Context, sink *QASink, chunk string) error {
	if sink == nil || sink.OnChunk == nil {
		return nil
	}
	if canceled(ctx, sink) {
		return errCanceled
	}
	sink.OnChunk(chunk)
	if canceled(ctx, sink) {
		return errCanceled
	}
	return nil
}

// canceled 判断是否已取消：ctx 取消，或 sink.Canceled() 为真（nil 视为永不取消）。
func canceled(ctx context.Context, sink *QASink) bool {
	select {
	case <-ctx.Done():
		return true
	default:
	}
	return sink != nil && sink.Canceled != nil && sink.Canceled()
}

// streamCancel 把 ctx 与 sink.Canceled 合并成 ChatStream 的取消通道：
// 任一触发即关闭，通知 llm 模块打断流式请求（连接阶段/背退等待/读侧监视）。
func streamCancel(ctx context.Context, sink *QASink) <-chan struct{} {
	if sink == nil || sink.Canceled == nil {
		return ctx.Done()
	}
	c := make(chan struct{})
	go func() {
		defer close(c)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			if sink.Canceled() {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return c
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

		// 文件未索引（无分块）：明确提示而非用空上下文调用 LLM。
		// 修复：此前空 fullText 会进入"全文直发"分支产生空气泡/LLM 返回空。
		if len(chunks) == 0 {
			return nil, nil, fmt.Errorf("[qa] 文件尚未索引，无法回答其内容（请在文档索引页确认该文件索引成功）")
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
			queryVec, err := m.llm.EmbedQuery(req.Question)
			if err != nil {
				return nil, nil, fmt.Errorf("[qa] 嵌入问题失败: %w", err)
			}
			// 全库搜索取回 2×块数，容忍其他文件高分块挤占，再按 fileID 过滤
			entries, err := m.storage.VectorsSearch(queryVec, len(chunks)*2)
			if err == nil {
				type scoredChunk struct {
					chunk *contract.Chunk
					score float64
				}
				var scored []scoredChunk
				for _, e := range entries {
					// 宽召回不过滤余弦分：阈值（此前 0.3/0.1）会在重排前丢掉相关块，
					// 即使配了重排也"找不到"。全部候选交给重排精排决定。
					if e.ChunkID == 0 {
						continue
					}
					chunk, err := m.storage.ChunksGet(e.ChunkID)
					if err != nil || chunk == nil || chunk.FileID != req.FileID {
						continue
					}
					scored = append(scored, scoredChunk{chunk: chunk, score: e.Score})
				}
				// 重排精排（若已配置）：交叉编码器对候选块重新打分，替代余弦分数
				if len(scored) > 1 {
					texts := make([]string, len(scored))
					for i := range scored {
						texts[i] = scored[i].chunk.Text
					}
					if rs, rerr := m.llm.Rerank(req.Question, texts); rerr == nil && len(rs) == len(scored) {
						for i := range scored {
							scored[i].score = rs[i]
						}
					}
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
	} else {
		// 全局模式：Embed 查询 + 全库宽召回（top-100），再用重排精排取前 8
		queryVec, err := m.llm.EmbedQuery(req.Question)
		if err != nil {
			return nil, nil, fmt.Errorf("[qa] 嵌入问题失败: %w", err)
		}
		entries, err := m.storage.VectorsSearch(queryVec, 100)
		if err == nil {
			type scored struct {
				chunk *contract.Chunk
				file  *contract.FileInfo
				score float64
			}
			// 宽召回不过滤余弦分：阈值（此前 0.3/0.1）会在重排前丢掉相关块，
			// 且维度不匹配时余弦全为 0 会被阈值清空导致"检索为空"。全部候选交给重排精排决定。
			var scoredChunks []scored
			for _, e := range entries {
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
			// 重排精排（若已配置）：交叉编码器对候选块重新打分，替代余弦分数
			if len(scoredChunks) > 1 {
				texts := make([]string, len(scoredChunks))
				for i := range scoredChunks {
					texts[i] = scoredChunks[i].chunk.Text
				}
				if rs, rerr := m.llm.Rerank(req.Question, texts); rerr == nil && len(rs) == len(scoredChunks) {
					for i := range scoredChunks {
						scoredChunks[i].score = rs[i]
					}
				}
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

	// 标题模糊搜索兜底：仅全局模式、语义检索无结果（或结果过少）时，
	// 按问题中的关键词模糊匹配文件名，把匹配文件的片段作为补充上下文，
	// 让 AI 至少能定位"哪个文件"（用户需求：AI 对话支持文件内容 + 标题搜索）。
	// 单文件模式不触发：文件内容已直发/检索，全库标题搜索会混入其他文件（review 发现）。
	if req.Mode != "file" && len(contextBlocks) < 2 {
		keyword := extractFilenameKeyword(req.Question)
		if keyword != "" {
			matched, err := m.storage.FilesFindByName(keyword, 5)
			if err != nil {
				// 标题搜索失败不影响主流程，记录日志便于排查（review 建议）
				logx.Warn("qa", "标题模糊搜索失败", "keyword", keyword, "err", err.Error())
			} else {
				usedRunes := 0
				for _, f := range matched {
					chunks, cerr := m.storage.ChunksByFile(f.ID)
					if cerr != nil || len(chunks) == 0 {
						continue
					}
					// 取该文件前若干分块作为标题匹配上下文（带"标题匹配"标注）
					limit := 3
					if len(chunks) < limit {
						limit = len(chunks)
					}
					overLimit := false
					for i := 0; i < limit; i++ {
						block := fmt.Sprintf("[文件=%s, 段落=%d, 标题匹配]\n%s", f.RelPath, chunks[i].Seq, chunks[i].Text)
						usedRunes += utf8.RuneCountInString(block)
						if usedRunes > m.maxContextChars {
							overLimit = true
							break
						}
						contextBlocks = append(contextBlocks, block)
						sources = append(sources, contract.QASource{RelPath: f.RelPath, Seq: chunks[i].Seq})
					}
					if overLimit {
						break // 上下文已超限，不再处理剩余文件（review 建议）
					}
				}
			}
		}
	}

	// 检索诊断日志：便于排查"找不到文件"——命中块数 / 命中文件，配合 llm 模块的重排日志定位环节
	if len(contextBlocks) > 0 {
		var topPaths []string
		for i, s := range sources {
			if i >= 3 {
				break
			}
			topPaths = append(topPaths, s.RelPath)
		}
		logx.Debug("qa", "检索命中", "mode", req.Mode, "blocks", len(contextBlocks), "top", strings.Join(topPaths, ","))
	} else {
		logx.Warn("qa", "检索为空", "mode", req.Mode, "question", req.Question)
	}

	return contextBlocks, sources, nil
}

// Sessions 列出所有会话
func (m *Module) Sessions() ([]*contract.QASession, error) {
	return m.storage.QASessionsList()
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

// linkifySourceRefs 把回答中的文件路径规整为 [文件=path] 引用标记。
// 推理模型输出不稳定：有时用反引号包裹路径（`10分类汇总.xlsx`）、有时裸写路径，
// 而不是提示词要求的 [文件=路径, 段落=N] 标记，导致前端无法转成可点击链接
// （修复：检索到文件却没超链接）。对每个检索来源路径，把其在回答中的出现
// （含反引号包裹形式）替换为 [文件=path]；已存在的 [文件=...] 标记原样保留。
func linkifySourceRefs(answer string, sources []contract.QASource) string {
	if len(sources) == 0 {
		return answer
	}

	// 1. 保护已有 [文件=...] 标记，避免后续替换误伤
	refRe := regexp.MustCompile(`\[文件=[^\]]+\]`)
	var refs []string
	marked := refRe.ReplaceAllStringFunc(answer, func(m string) string {
		refs = append(refs, m)
		return fmt.Sprintf("\x00REF%d\x00", len(refs)-1)
	})

	// 2. 收集来源路径，按长度降序处理（避免短路径覆盖长路径的子串）
	paths := make([]string, 0, len(sources))
	seen := map[string]bool{}
	for _, s := range sources {
		p := strings.TrimSpace(s.RelPath)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })

	// 3. 路径 → 临时占位符（含反引号包裹形式），再统一还原为标记，避免替换自嵌套
	placeholders := make(map[string]string, len(paths))
	for i, p := range paths {
		ph := fmt.Sprintf("\x00SRC%d\x00", i)
		placeholders[p] = ph
		btRe := regexp.MustCompile("`" + regexp.QuoteMeta(p) + "`")
		marked = btRe.ReplaceAllString(marked, ph)
		marked = strings.ReplaceAll(marked, p, ph)
	}
	for p, ph := range placeholders {
		marked = strings.ReplaceAll(marked, ph, "[文件="+p+"]")
	}

	// 4. 还原原有 [文件=...] 标记
	for i, m := range refs {
		marked = strings.ReplaceAll(marked, fmt.Sprintf("\x00REF%d\x00", i), m)
	}
	return marked
}

// extractFilenameKeyword 从问题中提取用于文件名模糊搜索的关键词。
// 优先级：引号/书名号内的文字（如“预算表”、“方案.xlsx”）> 去疑问词后的短语。
// 提取失败返回空串（不触发标题搜索兜底）。
func extractFilenameKeyword(question string) string {
	// 中文引号、英文引号、书名号内的文字优先（用户常这样指代文件）
	if m := quotedKeywordRe.FindStringSubmatch(question); len(m) > 1 && strings.TrimSpace(m[1]) != "" {
		return strings.TrimSpace(m[1])
	}

	// 无引号：去掉常见疑问/指示词后取核心短语（文件名通常在句末）
	kw := strings.TrimSpace(question)
	kw = strings.TrimRight(kw, "？?。！!，, ")
	// 去掉句首引导词
	for _, prefix := range []string{"帮我看看", "帮我查一下", "帮我查", "看看", "查一下", "这个", "那个", "文件", "关于"} {
		if strings.HasPrefix(kw, prefix) {
			kw = strings.TrimSpace(strings.TrimPrefix(kw, prefix))
			break
		}
	}
	// 若仍含"文件"字样，保留"文件"及其前面的内容（如"会议纪要文件的内容" → "会议纪要文件"），
	// 文件名主体通常在"文件"之前
	if idx := strings.Index(kw, "文件"); idx >= 0 {
		kw = kw[:idx+len("文件")]
	}
	// 控制长度：过长关键词模糊匹配几乎必然落空，截取前 12 字
	runes := []rune(kw)
	if len(runes) > 12 {
		runes = runes[:12]
	}
	return strings.TrimSpace(string(runes))
}

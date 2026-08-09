// Package tag 标签模块
// 自动打标、标签库、建议确认、手动覆盖
package tag

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"memora/internal/contract"
)

// IStorage tag 模块所需的 storage 接口
type IStorage interface {
	FilesGet(id int64) (*contract.FileInfo, error)
	FilesMarkStatus(id int64, status, lastError string) error
	ChunksByFile(fileID int64) ([]*contract.Chunk, error)
	TagsList() ([]*contract.TagInfo, error)
	TagsGetByName(name string) (*contract.TagInfo, error)
	TagsCreate(name, source string) (int64, error)
	FileTagsReplace(fileID int64, tags []contract.FileTag) error
	FileTagsListByFile(fileID int64) ([]contract.FileTag, error)
	OverridesAppend(fileID int64, tagName, action string) error
	SuggestionsAdd(name, reason string, suggestedByFile int64) (int64, error)
	SuggestionsListPending() ([]*contract.TagSuggestion, error)
	SuggestionsSetStatus(id int64, status string) error
}

// ILLM tag 模块所需的 llm 接口
type ILLM interface {
	ChatJSON(system, user, schemaDesc string, result interface{}) error
}

// IEvents tag 模块所需的事件接口
type IEvents interface {
	Notify(topic string, data interface{})
}

// Module 标签模块
type Module struct {
	storage     IStorage
	llm         ILLM
	events      IEvents
	mu          sync.Mutex
	forbidden   []string // 被拒 3 次的候选标签名
	rejectCount map[string]int
}

// New 创建标签模块
func New(storage IStorage, llm ILLM, events IEvents) *Module {
	m := &Module{
		storage:     storage,
		llm:         llm,
		events:      events,
		forbidden:   make([]string, 0),
		rejectCount: make(map[string]int),
	}

	// 种子化预定义标签
	m.seedPredefined()

	return m
}

// seedPredefined 将预定义标签写入标签库（幂等）
func (m *Module) seedPredefined() {
	for _, name := range predefinedTags {
		existing, err := m.storage.TagsGetByName(name)
		if err != nil || existing != nil {
			continue
		}
		if _, err := m.storage.TagsCreate(name, "predefined"); err != nil {
			fmt.Printf("[tag] 种子化标签 %s 失败: %v\n", name, err)
		}
	}
}

// predefinedTags 预定义标签库
var predefinedTags = []string{
	"合同", "报告", "会议纪要", "数据", "图纸", "简历", "发票",
	"方案", "清单", "学习笔记", "通知", "制度", "流程", "分析",
	"审批", "日程", "通讯录", "表单", "模板", "其他",
}

// ProcessFile 对文件自动打标（§4.9 内部流程）
func (m *Module) ProcessFile(file *contract.FileInfo) error {
	// Step 1: 取文本前 8000 字符
	chunks, err := m.storage.ChunksByFile(file.ID)
	if err != nil {
		return fmt.Errorf("[tag] 获取分块失败: %w", err)
	}

	var sample string
	for _, c := range chunks {
		sample += c.Text
		if utf8.RuneCountInString(sample) >= 8000 {
			break
		}
	}
	if utf8.RuneCountInString(sample) > 8000 {
		runes := []rune(sample)
		sample = string(runes[:8000])
	}

	if sample == "" {
		return nil
	}

	// Step 2: 调用 LLM 打标（附录 B.1 模板）
	forbiddenStr := strings.Join(m.forbidden, "、")
	if forbiddenStr == "" {
		forbiddenStr = "（无）"
	}

	systemPrompt := fmt.Sprintf(
		`你是文档分类助手。从下面的预定义标签库中为文档选择 1~3 个最贴切的标签；仅当确实没有合适标签时，才建议 1~2 个新标签并说明理由。禁止使用禁用词列表中的标签。只输出 JSON，格式：{"tags":["标签1","标签2"],"new_tags":[{"name":"新标签","reason":"理由"}]}
标签库：%s
禁用词：%s`,
		strings.Join(predefinedTags, "、"), forbiddenStr,
	)

	var result struct {
		Tags    []string `json:"tags"`
		NewTags []struct {
			Name   string `json:"name"`
			Reason string `json:"reason"`
		} `json:"new_tags"`
	}

	err = m.llm.ChatJSON(systemPrompt, sample, "标签JSON", &result)
	if err != nil {
		// 模型不可用 → 保留未打标，不阻塞
		fmt.Printf("[tag] LLM 打标失败（跳过）: %v\n", err)
		return nil
	}

	// Step 3: 写入标签
	var fileTags []contract.FileTag
	for _, tagName := range result.Tags {
		tagName = strings.TrimSpace(tagName)
		if tagName == "" {
			continue
		}
		fileTags = append(fileTags, contract.FileTag{
			Name:   tagName,
			Origin: "auto",
		})
	}

	if err := m.storage.FileTagsReplace(file.ID, fileTags); err != nil {
		return fmt.Errorf("[tag] 写入标签失败: %w", err)
	}

	// Step 4: 新标签写入建议
	for _, nt := range result.NewTags {
		nt.Name = strings.TrimSpace(nt.Name)
		if nt.Name == "" {
			continue
		}
		id, err := m.storage.SuggestionsAdd(nt.Name, nt.Reason, file.ID)
		if err != nil {
			fmt.Printf("[tag] 写入标签建议失败: %v\n", err)
			continue
		}
		m.events.Notify("suggestion_new", map[string]interface{}{
			"id":     id,
			"name":   nt.Name,
			"reason": nt.Reason,
		})
	}

	// 广播 tag_done
	m.events.Notify("tag_done", map[string]interface{}{
		"fileId":  file.ID,
		"relPath": file.RelPath,
		"tags":    result.Tags,
	})

	return nil
}

// ManualOverride 手动覆盖标签
func (m *Module) ManualOverride(fileID int64, add, remove []string) error {
	// 获取当前标签
	file, err := m.storage.FilesGet(fileID)
	if err != nil || file == nil {
		return fmt.Errorf("文件不存在")
	}

	// 从 DB 读取当前标签
	currentTags, err := m.storage.FileTagsListByFile(fileID)
	if err != nil {
		return fmt.Errorf("[tag] 读取当前标签失败: %w", err)
	}

	// 构建移除集合
	removeSet := make(map[string]bool)
	for _, name := range remove {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		removeSet[name] = true
		m.storage.OverridesAppend(fileID, name, "remove")
	}

	// 构建添加集合
	addSet := make(map[string]bool)
	for _, name := range add {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > 20 {
			continue
		}
		addSet[name] = true
		m.storage.OverridesAppend(fileID, name, "add")
	}

	// 合并：保留当前标签中不在 remove 列表的，加上 add 列表
	var fileTags []contract.FileTag
	seen := make(map[string]bool)

	// 保留原有标签（排除被移除的）
	for _, ct := range currentTags {
		if removeSet[ct.Name] {
			continue
		}
		if addSet[ct.Name] {
			// 如果 add 列表也有此标签，用 manual origin 覆盖
			fileTags = append(fileTags, contract.FileTag{Name: ct.Name, Origin: "manual"})
		} else {
			fileTags = append(fileTags, ct)
		}
		seen[ct.Name] = true
	}

	// 新增标签
	for _, name := range add {
		if !seen[name] {
			fileTags = append(fileTags, contract.FileTag{Name: name, Origin: "manual"})
			seen[name] = true
		}
	}

	// 全量替换
	if err := m.storage.FileTagsReplace(fileID, fileTags); err != nil {
		return fmt.Errorf("[tag] 更新标签失败: %w", err)
	}

	return nil
}

// ListLibrary 列出标签库
func (m *Module) ListLibrary() ([]*contract.TagInfo, error) {
	tags, err := m.storage.TagsList()
	if err != nil {
		return nil, err
	}
	return tags, nil
}

// ListSuggestions 列出待确认标签建议
func (m *Module) ListSuggestions() ([]*contract.TagSuggestion, error) {
	return m.storage.SuggestionsListPending()
}

// AcceptSuggestion 接受标签建议
func (m *Module) AcceptSuggestion(id int64) error {
	suggestions, err := m.storage.SuggestionsListPending()
	if err != nil {
		return err
	}

	var target *contract.TagSuggestion
	for _, s := range suggestions {
		if s.ID == id {
			target = s
			break
		}
	}
	if target == nil {
		return fmt.Errorf("建议不存在或已处理")
	}

	// 创建标签（同名容错）
	tag, err := m.storage.TagsGetByName(target.Name)
	if err != nil {
		return fmt.Errorf("[tag] 查询标签失败: %w", err)
	}
	if tag == nil {
		tagID, err := m.storage.TagsCreate(target.Name, "user_confirmed")
		if err != nil {
			return fmt.Errorf("[tag] 创建标签失败: %w", err)
		}
		tag = &contract.TagInfo{ID: tagID}
	}

	// 将标签应用到建议来源文件
	currentTags, err := m.storage.FileTagsListByFile(target.SuggestedByFile)
	if err != nil {
		return fmt.Errorf("[tag] 读取文件标签失败: %w", err)
	}
	// 检查是否已有此标签
	alreadyHas := false
	for _, ct := range currentTags {
		if ct.Name == target.Name {
			alreadyHas = true
			break
		}
	}
	if !alreadyHas {
		currentTags = append(currentTags, contract.FileTag{
			Name:   target.Name,
			Origin: "manual", // 用户确认视为手动
		})
		if err := m.storage.FileTagsReplace(target.SuggestedByFile, currentTags); err != nil {
			return fmt.Errorf("[tag] 应用标签到文件失败: %w", err)
		}
	}

	return m.storage.SuggestionsSetStatus(id, "accepted")
}

// RejectSuggestion 拒绝标签建议
func (m *Module) RejectSuggestion(id int64) error {
	suggestions, err := m.storage.SuggestionsListPending()
	if err != nil {
		return err
	}

	var target *contract.TagSuggestion
	for _, s := range suggestions {
		if s.ID == id {
			target = s
			break
		}
	}
	if target == nil {
		return fmt.Errorf("建议不存在或已处理")
	}

	// 记录拒绝次数
	m.mu.Lock()
	m.rejectCount[target.Name]++
	if m.rejectCount[target.Name] >= 3 {
		m.forbidden = append(m.forbidden, target.Name)
	}
	m.mu.Unlock()

	return m.storage.SuggestionsSetStatus(id, "rejected")
}

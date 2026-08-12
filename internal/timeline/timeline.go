// Package timeline 时间脉络模块
// 版本摘要、提交建议与历史回退
package timeline

import (
	"fmt"
	"strings"
	"time"

	"memora/internal/contract"
)

// IGit timeline 所需的 git 接口
type IGit interface {
	Log() ([]*contract.CommitInfo, error)
	DiffStats(hash string) (*contract.DiffStat, error)
	FileHistory(relPath string) ([]*contract.CommitInfo, error)
	ShowFileAt(relPath, hash string) (string, error)
	RestoreFile(relPath, hash string) error
	Status() (map[string]string, error)
	DiffContents() (string, error)                   // 提交前把变动文本喂给 AI
	CommitAuto(files []string) (string, bool, error) // 恢复前自动快照用
}

// IStorage timeline 所需的 storage 接口
type IStorage interface {
	FilesList(status, tag string, page, pageSize int, sortOrder string) ([]*contract.FileInfo, int, error)
	FilesFindByRelPath(relPath string) (*contract.FileInfo, error)
	SummariesUpsert(hash, summary string, genAt int64) error
	SummariesGet(hash string) (*contract.CommitSummary, error)
	TagsList() ([]*contract.TagInfo, error)
	FileTagsListByTag(tagID int64) ([]int64, error)
}

// ILLM timeline 所需的 llm 接口
type ILLM interface {
	Chat(system, user string, opts *contract.ChatOptions) (string, error)
}

// IEvents timeline 所需的事件接口
type IEvents interface {
	Notify(topic string, data interface{})
}

// Module 时间线模块
type Module struct {
	git       IGit
	storage   IStorage
	llm       ILLM
	events    IEvents
	workspace string
}

// New 创建时间线模块
func New(git IGit, storage IStorage, llm ILLM, events IEvents, workspace string) *Module {
	return &Module{
		git:       git,
		storage:   storage,
		llm:       llm,
		events:    events,
		workspace: workspace,
	}
}

// SuggestCommitMessage 根据当前未提交的变动，用 AI 生成一句中文提交备注。
// 若 LLM 未配置或生成失败，返回错误，由调用方按“无 AI 建议”流程处理。
func (m *Module) SuggestCommitMessage() (string, error) {
	diffText, err := m.git.DiffContents()
	if err != nil {
		return "", fmt.Errorf("[timeline] 读取未提交变动失败: %w", err)
	}
	if diffText == "" {
		return "", fmt.Errorf("[timeline] 当前没有未提交的变动")
	}

	system := "你是 Git 提交助手。根据下方列出的改动文件与内容摘要，用一句简洁的中文写出提交备注。要求：不超过 30 个汉字，不用引号，不解释，只输出备注本身。"
	user := diffText

	opts := &contract.ChatOptions{
		Temperature: 0.3,
		MaxTokens:   80,
	}
	summary, err := m.llm.Chat(system, user, opts)
	if err != nil {
		return "", fmt.Errorf("[timeline] AI 生成备注失败: %w", err)
	}
	summary = strings.TrimSpace(summary)
	// 去掉 AI 可能误加的双引号或首尾空白行，取第一行
	summary = strings.Trim(summary, "\"' ")
	if idx := strings.IndexAny(summary, "\n。"); idx > 0 && idx < 70 {
		summary = summary[:idx]
	}
	if summary == "" {
		return "", fmt.Errorf("[timeline] AI 返回空备注")
	}
	return summary, nil
}

// GenerateSummary 生成提交摘要（附录 B.2 模板）
func (m *Module) GenerateSummary(commitHash string) (string, error) {
	// 检查缓存
	existing, err := m.storage.SummariesGet(commitHash)
	if err == nil && existing != nil {
		return existing.Summary, nil
	}

	// 获取 diff 统计
	stat, err := m.git.DiffStats(commitHash)
	if err != nil {
		return "", fmt.Errorf("[timeline] 获取 diff 统计失败: %w", err)
	}

	// 获取提交信息
	commits, err := m.git.Log()
	if err != nil {
		return "", err
	}

	var targetCommit *contract.CommitInfo
	for _, c := range commits {
		if c.Hash == commitHash {
			targetCommit = c
			break
		}
	}
	if targetCommit == nil {
		return "", fmt.Errorf("提交不存在")
	}

	// 用真实改动文件清单喂给 AI。此前误把提交备注当"修改文件"传入，
	// 生成的摘要等于复述备注，信息量不足（review 发现）。
	filesChanged := strings.Join(stat.Files, ", ")
	if filesChanged == "" {
		filesChanged = targetCommit.Message
	}

	// 用 LLM 生成摘要
	system := "你是版本记录助手。根据一次 Git 提交的文件改动，用 1~2 句中文总结这次提交做了什么。只输出总结文本，不要输出其他内容。"
	user := fmt.Sprintf("修改文件：%s\n改动统计：新增 %d、修改 %d、删除 %d。",
		filesChanged, stat.Added, stat.Modified, stat.Deleted)

	summary, err := m.llm.Chat(system, user, &contract.ChatOptions{Temperature: 0.3, MaxTokens: 200})
	if err != nil {
		return "", fmt.Errorf("[timeline] 生成摘要失败: %w", err)
	}

	// 缓存
	m.storage.SummariesUpsert(commitHash, summary, time.Now().UnixMilli())

	return summary, nil
}

// Restore 恢复文件到指定版本（小白视角：一键找回）。
// 工作区有未保存改动时先自动提交当前状态（「恢复前自动备份」），再执行恢复——
// 让恢复永远无痛，不再因 409 workspace_dirty 被拦下（修复：此前强制要求工作区干净）。
// 目标文件已删除时 RestoreFile 写盘即重建，同样可恢复。
func (m *Module) Restore(relPath, hash string) error {
	if relPath == "" || hash == "" {
		return fmt.Errorf("参数不完整")
	}

	status, err := m.git.Status()
	if err != nil {
		return fmt.Errorf("检查工作区状态失败: %w", err)
	}
	var dirtyFiles []string
	for f, code := range status {
		// code 为单字符：M/A/D/R/C 表示已跟踪文件的改动；'?' 为未跟踪新文件，不影响恢复
		if code != " " && code != "" && code != "?" {
			dirtyFiles = append(dirtyFiles, f)
		}
	}
	if len(dirtyFiles) > 0 {
		// 先把当前状态存为「恢复前自动备份」版本，再恢复，避免覆盖掉未保存的工作
		if _, skipped, cerr := m.git.CommitAuto(nil); cerr != nil {
			return fmt.Errorf("恢复前自动保存失败: %w", cerr)
		} else if !skipped {
			m.events.Notify("commit_done", map[string]interface{}{"auto": true})
		}
	}

	if err := m.git.RestoreFile(relPath, hash); err != nil {
		return fmt.Errorf("[timeline] 恢复文件失败: %w", err)
	}

	return nil
}

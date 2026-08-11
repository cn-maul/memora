// Package timeline 时间脉络模块
// 时间轴聚合、版本摘要、历史与回退
package timeline

import (
	"fmt"
	"path/filepath"
	"sort"
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

// Get 获取时间线（§4.11 内部流程 + 附录 C.6 分桶规则）
func (m *Module) Get(q *contract.TimelineQuery) ([]*contract.TimelineNode, error) {
	// 获取所有提交
	commits, err := m.git.Log()
	if err != nil {
		return nil, fmt.Errorf("[timeline] 获取提交日志失败: %w", err)
	}

	// 获取未跟踪文件
	untrackedFiles := m.getUntrackedFiles()

	// 分桶
	type bucketEntry struct {
		commit *contract.CommitInfo
		stat   *contract.DiffStat
	}
	buckets := make(map[string][]bucketEntry)

	for _, commit := range commits {
		bucket := bucketKey(commit.Time, q.Granularity)
		if q.From > 0 && commit.Time < q.From {
			continue
		}
		if q.To > 0 && commit.Time > q.To {
			continue
		}
		stat, _ := m.git.DiffStats(commit.Hash)
		buckets[bucket] = append(buckets[bucket], bucketEntry{
			commit: commit,
			stat:   stat,
		})
	}

	// 未跟踪文件按 mtime 入桶
	for _, f := range untrackedFiles {
		bucket := bucketKey(f.Mtime, q.Granularity)
		if _, ok := buckets[bucket]; !ok {
			buckets[bucket] = nil
		}
	}

	// 构造节点
	var nodes []*contract.TimelineNode
	for bucket, entries := range buckets {
		node := &contract.TimelineNode{
			Bucket: bucket,
			Label:  bucketLabel(bucket, q.Granularity),
		}

		for _, e := range entries {
			node.Count++
			if e.stat != nil {
				node.Added += e.stat.Added
				node.Modified += e.stat.Modified
				node.Deleted += e.stat.Deleted
				// 将已提交文件加入节点（带 commitHash）
				for _, f := range e.stat.Files {
					// 去重：同一文件在同桶多提交只保留一个
					exists := false
					for _, nf := range node.Files {
						if nf.RelPath == f {
							exists = true
							break
						}
					}
					if !exists {
						node.Files = append(node.Files, contract.TimelineFile{
							RelPath:    f,
							Mtime:      e.commit.Time,
							CommitHash: e.commit.Hash,
						})
					}
				}
			}
			// 获取摘要
			if node.Summary == "" {
				summary, _ := m.storage.SummariesGet(e.commit.Hash)
				if summary != nil {
					node.Summary = summary.Summary
				}
			}
		}

		// 添加未跟踪文件
		for _, f := range untrackedFiles {
			if bucketKey(f.Mtime, q.Granularity) == bucket {
				node.Files = append(node.Files, contract.TimelineFile{
					RelPath: f.RelPath,
					Mtime:   f.Mtime,
				})
			}
		}

		nodes = append(nodes, node)
	}

	// 标签过滤
	if len(q.TagFilter) > 0 {
		nodes = m.filterByTag(nodes, q.TagFilter)
	}

	// 排序：桶按时间倒序
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Bucket > nodes[j].Bucket
	})

	return nodes, nil
}

// bucketKey 生成桶键（附录 C.6）
func bucketKey(ts int64, granularity string) string {
	t := time.UnixMilli(ts)
	switch granularity {
	case "day":
		return t.Format("2006-01-02")
	case "week":
		// ISO 周：周一为第一天
		year, week := t.ISOWeek()
		return fmt.Sprintf("%d-W%02d", year, week)
	case "month":
		return t.Format("2006-01")
	default:
		return t.Format("2006-01-02")
	}
}

// bucketLabel 生成桶标签
func bucketLabel(bucket, granularity string) string {
	switch granularity {
	case "day":
		return bucket
	case "week":
		return fmt.Sprintf("第%s周", strings.TrimPrefix(bucket[5:], "W"))
	case "month":
		return bucket
	default:
		return bucket
	}
}

// getUntrackedFiles 获取未跟踪文件
func (m *Module) getUntrackedFiles() []*contract.FileInfo {
	// git.Status() 只返回有差异的路径；code 为单字符 '?' 表示未跟踪。
	// 已提交且无变化的文件不会出现在 status 中，因此不能反推"不在 map 即未跟踪"。
	status, err := m.git.Status()
	if err != nil {
		return nil
	}

	var relPaths []string
	for relPath, code := range status {
		if code == "?" {
			relPaths = append(relPaths, relPath)
		}
	}
	if len(relPaths) == 0 {
		return nil
	}

	// 按 relPath 集合过滤 storage 中的文件。
	// git.Status() 的键恒为 "/" 分隔（go-git 内部统一正斜杠），而 storage 的 rel_path
	// 在 Windows 上是反斜杠，需归一化后再比对（review 发现：Windows 下未跟踪文件不显示）。
	fileSet := make(map[string]bool, len(relPaths))
	for _, p := range relPaths {
		fileSet[p] = true
	}
	files, _, err := m.storage.FilesList("", "", 0, 5000, "")
	if err != nil {
		return nil
	}
	var untracked []*contract.FileInfo
	for _, f := range files {
		if fileSet[filepath.ToSlash(f.RelPath)] {
			untracked = append(untracked, f)
		}
	}
	return untracked
}

// filterByTag 按标签过滤节点
func (m *Module) filterByTag(nodes []*contract.TimelineNode, tags []string) []*contract.TimelineNode {
	tagSet := make(map[string]bool)
	for _, t := range tags {
		tagSet[t] = true
	}

	allTags, err := m.storage.TagsList()
	if err != nil {
		return nodes
	}

	var filtered []*contract.TimelineNode
	for _, node := range nodes {
		var taggedFiles []contract.TimelineFile
		for _, f := range node.Files {
			file, err := m.storage.FilesFindByRelPath(f.RelPath)
			if err != nil || file == nil {
				continue
			}
			// 检查标签
			for _, ti := range allTags {
				if tagSet[ti.Name] {
					fids, _ := m.storage.FileTagsListByTag(ti.ID)
					for _, fid := range fids {
						if fid == file.ID {
							taggedFiles = append(taggedFiles, f)
							break
						}
					}
				}
			}
		}
		if len(taggedFiles) > 0 {
			node.Files = taggedFiles
			filtered = append(filtered, node)
		}
	}
	return filtered
}

// NodeDetail 获取节点详情（暂简单实现）
func (m *Module) NodeDetail(node *contract.TimelineNode) error {
	return nil
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

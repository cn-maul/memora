// Package git go-git 封装模块
// 负责版本管理：提交/历史/diff/还原/初始化
// 所有 git 调用在 taskqueue 单处理器内串行执行，天然安全
package git

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"memora/internal/contract"
	"memora/internal/logx"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// ConfigProvider Git 配置获取接口
type ConfigProvider interface {
	GetAutoCommitConfig() (enabled bool, debounceSec int)
	GetGitAuthor() (name, email string)
}

// Module git 模块
// 注：go-git 的 Repository/Worktree 非线程安全，HTTP 处理器与 taskqueue 任务
// 可能并发调用，故所有公开方法通过 mu 串行化（读共享锁/写独占锁）。
type Module struct {
	mu   sync.RWMutex
	repo *gogit.Repository
	path string // 工作目录
	cfg  ConfigProvider
}

// New 创建 git 模块（不立即初始化仓库）
func New(cfg ConfigProvider) *Module {
	return &Module{
		cfg: cfg,
	}
}

// EnsureRepo 确保工作目录已初始化 Git 仓库
func (m *Module) EnsureRepo(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.path = path

	// 先探测是否存在
	repo, err := gogit.PlainOpen(path)
	if err != nil {
		// 不存在则初始化
		repo, err = gogit.PlainInit(path, false)
		if err != nil {
			return fmt.Errorf("[git] 初始化仓库失败: %w", err)
		}
	}

	m.repo = repo

	// 无论新建还是复用已有仓库，都确保 .gitignore 拒绝 .memora/，
	// 避免 API Key（.memora/config.json）、数据库、问答缓存进入 Git 历史。
	// （修复：原实现 PlainOpen 分支直接返回，漏掉对已有仓库的检查）
	if err := ensureMemoraIgnored(path); err != nil {
		logx.Warn("git", "写入 .gitignore 失败", "err", err.Error())
	}

	// 首次初始化：空仓库（无任何提交）时立即把工作区全部文件提交为「初始版本」。
	// 让 Timeline 从第一天就有基线版本，"找回"有起点（修复：此前直到首次文件变更才有提交）。
	// 已有提交的仓库会被幂等跳过。
	m.commitInitialLocked()

	// 泄漏扫描：工作树 + index 中是否存在 .memora 路径（仅检测与记录，不执行密钥轮换）。
	// 泄露 = 已 tracked 且路径含 .memora：即使补上 ignore 也救不回历史，需人工处理。
	leaks, scanErr := ScanForMemoraLeaks(path)
	switch {
	case scanErr != nil:
		logx.Warn("git", "泄漏扫描失败", "err", scanErr.Error())
	case len(leaks) > 0:
		logx.Error("git", "检测到 .memora 敏感数据已被 Git 跟踪（泄露）",
			"files", strings.Join(leaks, ","),
			"hint", "从 index 移除：git rm -r --cached .memora 并提交；若已推送需清理远端历史并轮换密钥")
	}

	return nil
}

// memoraIgnoreRule .gitignore 中拒绝 .memora 数据目录的规则文本
const memoraIgnoreRule = "\n# Memora 软件数据目录\n.memora/\n"

// ensureMemoraIgnored 确保 .gitignore 包含拒绝 .memora/ 的规则；缺失则追加。
func ensureMemoraIgnored(path string) error {
	gitignorePath := filepath.Join(path, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err == nil && strings.Contains(string(data), ".memora") {
		return nil // 已含规则
	}
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("[git] 打开 .gitignore 失败: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(memoraIgnoreRule); err != nil {
		return fmt.Errorf("[git] 写入 .gitignore 失败: %w", err)
	}
	return nil
}

// isMemoraPath 判断路径是否为 .memora 目录内（或 .memora 自身）。
// go-git 的 index/status 路径恒用正斜杠，按路径段匹配，避免误伤 "notemora.txt" 之类文件名。
func isMemoraPath(p string) bool {
	return p == ".memora" || strings.HasPrefix(p, ".memora/") || strings.Contains(p, "/.memora/")
}

// ScanForMemoraLeaks 扫描工作树与 Git index 中是否存在 .memora 路径
// （.memora/config.json 含明文 API Key、数据库、问答缓存，绝不能进入 Git 历史）。
// 泄露 = 已 tracked（进入 index）且路径含 .memora：这类文件会被提交/推送，需人工处理。
// 本函数只检测并返回泄露路径清单，不轮换密钥（轮换需人工确认）；绝不读取/打印文件内容。
func ScanForMemoraLeaks(path string) ([]string, error) {
	repo, err := gogit.PlainOpen(path)
	if err != nil {
		return nil, fmt.Errorf("[git] 打开仓库失败: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("[git] 获取工作区失败: %w", err)
	}
	status, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("[git] 获取状态失败: %w", err)
	}

	// 工作树 status 中出现未跟踪的 .memora 路径 = ignore 未生效，add 全部时会被带进提交
	for f, s := range status {
		if isMemoraPath(f) && s.Worktree == '?' {
			logx.Warn("git", "检测到 .memora 路径未被忽略（add 时会进入提交）", "file", f)
		}
	}

	// index 中已 tracked 的 .memora 文件：真正泄露
	idx, err := repo.Storer.Index()
	if err != nil {
		return nil, fmt.Errorf("[git] 读取 index 失败: %w", err)
	}
	var leaks []string
	for _, e := range idx.Entries {
		if isMemoraPath(e.Name) {
			leaks = append(leaks, e.Name)
		}
	}
	return leaks, nil
}

// ScanHistoryForMemoraLeaks 扫描 Git 历史中所有提交的树，返回曾进入任何提交的 .memora 路径。
// 即使当前 index/工作树已清理，已写入历史的敏感文件（明文 API Key 等）仍是泄露，
// 需人工决策是否改写历史；本函数只检测与返回，绝不读取/打印文件内容。
func ScanHistoryForMemoraLeaks(path string) ([]string, error) {
	repo, err := gogit.PlainOpen(path)
	if err != nil {
		return nil, fmt.Errorf("[git] 打开仓库失败: %w", err)
	}

	found := make(map[string]bool)
	iter, err := repo.Log(&gogit.LogOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("[git] 遍历提交历史失败: %w", err)
	}
	defer iter.Close()

	err = iter.ForEach(func(c *object.Commit) error {
		tree, err := c.Tree()
		if err != nil {
			logx.Warn("git", "读取提交树失败", "commit", c.Hash.String(), "err", err.Error())
			return nil
		}
		return tree.Files().ForEach(func(f *object.File) error {
			if isMemoraPath(f.Name) {
				found[f.Name] = true
			}
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("[git] 扫描历史失败: %w", err)
	}

	var leaks []string
	for p := range found {
		leaks = append(leaks, p)
	}
	return leaks, nil
}

// commitInitialLocked 空仓库快照提交（调用方需持有 m.mu）。
// 已有提交则跳过；工作区为空（无可提交文件）也跳过，留给首次文件变更生成第一个版本。
// 失败仅记日志不阻断启动——首次自动提交会兜底。
func (m *Module) commitInitialLocked() {
	if m.repo == nil {
		return
	}
	if _, err := m.repo.Head(); err == nil {
		return // 已有提交
	}

	wt, err := m.repo.Worktree()
	if err != nil {
		logx.Warn("git", "初始版本获取工作区失败", "err", err.Error())
		return
	}
	status, err := wt.Status()
	if err != nil {
		logx.Warn("git", "初始版本读取状态失败", "err", err.Error())
		return
	}
	hasAny := false
	for f, s := range status {
		// 空仓库中所有文件均为 '?'（未跟踪）；仅有的 .gitignore 不算实质内容，跳过
		if s.Worktree == '?' && f != ".gitignore" {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return // 空工作区，暂无内容可提交
	}

	if _, err := wt.Add("."); err != nil {
		// 回退到逐文件 add（与 CommitAuto 一致的稳妥路径）
		logx.Debug("git", "初始版本 Add('.') 失败，改逐文件 add", "err", err.Error())
		for f, s := range status {
			if s.Worktree == '?' {
				if _, aerr := wt.Add(f); aerr != nil {
					// 任一 add 失败即终止本次提交，避免不完整快照混入（首次自动提交会兜底）
					logx.Warn("git", "初始版本 add 失败，放弃本次提交", "file", f, "err", aerr.Error())
					return
				}
			}
		}
	}
	now := time.Now()
	name, email := m.cfg.GetGitAuthor()
	if _, err := wt.Commit("初始版本：工作区全部文件快照", &gogit.CommitOptions{
		Author: &object.Signature{Name: name, Email: email, When: now},
	}); err != nil {
		logx.Warn("git", "初始版本提交失败", "err", err.Error())
	}
}

// Status 返回工作区状态
// 返回值 map[relPath]code，code 含义：??=未跟踪、M=修改、D=删除、A=新增
func (m *Module) Status() (map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.statusLocked()
}

func (m *Module) statusLocked() (map[string]string, error) {
	if m.repo == nil {
		return nil, fmt.Errorf("[git] 仓库未初始化")
	}

	wt, err := m.repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("[git] 获取工作区失败: %w", err)
	}

	status, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("[git] 获取状态失败: %w", err)
	}

	result := make(map[string]string)
	for path, s := range status {
		// s.Worktree 反映工作区变更
		code := string(s.Worktree)
		if code != "" && code != " " {
			result[path] = code
		}
	}
	return result, nil
}

// CommitAuto 自动提交：无变化则跳过
// 返回 hash, skipped, err
func (m *Module) CommitAuto(files []string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.repo == nil {
		return "", false, fmt.Errorf("[git] 仓库未初始化")
	}

	wt, err := m.repo.Worktree()
	if err != nil {
		return "", false, fmt.Errorf("[git] 获取工作区失败: %w", err)
	}

	// 先查状态，空则不提交
	status, err := wt.Status()
	if err != nil {
		return "", false, fmt.Errorf("[git] 获取状态失败: %w", err)
	}

	hasChanges := false
	for _, s := range status {
		if s.Worktree != ' ' && s.Worktree != 0 {
			hasChanges = true
			break
		}
	}

	if !hasChanges {
		return "", true, nil
	}

	// 逐个 add；任一 add 失败必须中止提交（修复：此前 logx.Warn 后继续，
	// 会混入预 staged 内容或产生不完整提交）
	if len(files) == 0 {
		// 未指定文件列表则 stage 全部变更（含删除）
		for f, s := range status {
			if s.Worktree != ' ' && s.Worktree != 0 {
				if _, err := wt.Add(f); err != nil {
					return "", false, fmt.Errorf("[git] add 失败 %s: %w", f, err)
				}
			}
		}
	} else {
		for _, f := range files {
			if _, err := wt.Add(f); err != nil {
				return "", false, fmt.Errorf("[git] add 失败 %s: %w", f, err)
			}
		}
	}

	// 统计变更数
	added, modified, deleted := 0, 0, 0
	for _, s := range status {
		switch s.Worktree {
		case 'A', '?':
			added++
		case 'M':
			modified++
		case 'D':
			deleted++
		}
	}

	// 口语化提交信息（面向小白）：只列有变化的部分，避免「删除 0 个」噪音
	var changeParts []string
	if modified > 0 {
		changeParts = append(changeParts, fmt.Sprintf("修改 %d 个文件", modified))
	}
	if added > 0 {
		changeParts = append(changeParts, fmt.Sprintf("新增 %d 个", added))
	}
	if deleted > 0 {
		changeParts = append(changeParts, fmt.Sprintf("删除 %d 个", deleted))
	}
	msg := "自动保存：" + strings.Join(changeParts, "、")
	if len(changeParts) == 0 {
		msg = "自动保存"
	}

	// 添加文件清单到正文
	var fileList []string
	for f, s := range status {
		if s.Worktree != ' ' && s.Worktree != 0 {
			fileList = append(fileList, fmt.Sprintf("  %s [%s]", f, string(s.Worktree)))
		}
	}
	if len(fileList) > 0 {
		msg += "\n\n" + strings.Join(fileList, "\n")
	}

	now := time.Now()
	name, email := m.cfg.GetGitAuthor()
	hash, err := wt.Commit(msg, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  name,
			Email: email,
			When:  now,
		},
	})
	if err != nil {
		return "", false, fmt.Errorf("[git] 提交失败: %w", err)
	}

	return hash.String(), false, nil
}

// CommitManual 手动提交
func (m *Module) CommitManual(message string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.repo == nil {
		return "", fmt.Errorf("[git] 仓库未初始化")
	}

	wt, err := m.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("[git] 获取工作区失败: %w", err)
	}

	// add 全部
	status, err := wt.Status()
	if err != nil {
		return "", fmt.Errorf("[git] 获取状态失败: %w", err)
	}
	for f, s := range status {
		if s.Worktree != ' ' && s.Worktree != 0 {
			if _, err := wt.Add(f); err != nil {
				return "", fmt.Errorf("[git] add 失败 %s: %w", f, err)
			}
		}
	}

	now := time.Now()
	name, email := m.cfg.GetGitAuthor()
	hash, err := wt.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  name,
			Email: email,
			When:  now,
		},
	})
	if err != nil {
		return "", fmt.Errorf("[git] 提交失败: %w", err)
	}

	return hash.String(), nil
}

// Log 获取提交历史
func (m *Module) Log() ([]*contract.CommitInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.repo == nil {
		return nil, fmt.Errorf("[git] 仓库未初始化")
	}

	iter, err := m.repo.Log(&gogit.LogOptions{})
	if err != nil {
		return nil, fmt.Errorf("[git] 获取日志失败: %w", err)
	}
	defer iter.Close()

	var commits []*contract.CommitInfo
	err = iter.ForEach(func(c *object.Commit) error {
		commits = append(commits, &contract.CommitInfo{
			Hash:    c.Hash.String(),
			Time:    c.Author.When.UnixMilli(),
			Message: c.Message,
			Author:  c.Author.Name,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("[git] 迭代日志失败: %w", err)
	}

	return commits, nil
}

// DiffStats 获取提交的改动统计
func (m *Module) DiffStats(hash string) (*contract.DiffStat, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.diffStatsLocked(hash)
}

func (m *Module) diffStatsLocked(hash string) (*contract.DiffStat, error) {
	if m.repo == nil {
		return nil, fmt.Errorf("[git] 仓库未初始化")
	}

	commit, err := m.repo.CommitObject(plumbing.NewHash(hash))
	if err != nil {
		return nil, fmt.Errorf("[git] 获取提交失败: %w", err)
	}

	stat := &contract.DiffStat{}
	parent, err := commit.Parent(0)
	if err != nil {
		// 无父提交 = 首个提交，全部算新增
		tree, err := commit.Tree()
		if err != nil {
			return nil, fmt.Errorf("[git] 获取树失败: %w", err)
		}
		tree.Files().ForEach(func(f *object.File) error {
			stat.Added++
			stat.Files = append(stat.Files, f.Name)
			return nil
		})
		return stat, nil
	}

	commitTree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("[git] 获取提交树失败: %w", err)
	}
	parentTree, err := parent.Tree()
	if err != nil {
		return nil, fmt.Errorf("[git] 获取父树失败: %w", err)
	}

	changes, err := commitTree.Diff(parentTree)
	if err != nil {
		return nil, fmt.Errorf("[git] 计算 diff 失败: %w", err)
	}

	for _, change := range changes {
		fileName := change.To.Name
		if fileName == "" {
			fileName = change.From.Name
		}
		stat.Files = append(stat.Files, fileName)

		if change.From.Name == "" {
			// From 为提交侧：文件只存在于父树中 → 本提交中删除
			stat.Deleted++
		} else if change.To.Name == "" {
			// To 为父树侧：文件只存在于本提交中 → 本提交中新增
			stat.Added++
		} else {
			stat.Modified++
		}
	}

	return stat, nil
}

// toSlash 将 OS 分隔符路径转成 go-git 树路径（正斜杠）。
// storage 的 rel_path 在 Windows 上是反斜杠，而 go-git 的 Tree.File / Log PathFilter
// 一律按 "/" 分隔匹配，直接传入会导致子目录文件的版本历史/内容/还原查不到（review 发现）。
// 在非 Windows 上是幂等操作。
func toSlash(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

// FileHistory 获取文件的历史提交。
// 用 PathFilter 仅返回真实改动该文件的提交（与父提交 diff 比较），
// 修复：此前用 tree.File 判断"提交树中是否存在"会把从未改动的文件
// 计入每次提交的版本（其他文件改动提交时该文件仍在树中），导致版本列表虚高。
func (m *Module) FileHistory(relPath string) ([]*contract.CommitInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.repo == nil {
		return nil, fmt.Errorf("[git] 仓库未初始化")
	}

	treePath := toSlash(relPath)
	iter, err := m.repo.Log(&gogit.LogOptions{
		PathFilter: func(p string) bool { return p == treePath },
	})
	if err != nil {
		return nil, fmt.Errorf("[git] 获取日志失败: %w", err)
	}
	defer iter.Close()

	var commits []*contract.CommitInfo
	err = iter.ForEach(func(c *object.Commit) error {
		commits = append(commits, &contract.CommitInfo{
			Hash:    c.Hash.String(),
			Time:    c.Author.When.UnixMilli(),
			Message: c.Message,
			Author:  c.Author.Name,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("[git] 迭代文件历史失败: %w", err)
	}

	return commits, nil
}

// ShowFileAt 获取文件在指定提交时的内容
func (m *Module) ShowFileAt(relPath, hash string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.showFileAtLocked(relPath, hash)
}

func (m *Module) showFileAtLocked(relPath, hash string) (string, error) {
	if m.repo == nil {
		return "", fmt.Errorf("[git] 仓库未初始化")
	}

	commit, err := m.repo.CommitObject(plumbing.NewHash(hash))
	if err != nil {
		return "", fmt.Errorf("[git] 获取提交失败: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return "", fmt.Errorf("[git] 获取树失败: %w", err)
	}

	// 树路径用正斜杠；Windows 下传入的反斜杠 relPath 需归一化（review 发现）
	file, err := tree.File(toSlash(relPath))
	if err != nil {
		return "", fmt.Errorf("[git] 在提交中查找文件失败: %w", err)
	}

	content, err := file.Contents()
	if err != nil {
		return "", fmt.Errorf("[git] 读取文件内容失败: %w", err)
	}

	return content, nil
}

// DiffContents 返回当前未提交工作的文本摘要。
// 用于在提交前把改动内容喂给 AI 生成备注。仅取文本文件前 ~500 字，二进制文件只列路径。
func (m *Module) DiffContents() (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.repo == nil {
		return "", nil
	}
	status, err := m.statusLocked()
	if err != nil {
		return "", err
	}
	if len(status) == 0 {
		return "", nil
	}

	const maxPerFile = 500

	// 按状态分组收集（保持稳定顺序）
	type entry struct {
		rel  string
		code string
	}
	var modified, added, deleted []entry
	for rel, code := range status {
		switch code {
		case "M":
			modified = append(modified, entry{rel, code})
		case "A", "?": // 新增 / 未跟踪
			added = append(added, entry{rel, code})
		case "D":
			deleted = append(deleted, entry{rel, code})
		default:
			modified = append(modified, entry{rel, code})
		}
	}

	var sb strings.Builder
	writeGroup := func(label string, items []entry) {
		if len(items) == 0 {
			return
		}
		sb.WriteString(label + "：\n")
		for _, it := range items {
			sb.WriteString("  - " + it.rel + "\n")
			if it.code == "D" {
				continue
			}
			full := filepath.Join(m.path, it.rel)
			data, rerr := os.ReadFile(full)
			if rerr != nil {
				continue
			}
			// 跳过明显二进制
			if isBinary(data) {
				sb.WriteString("    （二进制文件）\n")
				continue
			}
			snippet := string(data)
			if len(snippet) > maxPerFile {
				snippet = snippet[:maxPerFile] + "..."
			}
			for _, line := range strings.Split(snippet, "\n") {
				sb.WriteString("    " + line + "\n")
			}
		}
	}

	writeGroup("修改", modified)
	writeGroup("新增", added)
	writeGroup("删除", deleted)
	return strings.TrimSpace(sb.String()), nil
}

// isBinary 简单判断是否为二进制数据（含 NUL 字节）。
func isBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	limit := len(data)
	if limit > 8000 {
		limit = 8000
	}
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// currentBranchLocked 读取当前分支名（须持有 m.mu 读锁）。
// 空仓库时从 .git/HEAD 读取实际分支引用（PlainInit 会写入）；分离 HEAD 时返回短哈希。
func (m *Module) currentBranchLocked() string {
	ref, err := m.repo.Head()
	if err == nil {
		if ref.Name().IsBranch() {
			return ref.Name().Short()
		}
		// 分离 HEAD：ref.Name() 为 "HEAD"，直接取 hash 短名更有意义
		hash := ref.Hash().String()
		if len(hash) > 8 {
			hash = hash[:8]
		}
		return hash
	}

	// 空仓库（无 HEAD 引用）：从 .git/HEAD 读取分支引用，如 "ref: refs/heads/main"
	if m.path != "" {
		headFile := filepath.Join(m.path, ".git", "HEAD")
		if data, rerr := os.ReadFile(headFile); rerr == nil {
			line := strings.TrimSpace(string(data))
			if strings.HasPrefix(line, "ref: refs/heads/") {
				return strings.TrimPrefix(line, "ref: refs/heads/")
			}
		}
	}
	return "master" // 兜底
}

// Head 返回当前版本（HEAD）概要：commit id、文件总数、相对上一提交的改动文件数。
func (m *Module) Head() (*contract.HeadInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.repo == nil {
		return nil, fmt.Errorf("[git] 仓库未初始化")
	}

	head := &contract.HeadInfo{}

	// 空仓库（尚无提交）时 go-git 的 Log() 直接返回 ErrReferenceNotFound，
	// 不会走到 iter.Next() 的 io.EOF 分支；这里先探测 HEAD 引用（修复空仓库 500）。
	if _, err := m.repo.Head(); err != nil {
		head.Branch = m.currentBranchLocked()
		return head, nil // 无提交：返回空 head，HasCommits=false
	}

	iter, err := m.repo.Log(&gogit.LogOptions{})
	if err != nil {
		return nil, fmt.Errorf("[git] 获取日志失败: %w", err)
	}
	defer iter.Close()

	commit, err := iter.Next()
	if err == io.EOF {
		// 理论上不可达（已确认有 HEAD），防御性处理
		head.Branch = m.currentBranchLocked()
		return head, nil
	}
	if err != nil {
		return nil, fmt.Errorf("[git] 读取提交失败: %w", err)
	}

	head.Hash = commit.Hash.String()
	head.HasCommits = true
	head.Branch = m.currentBranchLocked()

	// 文件总数 = HEAD 树内文件数
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("[git] 获取提交树失败: %w", err)
	}
	if err := tree.Files().ForEach(func(f *object.File) error {
		head.CountFiles++
		return nil
	}); err != nil {
		return nil, fmt.Errorf("[git] 遍历文件失败: %w", err)
	}

	// 相对上一提交的改动文件数
	stat, err := m.diffStatsLocked(head.Hash)
	if err == nil {
		head.ChangedFiles = stat.Added + stat.Modified + stat.Deleted
	}

	return head, nil
}

// CommitFiles 返回指定提交内每个改动文件的明细（added|modified|deleted）。
func (m *Module) CommitFiles(hash string) ([]*contract.CommitFile, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.repo == nil {
		return nil, fmt.Errorf("[git] 仓库未初始化")
	}

	commit, err := m.repo.CommitObject(plumbing.NewHash(hash))
	if err != nil {
		return nil, fmt.Errorf("[git] 获取提交失败: %w", err)
	}

	commitTree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("[git] 获取提交树失败: %w", err)
	}

	var files []*contract.CommitFile

	parent, err := commit.Parent(0)
	if err != nil {
		// 首个提交：整树视为新增
		commitTree.Files().ForEach(func(f *object.File) error {
			files = append(files, &contract.CommitFile{Path: f.Name, Status: "added"})
			return nil
		})
		return files, nil
	}

	parentTree, err := parent.Tree()
	if err != nil {
		return nil, fmt.Errorf("[git] 获取父树失败: %w", err)
	}

	changes, err := commitTree.Diff(parentTree)
	if err != nil {
		return nil, fmt.Errorf("[git] 计算 diff 失败: %w", err)
	}

	for _, change := range changes {
		fileName := change.To.Name
		if fileName == "" {
			fileName = change.From.Name
		}
		status := "modified"
		if change.From.Name == "" {
			// From 为提交侧：文件只存在于父树中 → 本提交中删除
			status = "deleted"
		} else if change.To.Name == "" {
			// To 为父树侧：文件只存在于本提交中 → 本提交中新增
			status = "added"
		}
		files = append(files, &contract.CommitFile{Path: fileName, Status: status})
	}

	return files, nil
}

// versionDocType 根据文件扩展名返回文档类型（与 browser.detectDocType 一致）
func versionDocType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		return "pdf"
	case ".docx":
		return "docx"
	case ".pptx":
		return "pptx"
	case ".xlsx":
		return "xlsx"
	case ".txt":
		return "txt"
	case ".md":
		return "md"
	case ".doc":
		return "ignored"
	default:
		return ""
	}
}

// ListTreeAt 列出某提交快照中的全部文件（递归整树）。
// 返回文件路径、大小、文档类型，供前端浏览该提交的全部文件。
func (m *Module) ListTreeAt(hash string) ([]*contract.VersionFile, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.repo == nil {
		return nil, fmt.Errorf("[git] 仓库未初始化")
	}

	commit, err := m.repo.CommitObject(plumbing.NewHash(hash))
	if err != nil {
		return nil, fmt.Errorf("[git] 获取提交失败: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("[git] 获取树失败: %w", err)
	}

	var files []*contract.VersionFile
	err = tree.Files().ForEach(func(f *object.File) error {
		files = append(files, &contract.VersionFile{
			Path:    f.Name,
			Size:    f.Size, // File 内嵌 Blob，Size 为 int64 字段
			DocType: versionDocType(f.Name),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("[git] 遍历树文件失败: %w", err)
	}

	return files, nil
}

// RestoreFile 将文件恢复到指定提交时的版本（直接写盘）
func (m *Module) RestoreFile(relPath, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.repo == nil {
		return fmt.Errorf("[git] 仓库未初始化")
	}

	content, err := m.showFileAtLocked(relPath, hash)
	if err != nil {
		return err
	}

	fullPath := filepath.Join(m.path, relPath)
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("[git] 写回文件失败: %w", err)
	}

	return nil
}

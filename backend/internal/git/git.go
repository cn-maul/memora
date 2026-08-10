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
	if err == nil {
		m.repo = repo
		return nil
	}

	// 不存在则初始化
	repo, err = gogit.PlainInit(path, false)
	if err != nil {
		return fmt.Errorf("[git] 初始化仓库失败: %w", err)
	}

	// 创建 .gitignore 并添加 .memora/
	gitignorePath := filepath.Join(path, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil || !strings.Contains(string(data), ".memora") {
		f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			f.WriteString("\n# Memora 软件数据目录\n.memora/\n")
			f.Close()
		}
	}

	m.repo = repo
	return nil
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

	// 逐个 add
	if len(files) == 0 {
		// 未指定文件列表则 stage 全部变更（含删除）
		for f, s := range status {
			if s.Worktree != ' ' && s.Worktree != 0 {
				if _, err := wt.Add(f); err != nil {
					logx.Warn("git", "add 失败", "file", f, "err", err.Error())
				}
			}
		}
	} else {
		for _, f := range files {
			if _, err := wt.Add(f); err != nil {
				logx.Warn("git", "add 失败", "file", f, "err", err.Error())
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

	msg := fmt.Sprintf("自动提交：修改 %d、新增 %d、删除 %d", modified, added, deleted)

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
			wt.Add(f)
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

// FileHistory 获取文件的历史提交
func (m *Module) FileHistory(relPath string) ([]*contract.CommitInfo, error) {
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
		// 检查该提交是否包含此文件
		tree, err := c.Tree()
		if err != nil {
			return nil // 跳过
		}
		_, err = tree.File(relPath)
		if err == nil {
			commits = append(commits, &contract.CommitInfo{
				Hash:    c.Hash.String(),
				Time:    c.Author.When.UnixMilli(),
				Message: c.Message,
				Author:  c.Author.Name,
			})
		}
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

	file, err := tree.File(relPath)
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

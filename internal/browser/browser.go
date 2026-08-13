// Package browser 文件浏览模块
// 提供资源管理器式的目录树浏览、按文件名/路径模糊搜索、原生目录选择。
// 与索引无关：实时扫描工作区磁盘，展示全部文件与目录结构。
package browser

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"memora/internal/documentpolicy"
)

// Dialog 是对话框能力接口（供 PickDirectory/PickFile 调用，便于测试注入）。
// openDir 弹出目录选择、openFile 弹出文件选择；filters 为后缀过滤（如 []string{".exe"}）。
// 未注入时回退到浏览器包内建的 Wails 实现（app.Run 后 application.Get() 非空）。
type Dialog interface {
	openDir(title, initial string) (string, error)
	openFile(title string, filters []string, initial string) (string, error)
}

// DirEntry 目录中的一个条目（子目录或文件）
type DirEntry struct {
	Name        string `json:"name"`    // 名称（不含路径）
	RelPath     string `json:"relPath"` // 相对工作区根（目录以 / 结尾；根目录为空）
	IsDir       bool   `json:"isDir"`
	Size        int64  `json:"size,omitempty"`
	Mtime       int64  `json:"mtime,omitempty"`       // 毫秒
	DocType     string `json:"docType,omitempty"`     // 文档类型（不支持的为空）
	Indexable   bool   `json:"indexable"`             // 是否为受支持的、可建立索引的文档类型
	IndexStatus string `json:"indexStatus,omitempty"` // indexed / pending / failed / ignored / ''（未入索引库）
}

// ListDir 列出工作区下指定子目录（subPath）的内容。
// 根目录 subPath=""。返回子目录在前、文件在后，均按名称排序。
func ListDir(workspace, subPath string) ([]*DirEntry, error) {
	abs := workspace
	if subPath != "" {
		// 安全：统一走 documentpolicy.SafeJoin 词法 containment，拒绝 ../ 越界与绝对路径
		full, err := documentpolicy.SafeJoin(workspace, subPath)
		if err != nil {
			return nil, fmt.Errorf("[browser] 非法路径")
		}
		abs = full
	}

	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("[browser] 无法访问目录: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("[browser] 不是目录")
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("[browser] 读取目录失败: %w", err)
	}

	var dirs, files []*DirEntry
	for _, e := range entries {
		name := e.Name()
		// 忽略隐藏目录 / .git / .memora / node_modules 等（统一 documentpolicy 规则）
		if documentpolicy.IsIgnoredDir(name) {
			continue
		}
		rel := name
		if subPath != "" {
			rel = filepath.ToSlash(filepath.Join(subPath, name))
		}
		if e.IsDir() {
			dirs = append(dirs, &DirEntry{Name: name, RelPath: rel + "/", IsDir: true})
			continue
		}
		// 只展示普通文件
		entryInfo, err := e.Info()
		if err != nil {
			continue
		}
		docType := documentpolicy.DetectDocType(name)
		indexable := documentpolicy.IsIndexable(name)
		files = append(files, &DirEntry{
			Name:      name,
			RelPath:   rel,
			IsDir:     false,
			Size:      entryInfo.Size(),
			Mtime:     entryInfo.ModTime().UnixMilli(),
			DocType:   docType,
			Indexable: indexable,
		})
	}

	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	return append(dirs, files...), nil
}

// SearchResult 文件名搜索命中项
type SearchResult struct {
	RelPath   string `json:"relPath"`
	IsDir     bool   `json:"isDir"`
	Size      int64  `json:"size,omitempty"`
	Mtime     int64  `json:"mtime,omitempty"`
	DocType   string `json:"docType,omitempty"`
	Indexable bool   `json:"indexable"`
}

// isSubsequence 检查 query 是否为 s 的字符子序列（按序出现即算命中）。
// 比 strings.Contains 更宽松：记错字序也能命中（E4）。
func isSubsequence(s, query string) bool {
	qi := 0
	if qi >= len(query) {
		return true
	}
	for i := 0; i < len(s) && qi < len(query); i++ {
		if s[i] == query[qi] {
			qi++
		}
	}
	return qi >= len(query)
}

// SearchByName 按文件名/相对路径做大小写不敏感模糊搜索。
// 递归扫描工作区，限制返回数量（默认 100）以控制开销。
// 返回截断后的结果与真实命中总数（修复 L-1：前端截断提示需要真实 total）
func SearchByName(workspace, query string, limit int) ([]*SearchResult, int, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, 0, nil
	}
	if limit <= 0 {
		limit = 100
	}

	var results []*SearchResult
	total := 0
	walkErr := filepath.Walk(workspace, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		name := info.Name()
		// 忽略隐藏目录 / .git / .memora / node_modules 等（统一 documentpolicy 规则，
		// 避免扫到工作区内链接指向的目录内容）
		if info.IsDir() {
			if documentpolicy.IsIgnoredDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(workspace, path)
		relSlash := filepath.ToSlash(rel)
		n := strings.ToLower(name)
		r := strings.ToLower(relSlash)
		// E4：优先子串匹配；记错字序时用子序列兜底
		if strings.Contains(n, query) || strings.Contains(r, query) || isSubsequence(n, query) || isSubsequence(r, query) {
			total++
			// 只保留前 limit 条，避免大目录全量收集内存暴涨（修复 L-1：total 为真实命中数）
			if len(results) < limit {
				docType := documentpolicy.DetectDocType(name)
				results = append(results, &SearchResult{
					RelPath:   relSlash,
					Size:      info.Size(),
					Mtime:     info.ModTime().UnixMilli(),
					DocType:   docType,
					Indexable: documentpolicy.IsIndexable(name),
				})
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, 0, fmt.Errorf("[browser] 搜索失败: %w", walkErr)
	}
	return results, total, nil
}

// OpenFile 基于工作区根与相对路径构造绝对路径，校验文件存在后用系统默认程序打开。
// 仅支持工作区内文件，防止路径穿越。Windows 使用 rundll32 打开，macOS 用 open，Linux 用 xdg-open。
func OpenFile(workspace, relPath string) error {
	if workspace == "" || relPath == "" {
		return fmt.Errorf("[browser] 工作区或路径为空")
	}
	// 最终路径 containment：跟随 symlink/junction 解析，拒绝越出工作区（含链接指向外部）
	full, err := documentpolicy.FinalPath(workspace, relPath)
	if err != nil {
		return fmt.Errorf("[browser] 非法相对路径")
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		return fmt.Errorf("[browser] 文件不存在")
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", full)
	case "darwin":
		cmd = exec.Command("open", full)
	default:
		cmd = exec.Command("xdg-open", full)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("[browser] 打开文件失败: %w", err)
	}
	return nil
}

// PickDirectory 弹出原生目录选择对话框，返回所选绝对路径。
// 跨平台：通过 Wails 原生对话框（Windows COM IFileDialog / macOS NSOpenPanel / Linux GTK），
// 无需 PowerShell 或 GUI 自动化；用户取消时返回空串与 nil error。
func PickDirectory(initial string) (string, error) {
	return pickDialog("选择目录", initial, true)
}

// PickFile 弹出原生文件选择对话框，返回所选绝对路径。
// title 标题、filters 过滤后缀（如 []string{".exe"}）、initial 初始目录。
// 用户取消时返回空串与 nil error。
func PickFile(title string, filters []string, initial string) (string, error) {
	return pickDialog(title, initial, false, filters...)
}

// pickDialog 统一的原生对话框实现。isDir 为 true 时选择目录，否则选择文件。
// 非 Wails 环境（如单元测试）且未注入 Dialog 时返回错误。
func pickDialog(title, initial string, isDir bool, filters ...string) (string, error) {
	if dial := dialog; dial != nil {
		if isDir {
			return dial.openDir(title, initial)
		}
		return dial.openFile(title, filters, initial)
	}
	app := application.Get()
	if app == nil {
		return "", fmt.Errorf("[browser] 当前环境不支持原生对话框（需在 Wails 应用内调用）")
	}
	dlg := app.Dialog.OpenFile().SetTitle(title)
	if isDir {
		dlg = dlg.CanChooseDirectories(true).CanChooseFiles(false)
	} else {
		dlg = dlg.CanChooseFiles(true).CanChooseDirectories(false)
		for _, f := range filters {
			dlg = dlg.AddFilter("Files", "*"+f)
		}
	}
	if initial != "" {
		dlg = dlg.SetDirectory(initial)
	}
	path, err := dlg.PromptForSingleSelection()
	if err != nil {
		return "", fmt.Errorf("[browser] 选择失败: %w", err)
	}
	if path == "" {
		return "", nil
	}
	return path, nil
}

// dialog 可由测试注入的对话框实现（nil 时回退到 Wails 原生）。
var dialog Dialog

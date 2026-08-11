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
)

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
		// 安全：禁止越出工作区
		sub := filepath.Clean(subPath)
		if strings.HasPrefix(sub, "..") || filepath.IsAbs(sub) {
			return nil, fmt.Errorf("[browser] 非法路径")
		}
		abs = filepath.Join(workspace, sub)
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
		// 忽略隐藏目录 / .git / .memora / node_modules 等
		if strings.HasPrefix(name, ".") || name == "node_modules" {
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
		docType := detectDocType(name)
		indexable := docType != "" && docType != "ignored"
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
		// 忽略隐藏目录 / .git / .memora / node_modules
		if info.IsDir() {
			if strings.HasPrefix(name, ".") || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(workspace, path)
		relSlash := filepath.ToSlash(rel)
		if strings.Contains(strings.ToLower(name), query) || strings.Contains(strings.ToLower(relSlash), query) {
			total++
			// 只保留前 limit 条，避免大目录全量收集内存暴涨（修复 L-1：total 为真实命中数）
			if len(results) < limit {
				docType := detectDocType(name)
				results = append(results, &SearchResult{
					RelPath:   relSlash,
					Size:      info.Size(),
					Mtime:     info.ModTime().UnixMilli(),
					DocType:   docType,
					Indexable: docType != "" && docType != "ignored",
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

// detectDocType 复用索引的文档类型判定（支持的主文档类型）
func detectDocType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
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

// OpenFile 基于工作区根与相对路径构造绝对路径，校验文件存在后用系统默认程序打开。
// 仅支持工作区内文件，防止路径穿越。Windows 使用 rundll32 打开，macOS 用 open，Linux 用 xdg-open。
func OpenFile(workspace, relPath string) error {
	if workspace == "" || relPath == "" {
		return fmt.Errorf("[browser] 工作区或路径为空")
	}
	rel := filepath.Clean(filepath.FromSlash(relPath))
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("[browser] 非法相对路径")
	}
	full := filepath.Join(workspace, rel)
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

// PickDirectory 弹出 Windows 原生目录选择对话框，返回所选绝对路径。
// 使用 PowerShell + System.Windows.Forms.FolderBrowserDialog（需 -STA）。
// 非 Windows 或调用失败返回空串与错误。
func PickDirectory(initial string) (string, error) {
	if os.PathSeparator == '\\' { // Windows
		return pickWindowsDir(initial)
	}
	return "", fmt.Errorf("[browser] 当前系统不支持原生目录选择")
}

func pickWindowsDir(initial string) (string, error) {
	// 使用 -STA 以支持 FolderBrowserDialog；输出所选路径到 stdout
	// 修复：中文 Windows 上 PowerShell 默认按 GBK/OEM 代码页编码 stdout，
	// Go 端按 UTF-8 解读会乱码（中文路径变 �����z�）。先强制 stdout 用 UTF-8。
	script := `
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = "选择工作区目录"
`
	if initial != "" {
		escaped := strings.ReplaceAll(initial, "'", "''")
		script += fmt.Sprintf("$dialog.SelectedPath = '%s'\n", escaped)
	}
	script += `
if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
	Write-Output $dialog.SelectedPath
}
`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-STA", "-Command", script)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("[browser] 打开目录选择失败: %w", err)
	}
	path := strings.TrimSpace(string(output))
	if path == "" {
		// 用户取消
		return "", nil
	}
	return path, nil
}

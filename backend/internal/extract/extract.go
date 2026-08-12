// Package extract 文本提取模块
// MarkItDown 子进程提取 + 文本缓存
package extract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"memora/internal/documentpolicy"
	"memora/internal/logx"
)

const (
	// cacheVersionPrefix 缓存 key 版本前缀。缓存内容/格式发生变化时递增版本，
	// 使旧版本缓存自动失效（格式：v1-<sha256>.md）。
	cacheVersionPrefix = "v1"
	// defaultCacheQuota 缓存大小配额默认值：512MB。
	defaultCacheQuota = 512 * 1024 * 1024
)

// Module 文本提取模块
type Module struct {
	cacheDir        string // text_cache 目录
	cacheQuotaBytes int64  // 缓存大小配额；超过时按 mtime 最旧优先清理（<=0 表示不限制）
	pythonPath      string // Python 解释器路径
	command         string // MarkItDown 命令模板
	markitdownCmd   string // markitdown 直接可执行路径（优先）
	workspaceRoot   string // 工作区根；由 SetWorkspaceRoot 注入，非空时 ExtractFile 做路径 containment 校验
}

// cacheKeyFor 由内容 SHA256 生成版本化缓存 key（v1-<hash>）。
func cacheKeyFor(sum [32]byte) string {
	return fmt.Sprintf("%s-%x", cacheVersionPrefix, sum)
}

// isCacheFileName 判断文件名是否为版本化缓存项（v1-*.md）。
// 临时输出文件（mkd_*.md）不属于缓存项，不参与配额/统计/TTL 管理。
func isCacheFileName(name string) bool {
	return strings.HasPrefix(name, cacheVersionPrefix+"-") && strings.HasSuffix(name, ".md")
}

// normalizeToUTF8 将子进程输出规范化到 UTF-8。
// 兜底：即使 PYTHONIOENCODING 未生效，非法 UTF-8 字节也按 GB18030（GBK 超集）解码，
// 保证索引文本与嵌入请求始终是合法 UTF-8。
func normalizeToUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	dec := simplifiedchinese.GB18030.NewDecoder()
	out, _, err := transform.Bytes(dec, b)
	if err != nil {
		// 最终兜底：替换非法字节，保证返回合法 UTF-8
		return strings.ToValidUTF8(string(b), "\uFFFD")
	}
	return string(out)
}

// newTempOut 在缓存目录创建 markitdown 临时输出文件，返回路径（调用方负责删除）
func (m *Module) newTempOut() (string, error) {
	tmpOut, err := os.CreateTemp(m.cacheDir, "mkd_*.md")
	if err != nil {
		return "", fmt.Errorf("[extract] 创建临时文件失败: %w", err)
	}
	path := tmpOut.Name()
	tmpOut.Close()
	return path, nil
}

// runMarkitdownToFile 以参数列表方式执行 markitdown，强制输出到临时文件再读取。
// Windows 下 markitdown 重定向 stdout 输出 GBK，而 -o 参数输出 UTF-8（实测确认），
// 故必须用 -o 写文件而非捕获 stdout。
func (m *Module) runMarkitdownToFile(args []string) ([]byte, error) {
	tmpPath, err := m.newTempOut()
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpPath)

	fullArgs := append(append([]string{}, args...), "-o", tmpPath)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// 用 exec.CommandContext 创建（自动处理超时取消），随后设置 Env/Stderr——
	// 顺序关键：CommandContext 之后设置不丢失，且不需要重建 cmd
	cmd := exec.CommandContext(ctx, fullArgs[0], fullArgs[1:]...)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := fmt.Sprintf("[extract] 提取失败: %v", err)
		if stderr.Len() > 0 {
			msg += "\nstderr: " + stderr.String()
		}
		return nil, fmt.Errorf("%s", msg)
	}

	out, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("[extract] 读取转换结果失败: %w", err)
	}
	return out, nil
}

// runShellMarkitdownToFile 以 cmd /c 模板方式执行 markitdown（用户自定义 command 模板），
// 同样以 -o 输出到临时文件。
func (m *Module) runShellMarkitdownToFile(command, filePath string) ([]byte, error) {
	tmpPath, err := m.newTempOut()
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpPath)

	cmdStr := strings.ReplaceAll(command, "{file}", filePath)
	cmdStr = fmt.Sprintf("%s -o \"%s\"", cmdStr, tmpPath)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// exec.CommandContext 创建后设置 Env/Stderr，不丢失且自动处理超时
	cmd := exec.CommandContext(ctx, "cmd", "/c", cmdStr)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := fmt.Sprintf("[extract] 提取失败: %v", err)
		if stderr.Len() > 0 {
			msg += "\nstderr: " + stderr.String()
		}
		return nil, fmt.Errorf("%s", msg)
	}

	out, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("[extract] 读取转换结果失败: %w", err)
	}
	return out, nil
}

// New 创建提取模块
func New(dataDir string, pythonPath string, command string, markitdownCmd string) (*Module, error) {
	cacheDir := filepath.Join(dataDir, "text_cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("[extract] 创建缓存目录失败: %w", err)
	}
	return &Module{cacheDir: cacheDir, cacheQuotaBytes: defaultCacheQuota, pythonPath: pythonPath, command: command, markitdownCmd: markitdownCmd}, nil
}

// SetCacheQuota 调整缓存大小配额（字节）。传入 <=0 表示不限制。
func (m *Module) SetCacheQuota(bytes int64) {
	m.cacheQuotaBytes = bytes
}

// SetWorkspaceRoot 注入工作区根。注入后 ExtractFile 在读取文件前做最终路径 containment 校验，
// 阻止越界（含 junction/symlink 指向工作区外）路径被提取。
func (m *Module) SetWorkspaceRoot(root string) {
	m.workspaceRoot = root
}

// ApplyConfig 热更新 MarkItDown 运行参数（修复 H-09）
func (m *Module) ApplyConfig(pythonPath, command, markitdownCmd string) {
	if pythonPath != "" {
		m.pythonPath = pythonPath
	}
	if command != "" {
		m.command = command
	}
	if markitdownCmd != "" {
		m.markitdownCmd = markitdownCmd
	}
}

// Probe 验证 MarkItDown 命令可用
// 创建临时 txt 文件实测提取命令
func (m *Module) Probe(pythonPath, command string) (ok bool, message string) {
	// 创建临时测试文件
	tmpDir, err := os.MkdirTemp("", "memora_probe_")
	if err != nil {
		return false, fmt.Sprintf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpFile := filepath.Join(tmpDir, "test.txt")
	testContent := "Hello Memora Test"
	if err := os.WriteFile(tmpFile, []byte(testContent), 0644); err != nil {
		return false, fmt.Sprintf("写入测试文件失败: %v", err)
	}

	// 替换命令模板中的 {file} 为测试文件路径
	cmdStr := strings.ReplaceAll(command, "{file}", tmpFile)
	cmdParts := strings.Fields(cmdStr)
	if len(cmdParts) == 0 {
		return false, "命令模板为空"
	}

	var cmd *exec.Cmd
	if pythonPath != "" {
		// 用用户指定的 python 解释器直接调用 -m markitdown
		cmd = exec.Command(pythonPath, "-m", "markitdown", tmpFile)
	} else {
		cmdStr = strings.ReplaceAll(command, "{file}", tmpFile)
		cmdParts := strings.Fields(cmdStr)
		if len(cmdParts) == 0 {
			return false, "命令模板为空"
		}
		cmd = exec.Command(cmdParts[0], cmdParts[1:]...)
	}

	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Sprintf("MarkItDown 执行失败: %v\n请确认已安装: pip install markitdown", err)
	}

	if len(strings.TrimSpace(string(output))) == 0 {
		return false, "MarkItDown 输出为空"
	}

	return true, "MarkItDown 可用"
}

// ExtractFile 提取文件文本
// 返回提取的 Markdown 文本和缓存键（版本化 SHA256，格式 v1-<hash>）
func (m *Module) ExtractFile(filePath string) (text string, cacheKey string, err error) {
	// 最终路径 containment：若已注入工作区根，读取前校验解析后的真实路径仍位于工作区内
	if m.workspaceRoot != "" {
		if cerr := documentpolicy.EnsureWithin(m.workspaceRoot, filePath); cerr != nil {
			return "", "", fmt.Errorf("[extract] 路径校验失败: %w", cerr)
		}
	}
	// 流式计算文件 SHA256：逐块读取，避免大文件整读导致内存翻倍
	file, err := os.Open(filePath)
	if err != nil {
		return "", "", fmt.Errorf("[extract] 打开文件失败: %w", err)
	}
	hashW := sha256.New()
	if _, cerr := io.Copy(hashW, file); cerr != nil {
		file.Close()
		return "", "", fmt.Errorf("[extract] 计算内容哈希失败: %w", cerr)
	}
	file.Close()
	var h [sha256.Size]byte
	copy(h[:], hashW.Sum(nil))
	cacheKey = cacheKeyFor(h)
	// 旧版本缓存可能存有 GBK 内容，读取时统一规范化
	cacheFile := filepath.Join(m.cacheDir, cacheKey+".md")
	if cachedData, err := os.ReadFile(cacheFile); err == nil {
		return normalizeToUTF8(cachedData), cacheKey, nil
	}

	// 从模块字段读取配置
	pythonPath := m.pythonPath
	command := m.command

	// 统一以 markitdown -o 临时文件方式执行（Windows 下 stdout 重定向为 GBK，-o 为 UTF-8）
	var out []byte
	if m.markitdownCmd != "" {
		out, err = m.runMarkitdownToFile([]string{m.markitdownCmd, filePath})
		if err != nil && m.pythonPath != "" {
			// 用 python -m 兜底
			out, err = m.runMarkitdownToFile([]string{m.pythonPath, "-m", "markitdown", filePath})
		}
	} else if pythonPath != "" {
		out, err = m.runMarkitdownToFile([]string{pythonPath, "-m", "markitdown", filePath})
	} else {
		out, err = m.runShellMarkitdownToFile(command, filePath)
	}
	if err != nil {
		return "", "", err
	}

	text = normalizeToUTF8(out)

	// 写入缓存
	if err := os.WriteFile(cacheFile, []byte(text), 0644); err != nil {
		logx.Warn("extract", "写入缓存失败", "err", err.Error())
	} else {
		// 新缓存写入成功后检查配额：超限时按 mtime 最旧优先清理（LRU 式）
		m.enforceCacheQuota()
	}

	return text, cacheKey, nil
}

// cacheEntry 描述一个缓存项（仅版本化缓存文件 v1-*.md，不含 mkd_* 临时文件）
type cacheEntry struct {
	path  string
	mtime time.Time
	size  int64
}

// cacheEntries 列出缓存目录中全部缓存项。
func (m *Module) cacheEntries() []cacheEntry {
	entries, err := os.ReadDir(m.cacheDir)
	if err != nil {
		return nil
	}
	var out []cacheEntry
	for _, e := range entries {
		if e.IsDir() || !isCacheFileName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, cacheEntry{path: filepath.Join(m.cacheDir, e.Name()), mtime: info.ModTime(), size: info.Size()})
	}
	return out
}

// enforceCacheQuota 缓存总大小超过配额时，按 mtime 最旧优先删除缓存项（LRU 式），
// 直到总大小低于配额；始终保留最新写入的一个缓存项。
func (m *Module) enforceCacheQuota() {
	if m.cacheQuotaBytes <= 0 {
		return
	}
	entries := m.cacheEntries()
	var total int64
	for _, e := range entries {
		total += e.size
	}
	if total <= m.cacheQuotaBytes {
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mtime.Before(entries[j].mtime) })
	for i := 0; i < len(entries)-1 && total > m.cacheQuotaBytes; i++ {
		if err := os.Remove(entries[i].path); err != nil {
			logx.Warn("extract", "配额清理删除缓存失败", "path", entries[i].path, "err", err.Error())
			continue
		}
		total -= entries[i].size
	}
}

// CacheStats 统计缓存项数量与总字节数（仅供诊断/Phase 5 摘要使用）。
func (m *Module) CacheStats() (files int, bytes int64, err error) {
	for _, e := range m.cacheEntries() {
		files++
		bytes += e.size
	}
	return files, bytes, nil
}

// CleanupExpired 删除 mtime 超过 maxAge 的缓存文件（TTL 清理），返回删除数量。
func (m *Module) CleanupExpired(maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range m.cacheEntries() {
		if !e.mtime.Before(cutoff) {
			continue
		}
		if err := os.Remove(e.path); err != nil {
			return removed, fmt.Errorf("[extract] TTL 清理删除缓存失败: %w", err)
		}
		removed++
	}
	return removed, nil
}

// Cleanup 清理缓存（整体清空缓存目录）
func (m *Module) Cleanup() error {
	return os.RemoveAll(m.cacheDir)
}

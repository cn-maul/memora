// Package documentpolicy 文档类型、忽略目录与路径约束的唯一规则来源（P2-14/P1-16）。
//
// 硬性边界：watch/browser/index/git/轮询统一复用本包的规则，
// 不得各自维护一份 detectDocType / isHeavyDirName 的复制。
package documentpolicy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DetectDocType 根据文件名/扩展名返回文档类型。
// 返回 ""（不支持）或 "ignored"（明确忽略，如旧版 .doc）。
func DetectDocType(name string) string {
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

// IsIndexable 判断文档类型是否可建立索引。
func IsIndexable(name string) bool {
	t := DetectDocType(name)
	return t != "" && t != "ignored"
}

// IsIgnoredDir 判断目录名是否应被跳过（隐藏目录、.git/.memora、重型/非文档目录）。
// 供 watch/browser/index/poll 统一使用，避免重复维护规则。
func IsIgnoredDir(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "bin", "obj", "dist", "build", "out", "target", "vendor",
		"__pycache__", ".venv", "venv", ".idea", ".vscode", ".mvn", ".gradle":
		return true
	}
	return false
}

// SafeJoin 将相对路径 join 到 root 之下，并做词法级 containment 校验。
// 拒绝绝对路径、`..` 越界、混合分隔符（Windows 上同时接受 / 与 \ 输入，统一转系统分隔符）。
// 注意：这只是词法检查，无法阻止 Windows junction/symlink 越界——最终路径校验用 FinalPath。
func SafeJoin(root, rel string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("[documentpolicy] root 为空")
	}
	if rel == "" {
		return "", fmt.Errorf("[documentpolicy] rel 为空")
	}
	rel = filepath.FromSlash(rel)
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("[documentpolicy] 路径不允许为绝对路径")
	}
	cleaned := filepath.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("[documentpolicy] 路径越界")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("[documentpolicy] 解析 root 失败: %w", err)
	}
	full := filepath.Join(rootAbs, cleaned)
	// 二次词法校验：join 后仍必须在 root 下
	if !pathWithin(full, rootAbs) {
		return "", fmt.Errorf("[documentpolicy] 路径越界: %s", rel)
	}
	return full, nil
}

// FinalPath 返回 root 下 rel 的最终绝对路径，并做最终路径 containment 校验。
// 与 SafeJoin 的区别：会跟随 symlink/junction 解析最终路径（EvalSymlinks），
// 若目标真实路径落在 root 之外则拒绝——阻止工作区内链接越界读写工作区外文件。
// 路径尚不存在时，向上回溯到最近存在的祖先做 EvalSymlinks 再拼接，保证新建路径也被校验。
func FinalPath(root, rel string) (string, error) {
	full, err := SafeJoin(root, rel)
	if err != nil {
		return "", err
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("[documentpolicy] 解析 root 失败: %w", err)
	}

	resolved, err := evalPathWithAncestor(full)
	if err != nil {
		return "", err
	}
	if !pathWithin(resolved, rootAbs) {
		return "", fmt.Errorf("[documentpolicy] 最终路径越界(链接指向工作区外): %s", rel)
	}
	return full, nil
}

// evalPathWithAncestor 从 full 开始向上找最近存在的祖先做 EvalSymlinks，再拼接剩余路径。
func evalPathWithAncestor(full string) (string, error) {
	// 若路径本身存在，直接解析
	if _, err := os.Lstat(full); err == nil {
		r, rerr := filepath.EvalSymlinks(full)
		if rerr != nil {
			return "", fmt.Errorf("[documentpolicy] 解析最终路径失败: %w", rerr)
		}
		return r, nil
	}

	// 向上回溯到存在的祖先
	cur := full
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("[documentpolicy] 找不到可解析的祖先路径: %s", full)
		}
		if _, err := os.Lstat(parent); err == nil {
			r, rerr := filepath.EvalSymlinks(parent)
			if rerr != nil {
				return "", fmt.Errorf("[documentpolicy] 解析祖先路径失败: %w", rerr)
			}
			// 拼接剩余相对段
			remain := full[len(cur):]
			base := filepath.Base(cur)
			return filepath.Join(r, base+remain), nil
		}
		cur = parent
	}
}

// pathWithin 判断 path 是否在 root 之内（含 root 自身）。
// 基于字符串前缀比较；调用方需先 Abs + Clean 归一化。
func pathWithin(path, root string) bool {
	if path == root {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(path, root+sep)
}

// EnsureWithin 校验 path 已被解析且真实位置位于 root 之内；越界返回错误。
// 供恢复/打开/提取前做最后一道防线。
func EnsureWithin(root, path string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("[documentpolicy] 解析 root 失败: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("[documentpolicy] 解析路径失败: %w", err)
	}
	if !pathWithin(resolved, rootAbs) {
		return fmt.Errorf("[documentpolicy] 路径越界")
	}
	return nil
}

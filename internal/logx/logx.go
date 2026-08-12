// Package logx 轻量结构化日志：默认输出 JSON 行，便于脚本解析与排查；可通过环境变量
// MEMORA_LOG_FORMAT=console 或 SetLogFormat 切换为人类可读的控制台格式（两种格式都会做敏感字段脱敏）。
// 用法：logx.Info("index", "开始全量重建", "total", 5, "workspace", ws)
// JSON 输出示例：{"time":"2026-08-10T08:30:00+08:00","level":"info","module":"index","msg":"开始全量重建","total":5}
// console 输出示例：2026-08-10 08:30:00.000 INF [index] 开始全量重建 total=5 workspace=ws
package logx

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// stdout/stderr 为日志输出目标。定义为包级变量以便测试重定向（logx_test.go 会替换它们）。
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

// logFormat 当前输出格式，初始由环境变量 MEMORA_LOG_FORMAT 决定，默认 "json"；
// 进程启动后可用 SetLogFormat 覆盖。最小实现：启动时读一次，不做动态监测。
var logFormat = initLogFormat()

func initLogFormat() string {
	if strings.EqualFold(os.Getenv("MEMORA_LOG_FORMAT"), "console") {
		return "console"
	}
	return "json"
}

// redactedValue 敏感字段的脱敏占位符。
const redactedValue = "***"

// SetLogFormat 切换输出格式："json"（默认）输出结构化 JSON 行，"console" 输出人类可读文本。
// 也可以在进程启动时通过环境变量 MEMORA_LOG_FORMAT=console 指定，而无需调用本函数。
func SetLogFormat(format string) {
	if strings.EqualFold(format, "console") {
		logFormat = "console"
		return
	}
	logFormat = "json"
}

// RedactValue 返回敏感值的脱敏占位符 "***"。write 在命中敏感字段名（见 isSensitiveKey）时自动调用；
// 调用方也可在明确某个值敏感时手动使用。v 仅用于签名，实际值不会被输出。
func RedactValue(_ interface{}) interface{} {
	return redactedValue
}

// isSensitiveKey 判断字段名是否需要脱敏（大小写不敏感）：
// 包含 "apikey"，或精确等于 "token"，或包含 "password"/"secret"，或精确等于 "authorization"。
// 例如 apiKey、llmApiKey、api_key、token、password、user_secret、Authorization 会被脱敏，
// 而 taskKey、fileKey、accessToken 等不会被误伤。
func isSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "apikey") ||
		k == "token" ||
		strings.Contains(k, "password") ||
		strings.Contains(k, "secret") ||
		k == "authorization"
}

// Level 日志级别
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Debug 输出 debug 级日志
func Debug(module, msg string, fields ...interface{}) { write(LevelDebug, module, msg, fields...) }

// Info 输出 info 级日志
func Info(module, msg string, fields ...interface{}) { write(LevelInfo, module, msg, fields...) }

// Warn 输出 warn 级日志
func Warn(module, msg string, fields ...interface{}) { write(LevelWarn, module, msg, fields...) }

// Error 输出 error 级日志
func Error(module, msg string, fields ...interface{}) { write(LevelError, module, msg, fields...) }

// kv 记录一个有序字段，供 console 格式按传入顺序输出。
type kv struct {
	key string
	val interface{}
}

// write 组装并输出一条日志。fields 为偶数个 key/value（key 必须为 string）。
// 敏感字段（见 isSensitiveKey）在两种格式下都会被替换为 "***"。
// 错误级日志写入 stderr，其余写入 stdout，便于外部按流分流。
func write(level Level, module, msg string, fields ...interface{}) {
	entry := map[string]interface{}{
		"time":   time.Now().Format(time.RFC3339),
		"level":  level,
		"module": module,
		"msg":    msg,
	}
	var pairs []kv
	for i := 0; i+1 < len(fields); i += 2 {
		k, ok := fields[i].(string)
		if !ok {
			continue
		}
		v := fields[i+1]
		if isSensitiveKey(k) {
			v = RedactValue(v)
		}
		entry[k] = v
		pairs = append(pairs, kv{key: k, val: v})
	}

	out := stdout
	if level == LevelError {
		out = stderr
	}

	var line string
	if logFormat == "console" {
		line = formatConsole(level, module, msg, pairs, out)
	} else {
		b, err := json.Marshal(entry)
		if err != nil {
			// 序列化失败（罕见）：降级为普通输出，保证日志不丢
			fmt.Fprintf(stderr, "{\"level\":\"error\",\"module\":\"logx\",\"msg\":\"json marshal failed\",\"err\":%q}\n", err.Error())
			return
		}
		line = string(b)
	}
	io.WriteString(out, line+"\n")
}

var levelBadge = map[Level]string{
	LevelDebug: "DBG",
	LevelInfo:  "INF",
	LevelWarn:  "WRN",
	LevelError: "ERR",
}

// formatConsole 生成人类可读的控制台行：时间 级别 [模块] 消息 键=值...。
func formatConsole(level Level, module, msg string, pairs []kv, out io.Writer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s [%s] %s",
		time.Now().Format("2006-01-02 15:04:05.000"),
		levelBadgeText(level, out),
		module, msg)
	for _, p := range pairs {
		fmt.Fprintf(&b, " %s=%v", p.key, p.val)
	}
	return b.String()
}

// levelBadgeText 返回级别的两位缩写；当输出目标是终端时附加 ANSI 颜色（Nice-to-have）。
func levelBadgeText(level Level, out io.Writer) string {
	b := levelBadge[level]
	if !isTerminal(out) {
		return b
	}
	const (
		red   = "\x1b[31m"
		yel   = "\x1b[33m"
		cyn   = "\x1b[36m"
		reset = "\x1b[0m"
	)
	switch level {
	case LevelError:
		return red + b + reset
	case LevelWarn:
		return yel + b + reset
	default:
		return cyn + b + reset
	}
}

// isTerminal 判断输出目标是否为终端（字符设备），用于决定是否加 ANSI 颜色。
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

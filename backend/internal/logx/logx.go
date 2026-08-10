// Package logx 轻量结构化日志：控制台输出 JSON 行，便于脚本解析与排查。
// 用法：logx.Info("index", "开始全量重建", "total", 5, "workspace", ws)
// 输出示例：{"time":"2026-08-10T08:30:00+08:00","level":"info","module":"index","msg":"开始全量重建","total":5}
package logx

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

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

// write 组装并输出一条 JSON 日志。fields 为偶数个 key/value（key 必须为 string）。
// 错误级日志写入 stderr，其余写入 stdout，便于外部按流分流（review 建议）。
func write(level Level, module, msg string, fields ...interface{}) {
	entry := map[string]interface{}{
		"time":   time.Now().Format(time.RFC3339),
		"level":  level,
		"module": module,
		"msg":    msg,
	}
	for i := 0; i+1 < len(fields); i += 2 {
		if k, ok := fields[i].(string); ok {
			entry[k] = fields[i+1]
		}
	}
	b, err := json.Marshal(entry)
	if err != nil {
		// 序列化失败（罕见）：降级为普通输出，保证日志不丢
		fmt.Fprintf(os.Stderr, "{\"level\":\"error\",\"module\":\"logx\",\"msg\":\"json marshal failed\",\"err\":%q}\n", err.Error())
		return
	}
	out := os.Stdout
	if level == LevelError {
		out = os.Stderr
	}
	out.Write(append(b, '\n'))
}

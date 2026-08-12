package logx

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// capture 临时替换全局输出目标（stdout/stderr）与当前格式，执行 fn 后自动恢复。
// 该函数会改写包级全局变量，因此使用它的用例一律不开 t.Parallel。
func capture(t *testing.T, fn func()) (out string, errOut string) {
	t.Helper()
	oldOut, oldErr := stdout, stderr
	oldFmt := logFormat
	t.Cleanup(func() {
		stdout, stderr = oldOut, oldErr
		logFormat = oldFmt
	})
	outBuf, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	stdout, stderr = outBuf, errBuf
	fn()
	return outBuf.String(), errBuf.String()
}

func TestRedactValue(t *testing.T) {
	if got := RedactValue("anything"); got != redactedValue {
		t.Errorf("RedactValue got %v, want %v", got, redactedValue)
	}
}

func TestJSONFormatValidAndRedacted(t *testing.T) {
	out, _ := capture(t, func() {
		SetLogFormat("json")
		Info("index", "开始全量重建",
			"total", 5,
			"apiKey", "sk-abc-123",
			"token", "t0k3n",
			"taskKey", "task-1",
			"fileKey", "file-1",
			"accessToken", "acc-1")
	})

	var m map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil {
		t.Fatalf("json 格式输出不是合法 JSON: %v\n%s", err, out)
	}
	if m["total"] != float64(5) {
		t.Errorf("total 字段未保留: %v", m["total"])
	}
	for _, k := range []string{"apiKey", "token"} {
		if got := m[k]; got != redactedValue {
			t.Errorf("%s 未脱敏: %v", k, got)
		}
	}
	for _, leak := range []string{"sk-abc-123", "t0k3n"} {
		if strings.Contains(out, leak) {
			t.Errorf("敏感值泄露到输出: %q\n%s", leak, out)
		}
	}
	for _, k := range []string{"taskKey", "fileKey", "accessToken"} {
		if m[k] == redactedValue {
			t.Errorf("非敏感字段 %s 被误脱敏", k)
		}
	}
	if m["taskKey"] != "task-1" || m["fileKey"] != "file-1" || m["accessToken"] != "acc-1" {
		t.Errorf("非敏感字段被改变: %v", m)
	}
}

func TestConsoleFormatReadableAndRedacted(t *testing.T) {
	out, _ := capture(t, func() {
		SetLogFormat("console")
		Info("transport", "开始请求",
			"requestId", "abc",
			"operationId", "op-1",
			"path", "/api/x",
			"apiKey", "sk-secret-00")
	})

	for _, want := range []string{
		"INF", "[transport]", "开始请求",
		"requestId=abc", "operationId=op-1", "path=/api/x", "apiKey=***",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("console 输出缺少 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sk-secret-00") {
		t.Errorf("敏感值泄露到输出:\n%s", out)
	}
	if strings.HasPrefix(out, "{") {
		t.Errorf("console 格式输出了 JSON 行:\n%s", out)
	}
}

func TestSetLogFormatSwitches(t *testing.T) {
	jsonOut, _ := capture(t, func() {
		SetLogFormat("json")
		Info("m", "hi", "requestId", "req-1")
	})
	if !strings.HasPrefix(jsonOut, "{") {
		t.Errorf("SetLogFormat(json) 应输出 JSON 行:\n%s", jsonOut)
	}
	if !strings.Contains(jsonOut, `"requestId":"req-1"`) {
		t.Errorf("json 输出缺少 requestId:\n%s", jsonOut)
	}

	consoleOut, _ := capture(t, func() {
		SetLogFormat("console")
		Info("m", "hi", "requestId", "req-1")
	})
	if strings.HasPrefix(consoleOut, "{") {
		t.Errorf("SetLogFormat(console) 不应输出 JSON 行:\n%s", consoleOut)
	}
	if !strings.Contains(consoleOut, "requestId=req-1") {
		t.Errorf("console 输出缺少 requestId=req-1:\n%s", consoleOut)
	}
}

func TestAllLevelsAndErrorStreams(t *testing.T) {
	out, errOut := capture(t, func() {
		SetLogFormat("json")
		Debug("m", "dbg", "k", "v")
		Info("m", "inf")
		Warn("m", "wrn")
		Error("m", "err", "Authorization", "Bearer secret-token")
	})

	for _, lvl := range []string{"debug", "info", "warn"} {
		if !strings.Contains(out, `"level":"`+lvl+`"`) {
			t.Errorf("stdout 缺少 %s 级日志:\n%s", lvl, out)
		}
	}
	if !strings.Contains(errOut, `"level":"error"`) {
		t.Errorf("stderr 缺少 error 级日志:\n%s", errOut)
	}
	if strings.Contains(out, `"level":"error"`) {
		t.Errorf("error 级日志误写入 stdout:\n%s", out)
	}
	if strings.Contains(out, "Bearer secret-token") || strings.Contains(errOut, "Bearer secret-token") {
		t.Errorf("Authorization 值泄露:\nstdout=%s\nstderr=%s", out, errOut)
	}
}

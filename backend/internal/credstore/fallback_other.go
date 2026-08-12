//go:build !windows

package credstore

import (
	"path/filepath"

	"memora/internal/logx"
)

// newPlatformStore 创建非 Windows 兜底凭据存储。
// 注意：兜底实现仅在 JSON 层做 base64 编码，不具备加密强度（等同明文），
// 仅用于开发与非 Windows 环境；生产环境必须接入系统凭据库
// （Windows DPAPI、macOS Keychain、Linux Secret Service）并在此处告警。
func newPlatformStore(dataDir string) (Store, error) {
	logx.Warn("credstore", "非 Windows 平台使用兜底凭据存储（仅 base64 编码，非加密），生产环境应告警并接入系统凭据库")
	return &fileStore{
		path:    filepath.Join(dataDir, "credentials.bin"),
		encrypt: func(b []byte) ([]byte, error) { return b, nil },
		decrypt: func(b []byte) ([]byte, error) { return b, nil },
	}, nil
}

// Package credstore 统一凭据存储。
// Windows 实现用 DPAPI（CryptProtectData）加密落盘到 <dataDir>/credentials.bin；
// 非 Windows 为基于文件的兜底实现（见 fallback_other.go，生产环境应告警）。
package credstore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Store 统一凭据存储。Windows 实现用 DPAPI（CryptProtectData）加密落盘；非 Windows 用兜底编码。
type Store interface {
	// Get 读取凭据；不存在时返回 os.ErrNotExist。
	Get(service, key string) (string, error)
	// Set 写入凭据；已存在则覆盖。
	Set(service, key string, value string) error
	// Delete 删除凭据；不存在时返回 os.ErrNotExist。
	Delete(service, key string) error
	// HasPlaintextMigration 报告是否仍存在待迁移的明文凭据（供启动流程判断是否需要执行迁移）。
	HasPlaintextMigration() bool
	// MarkPlaintextMigrated 标记明文凭据迁移已完成，避免重复迁移。
	MarkPlaintextMigrated() error
}

// New 创建凭据存储。
// Windows 返回 DPAPI 加密实现；其他平台返回基于文件的兜底实现。
func New(dataDir string) (Store, error) {
	return newPlatformStore(dataDir)
}

// fileStore 基于单一 JSON 文件（credentials.bin）的凭据存储。
// 落盘结构：service -> key -> base64(encryptedBytes)；encrypt/decrypt 由平台提供。
type fileStore struct {
	mu      sync.RWMutex
	path    string
	encrypt func([]byte) ([]byte, error)
	decrypt func([]byte) ([]byte, error)
}

// storeFile credentials.bin 的磁盘结构。
type storeFile struct {
	Version  int                          `json:"version"`
	Migrated bool                         `json:"plaintext_migrated,omitempty"`
	Entries  map[string]map[string]string `json:"entries"`
}

// credStoreVersion credentials.bin 当前版本
const credStoreVersion = 1

// Get 读取凭据；不存在返回 os.ErrNotExist。
func (s *fileStore) Get(service, key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sf, err := s.read()
	if err != nil {
		return "", err
	}
	b64, ok := sf.Entries[service][key]
	if !ok {
		return "", os.ErrNotExist
	}
	blob, err := base64.RawStdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("[credstore] 解码凭据失败 (%s/%s): %w", service, key, err)
	}
	plain, err := s.decrypt(blob)
	if err != nil {
		return "", fmt.Errorf("[credstore] 解密凭据失败 (%s/%s): %w", service, key, err)
	}
	return string(plain), nil
}

// Set 写入凭据（已存在则覆盖）。
func (s *fileStore) Set(service, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sf, err := s.read()
	if err != nil {
		return err
	}
	enc, err := s.encrypt([]byte(value))
	if err != nil {
		return fmt.Errorf("[credstore] 加密凭据失败 (%s/%s): %w", service, key, err)
	}
	if sf.Entries[service] == nil {
		sf.Entries[service] = map[string]string{}
	}
	sf.Entries[service][key] = base64.RawStdEncoding.EncodeToString(enc)
	return s.write(sf)
}

// Delete 删除凭据；不存在返回 os.ErrNotExist。
func (s *fileStore) Delete(service, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sf, err := s.read()
	if err != nil {
		return err
	}
	if _, ok := sf.Entries[service][key]; !ok {
		return os.ErrNotExist
	}
	delete(sf.Entries[service], key)
	if len(sf.Entries[service]) == 0 {
		delete(sf.Entries, service)
	}
	return s.write(sf)
}

// HasPlaintextMigration 报告是否仍存在待迁移的明文凭据（未被标记为已完成）。
func (s *fileStore) HasPlaintextMigration() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sf, err := s.read()
	if err != nil {
		return false
	}
	return !sf.Migrated
}

// MarkPlaintextMigrated 标记明文凭据迁移已完成。
func (s *fileStore) MarkPlaintextMigrated() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sf, err := s.read()
	if err != nil {
		return err
	}
	if sf.Migrated {
		return nil
	}
	sf.Migrated = true
	return s.write(sf)
}

// read 读取凭据文件；文件不存在时返回空存储。
func (s *fileStore) read() (*storeFile, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &storeFile{Version: credStoreVersion, Entries: map[string]map[string]string{}}, nil
		}
		return nil, fmt.Errorf("[credstore] 读取凭据文件失败: %w", err)
	}
	var sf storeFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("[credstore] 凭据文件损坏，解析失败: %w", err)
	}
	if sf.Version == 0 {
		sf.Version = credStoreVersion
	}
	if sf.Entries == nil {
		sf.Entries = map[string]map[string]string{}
	}
	return &sf, nil
}

// write 原子落盘凭据文件（0600，同目录临时文件 + rename）。
func (s *fileStore) write(sf *storeFile) error {
	sf.Version = credStoreVersion
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("[credstore] 序列化凭据失败: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("[credstore] 创建凭据目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("[credstore] 写入凭据失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("[credstore] 写入凭据失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("[credstore] 写入凭据失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("[credstore] 写入凭据失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("[credstore] 写入凭据失败: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("[credstore] 写入凭据失败: %w", err)
	}
	return nil
}

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"memora/internal/credstore"
)

// TestSaveToAtomic 原子写：保存后文件存在且可解析，且无遗留临时文件。
func TestSaveToAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := New(path, nil)
	if err != nil {
		t.Fatalf("New(%q) 失败: %v", path, err)
	}
	if err := m.Set("llm.model", "gpt-atomic"); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 config.json 失败: %v", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("保存后的文件不可解析: %v", err)
	}
	if c.LLM.Model != "gpt-atomic" {
		t.Fatalf("保存后 llm.model = %q, want gpt-atomic", c.LLM.Model)
	}

	// 无遗留临时文件
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Fatalf("发现遗留文件: %s", e.Name())
		}
	}
}

// TestSaveToFailureKeepsOriginal 目标文件被占用时 saveTo 返回错误且原文件内容不变（原子写）。
// Windows：Go os.Open 打开的句柄共享模式不含 FILE_SHARE_DELETE，
// 会阻塞 os.Rename 的替换（共享冲突），从而确定性模拟"目标文件被占用"。
// 非 Windows（POSIX rename 不依赖目标文件共享模式）无法以此模拟，选择跳过。
func TestSaveToFailureKeepsOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := New(path, nil)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if err := m.Set("llm.model", "keep-me"); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取原文件失败: %v", err)
	}

	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows 可确定性模拟目标文件被占用")
	}

	// 独占占用目标文件，使原子替换失败
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开目标文件失败: %v", err)
	}
	defer f.Close()

	if err := m.Set("llm.model", "should-fail"); err == nil {
		t.Fatal("目标文件被占用时 saveTo 应返回错误")
	}

	// 原文件仍可读且内容不变
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取写入失败后的文件失败: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("写入失败后原文件内容被破坏（原子写未生效）")
	}
	// 内存应回滚
	_, _, model, _ := m.GetLLMConfig()
	if model != "keep-me" {
		t.Fatalf("落盘失败后内存 llm.model = %q, want keep-me", model)
	}
}

// TestLoadFromMigratesSchema 旧 schema_version 加载后自动升级并落盘。
func TestLoadFromMigratesSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	old := `{"schema_version": 0, "llm": {"model": "old-model", "temperature": 0.5}}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatalf("写旧配置失败: %v", err)
	}

	m, err := New(path, nil)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if m.cfg.SchemaVersion != latestSchemaVersion {
		t.Fatalf("加载后 schema_version = %d, want %d", m.cfg.SchemaVersion, latestSchemaVersion)
	}
	if m.cfg.LLM.Model != "old-model" {
		t.Fatalf("迁移不应丢失既有配置 llm.model = %q", m.cfg.LLM.Model)
	}

	// 磁盘版本已升级
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取升级后文件失败: %v", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("升级后的文件不可解析: %v", err)
	}
	if c.SchemaVersion != latestSchemaVersion {
		t.Fatalf("磁盘 schema_version = %d, want %d", c.SchemaVersion, latestSchemaVersion)
	}
}

// failingStore 用于注入凭据存储写入失败
type failingStore struct{ err error }

func (f *failingStore) Get(service, key string) (string, error) { return "", f.err }
func (f *failingStore) Set(service, key, value string) error    { return f.err }
func (f *failingStore) Delete(service, key string) error        { return f.err }
func (f *failingStore) HasPlaintextMigration() bool             { return true }
func (f *failingStore) MarkPlaintextMigrated() error            { return f.err }

// TestMigrateSecretsToCredStore 明文 key 迁移到 credstore，config 内存与磁盘均不含明文。
func TestMigrateSecretsToCredStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := New(path, nil)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if err := m.UpsertSecrets("sk-llm", "sk-embed", "sk-rerank"); err != nil {
		t.Fatalf("UpsertSecrets 失败: %v", err)
	}

	store, err := credstore.New(filepath.Join(t.TempDir(), "creds"))
	if err != nil {
		t.Fatalf("credstore.New 失败: %v", err)
	}
	if err := m.MigrateSecretsToCredStore(store); err != nil {
		t.Fatalf("MigrateSecretsToCredStore 失败: %v", err)
	}

	// store 可读回
	for service, want := range map[string]string{"llm": "sk-llm", "embed": "sk-embed", "rerank": "sk-rerank"} {
		got, err := store.Get(service, "api_key")
		if err != nil {
			t.Fatalf("store.Get(%s) 失败: %v", service, err)
		}
		if got != want {
			t.Fatalf("store.Get(%s) = %q, want %q", service, got, want)
		}
	}

	// 内存明文已清空
	if _, lk, _, _ := m.GetLLMConfig(); lk != "" {
		t.Fatalf("迁移后内存 llm.api_key = %q, want 空", lk)
	}
	if _, ek, _, _ := m.GetEmbedConfig(); ek != "" {
		t.Fatalf("迁移后内存 embed.api_key = %q, want 空", ek)
	}
	if _, rk, _ := m.GetRerankConfig(); rk != "" {
		t.Fatalf("迁移后内存 rerank.api_key = %q, want 空", rk)
	}

	// 磁盘不含明文
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 config.json 失败: %v", err)
	}
	if bytes.Contains(data, []byte("sk-llm")) || bytes.Contains(data, []byte("sk-embed")) || bytes.Contains(data, []byte("sk-rerank")) {
		t.Fatal("config.json 仍含明文 api_key")
	}

	// 重新 New 后内存同样不含明文
	m2, err := New(path, nil)
	if err != nil {
		t.Fatalf("重新 New 失败: %v", err)
	}
	if _, lk2, _, _ := m2.GetLLMConfig(); lk2 != "" {
		t.Fatalf("重新加载后 llm.api_key = %q, want 空", lk2)
	}
}

// TestMigrateSecretsKeepsPlaintextOnFailure store 写入失败时保留明文（内存与磁盘）。
func TestMigrateSecretsKeepsPlaintextOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := New(path, nil)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if err := m.UpsertSecrets("sk-llm", "", ""); err != nil {
		t.Fatalf("UpsertSecrets 失败: %v", err)
	}

	store := &failingStore{err: errors.New("注入失败")}
	if err := m.MigrateSecretsToCredStore(store); err == nil {
		t.Fatal("store 写入失败时 MigrateSecretsToCredStore 应返回错误")
	}
	// 内存明文保留（原可用状态）
	if _, lk, _, _ := m.GetLLMConfig(); lk != "sk-llm" {
		t.Fatalf("store 失败后 llm.api_key = %q, want sk-llm（明文应保留）", lk)
	}
	// 磁盘明文保留（未执行清空落盘）
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 config.json 失败: %v", err)
	}
	if !bytes.Contains(data, []byte("sk-llm")) {
		t.Fatal("store 失败后磁盘明文被清空")
	}
}

// TestMigrateSecretsNoPlaintextIsNoop 无明文 key 时迁移为无副作用成功。
func TestMigrateSecretsNoPlaintextIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := New(path, nil)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	store, err := credstore.New(filepath.Join(t.TempDir(), "creds"))
	if err != nil {
		t.Fatalf("credstore.New 失败: %v", err)
	}
	if err := m.MigrateSecretsToCredStore(store); err != nil {
		t.Fatalf("无明文时迁移应成功: %v", err)
	}
}

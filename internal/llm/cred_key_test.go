package llm

import (
	"os"
	"testing"

	"memora/internal/credstore"
)

// credKeyConfig 返回明文回退的配置实现（密钥在 config 中为明文）。
type credKeyConfig struct {
	llmKey, embedKey, rerankKey string
}

func (c *credKeyConfig) GetLLMConfig() (string, string, string, float64) {
	return "http://llm.local", c.llmKey, "model", 0.2
}
func (c *credKeyConfig) GetEmbedConfig() (string, string, string, int) {
	return "http://embed.local", c.embedKey, "embed-model", 1024
}
func (c *credKeyConfig) GetRerankConfig() (string, string, string) {
	return "http://rerank.local", c.rerankKey, "rerank-model"
}

// TestCredKeyCredstoreWins credstore 命中且非空时优先于 config 明文。
func TestCredKeyCredstoreWins(t *testing.T) {
	store, err := credstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("credstore.New 失败: %v", err)
	}
	if err := store.Set("llm", "api_key", "cred-key"); err != nil {
		t.Fatalf("store.Set 失败: %v", err)
	}
	m := New(&credKeyConfig{llmKey: "plain-key"})
	m.SetCredStore(store)

	if got := m.credKey("llm", "plain-key"); got != "cred-key" {
		t.Fatalf("credKey 应返回 credstore 值，got %q", got)
	}
}

// TestCredKeyFallsBackToConfig credstore 未注入 nil 时回退 config 明文。
func TestCredKeyFallsBackToConfig(t *testing.T) {
	m := New(&credKeyConfig{llmKey: "plain-key"})

	if got := m.credKey("llm", "plain-key"); got != "plain-key" {
		t.Fatalf("credStore 为 nil 时应回退 config，got %q", got)
	}
}

// TestCredKeyEmptyCredstoreFallsBack credstore 中对应凭据缺失（os.ErrNotExist）时回退 config 明文。
func TestCredKeyEmptyCredstoreFallsBack(t *testing.T) {
	store, err := credstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("credstore.New 失败: %v", err)
	}
	m := New(&credKeyConfig{llmKey: "plain-key", embedKey: "plain-embed", rerankKey: "plain-rerank"})
	m.SetCredStore(store)

	if got := m.credKey("llm", "plain-key"); got != "plain-key" {
		t.Fatalf("credstore 为空时应回退 config，got %q", got)
	}
	if got := m.credKey("embed", "plain-embed"); got != "plain-embed" {
		t.Fatalf("embed credstore 为空时应回退 config，got %q", got)
	}
	if got := m.credKey("rerank", "plain-rerank"); got != "plain-rerank" {
		t.Fatalf("rerank credstore 为空时应回退 config，got %q", got)
	}
}

// credKeyMapStore 内存内 Store 实现（仅用于测试空凭据值回退；DPAPI 无法加密空串）。
type credKeyMapStore struct{ m map[string]map[string]string }

func (s *credKeyMapStore) Get(service, key string) (string, error) {
	v, ok := s.m[service][key]
	if !ok {
		return "", os.ErrNotExist
	}
	return v, nil
}
func (s *credKeyMapStore) Set(service, key, value string) error {
	if s.m[service] == nil {
		s.m[service] = map[string]string{}
	}
	s.m[service][key] = value
	return nil
}
func (s *credKeyMapStore) Delete(service, key string) error { return nil }
func (s *credKeyMapStore) HasPlaintextMigration() bool      { return false }
func (s *credKeyMapStore) MarkPlaintextMigrated() error     { return nil }

// TestCredKeyEmptyValueFallsBack credstore 中值存在的空字符串不被采用，继续回退 config 明文。
func TestCredKeyEmptyValueFallsBack(t *testing.T) {
	store := &credKeyMapStore{m: map[string]map[string]string{"llm": {"api_key": ""}}}
	m := New(&credKeyConfig{llmKey: "plain-key"})
	m.SetCredStore(store)

	if got := m.credKey("llm", "plain-key"); got != "plain-key" {
		t.Fatalf("credstore 值为空时应回退 config，got %q", got)
	}
}

// TestCredKeyPerService 三个服务密钥相互独立：仅命中 "embed" 时其余服务仍回退 config。
func TestCredKeyPerService(t *testing.T) {
	store, err := credstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("credstore.New 失败: %v", err)
	}
	if err := store.Set("embed", "api_key", "cred-embed"); err != nil {
		t.Fatalf("store.Set 失败: %v", err)
	}
	m := New(&credKeyConfig{llmKey: "plain-key", embedKey: "plain-embed", rerankKey: "plain-rerank"})
	m.SetCredStore(store)

	if got := m.credKey("llm", "plain-key"); got != "plain-key" {
		t.Fatalf("llm 应回退 config，got %q", got)
	}
	if got := m.credKey("embed", "plain-embed"); got != "cred-embed" {
		t.Fatalf("embed 应返回 credstore 值，got %q", got)
	}
	if got := m.credKey("rerank", "plain-rerank"); got != "plain-rerank" {
		t.Fatalf("rerank 应回退 config，got %q", got)
	}
}

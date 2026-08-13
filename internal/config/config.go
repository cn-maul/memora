// Package config 配置管理模块
// 自有数据：config.json（唯一写者）
// 规则：snake_case 落盘，内存操作后先改内存再落盘
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"memora/internal/credstore"
)

// Config 内存配置结构（snake_case 标签对应 JSON 键名）
type Config struct {
	SchemaVersion int `json:"schema_version"`

	WorkspacePath string `json:"workspace_path"`

	Markitdown struct {
		PythonPath string `json:"python_path"`
		Command    string `json:"command"`
		// MarkitdownCmd 直接指定 markitdown 可执行路径（优先级高于 pythonPath）
		MarkitdownCmd string `json:"markitdown_cmd,omitempty"`
	} `json:"markitdown"`

	LLM struct {
		BaseURL     string  `json:"base_url"`
		APIKey      string  `json:"api_key"`
		Model       string  `json:"model"`
		Temperature float64 `json:"temperature"`
	} `json:"llm"`

	Embed struct {
		BaseURL    string `json:"base_url"`
		APIKey     string `json:"api_key"`
		Model      string `json:"model"`
		Dimensions int    `json:"dimensions"`
	} `json:"embed"`

	Git struct {
		AuthorName  string `json:"author_name"`
		AuthorEmail string `json:"author_email"`
	} `json:"git"`

	AutoCommit struct {
		Enabled     bool `json:"enabled"`
		DebounceSec int  `json:"debounce_sec"`
	} `json:"auto_commit"`

	Index struct {
		ChunkSize       int `json:"chunk_size"`
		ChunkOverlap    int `json:"chunk_overlap"`
		ScanIntervalSec int `json:"scan_interval_sec"`
	} `json:"index"`

	Recent struct {
		WindowHours int `json:"window_hours"`
	} `json:"recent"`

	Rerank struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Model   string `json:"model"`
	} `json:"rerank"`

	QA struct {
		MaxContextChars int    `json:"max_context_chars"`
		SystemPrompt    string `json:"system_prompt"`
	} `json:"qa"`

	Stats struct {
		Enabled bool `json:"enabled"`
	} `json:"stats"`

	Tray struct {
		Enabled bool `json:"enabled"`
	} `json:"tray"`
}

// Module config 模块
type Module struct {
	mu     sync.RWMutex
	cfg    *Config
	path   string // config.json 绝对路径
	events EventBus
	// credStore 凭据存储（DPAPI/兜底）。注入后 UpsertSecrets 将密钥写入凭据存储，
	// config.json 不再落明文（修复：此前只写 config 明文，凭据存储有旧值时新密钥被忽略）。
	credStore credstore.Store
}

// EventBus config 模块使用的事件接口（避免 import events 包）
type EventBus interface {
	Notify(topic string, data interface{})
}

// New 创建并加载配置
func New(path string, events EventBus) (*Module, error) {
	m := &Module{
		cfg:    defaultConfig(),
		path:   path,
		events: events,
	}
	if err := m.load(); err != nil {
		return nil, fmt.Errorf("[config] 加载配置失败: %w", err)
	}
	return m, nil
}

// latestSchemaVersion 当前支持的配置 schema 版本
const latestSchemaVersion = 1

// defaultConfig 返回默认配置
func defaultConfig() *Config {
	c := &Config{}
	c.SchemaVersion = 1
	c.Markitdown.Command = `python -m markitdown "{file}"`
	c.LLM.Temperature = 0.2
	c.Embed.Dimensions = 1024
	c.Git.AuthorName = "Memora"
	c.Git.AuthorEmail = "memora@local"
	c.AutoCommit.Enabled = true
	c.AutoCommit.DebounceSec = 90
	c.Index.ChunkSize = 2000
	c.Index.ChunkOverlap = 256
	c.Index.ScanIntervalSec = 60                   // 低频 reconcile 兜底间隔（P2-16）：实时变更由 watcher 驱动，全盘扫描仅作低频 reconcile
	c.Recent.WindowHours = 24                      // 最近文件默认展示"最近 24 小时"内修改的文件
	c.Rerank.Model = "Pro/BAAI/bge-reranker-v2-m3" // 重排模型默认值（SiliconFlow）
	c.QA.MaxContextChars = 8000
	c.Stats.Enabled = true
	c.Tray.Enabled = true
	return c
}

// load 从文件加载配置
func (m *Module) load() error {
	if err := m.loadFrom(m.path); err != nil {
		return err
	}
	// 引导指针定位：若入口配置仅含 workspace_path（由 Relocate 写入的引导指针），
	// 则切换到工作区的 .memora/config.json 继续加载完整配置（修复 B-02）。
	// 仅当入口配置本身可解析、含非空 workspace_path，且工作区配置存在时才切换。
	if m.cfg.WorkspacePath != "" {
		wsCfg := filepath.Join(m.cfg.WorkspacePath, ".memora", "config.json")
		if wsCfg != m.path {
			if _, err := os.Stat(wsCfg); err == nil {
				// 切换路径并加载完整工作区配置
				m.path = wsCfg
				return m.loadFrom(wsCfg)
			}
		}
	}
	return nil
}

// loadFrom 从指定路径加载配置（不存在则用默认值并创建）
func (m *Module) loadFrom(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件不存在则用默认值，先创建目录
			dir := filepath.Dir(path)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("[config] 创建配置目录失败: %w", err)
			}
			return m.saveTo(path)
		}
		return err
	}
	if err := json.Unmarshal(data, m.cfg); err != nil {
		return fmt.Errorf("[config] 解析配置失败: %w", err)
	}
	// P0-06：解析成功后按 schema_version 增量迁移（仅当版本需要升级时触发并落盘一次，
	// 避免每次启动重复写盘；Migrate 内部 save 不会回调 loadFrom，无死循环风险）。
	if m.cfg.SchemaVersion < latestSchemaVersion {
		if err := m.Migrate(); err != nil {
			return fmt.Errorf("[config] 迁移配置失败: %w", err)
		}
	}
	return nil
}

// save 将当前配置写入文件
func (m *Module) save() error {
	return m.saveTo(m.path)
}

// saveTo 将当前配置原子写入指定路径。
// 原子写：先序列化到同目录临时文件 config.json.tmp-<rand>（0600），再 os.Rename 原子替换，
// 避免直接覆盖写导致的截断/断电丢失配置（P0-06）。
func (m *Module) saveTo(path string) error {
	data, err := json.MarshalIndent(m.cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("[config] 序列化配置失败: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("[config] 创建配置目录失败: %w", err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("[config] 写入配置失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 失败时清理临时文件；成功 rename 后此处删除已无文件

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("[config] 写入配置失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("[config] 写入配置失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("[config] 写入配置失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("[config] 写入配置失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("[config] 写入配置失败: %w", err)
	}
	return nil
}

// Relocate 将配置文件迁移到工作区的 .memora/config.json
// 在 init 设置 workspace.path 后调用，确保 config.json 与 meta 库同目录（D13）。
// 迁移后会在原入口路径写一份"引导指针"配置（仅含 workspace_path），
// 以便下次启动时能据此定位并加载工作区配置（修复 B-02）。
func (m *Module) Relocate(workspace string) error {
	if workspace == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	oldPath := m.path
	targetDir := filepath.Join(workspace, ".memora")
	targetPath := filepath.Join(targetDir, "config.json")

	// 目标路径与当前相同则无需迁移
	if targetPath == oldPath {
		return nil
	}

	// 如果目标 config 已存在（此前已初始化），仅切换并保留现有内容
	if _, err := os.Stat(targetPath); err == nil {
		// 让当前内存配置与磁盘目标保持一致（保留内存中的 workspace_path 等已设项）
		m.cfg.WorkspacePath = workspace
		m.path = targetPath
		if err := m.save(); err != nil {
			return err
		}
	} else {
		// 创建目标目录
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			return fmt.Errorf("[config] 创建工作区 .memora 目录失败: %w", err)
		}

		// 迁移：从当前路径（或默认配置）写入目标
		m.cfg.WorkspacePath = workspace
		if err := m.saveTo(targetPath); err != nil {
			return fmt.Errorf("[config] 迁移配置文件失败: %w", err)
		}

		// 写入完成后切换当前路径
		m.path = targetPath
	}

	// 在原入口路径写引导指针配置（仅含 workspace_path，用于下次启动定位）。
	// 若原入口路径与目标路径同目录则跳过，避免覆盖工作区配置。
	if filepath.Dir(oldPath) != filepath.Dir(targetPath) {
		ptr := &Config{SchemaVersion: 1, WorkspacePath: workspace}
		ptrData, err := json.MarshalIndent(ptr, "", "  ")
		if err != nil {
			return fmt.Errorf("[config] 序列化引导指针失败: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(oldPath), 0755); err != nil {
			return fmt.Errorf("[config] 创建引导配置目录失败: %w", err)
		}
		if err := os.WriteFile(oldPath, ptrData, 0600); err != nil {
			return fmt.Errorf("[config] 写入引导指针失败: %w", err)
		}
	}

	return nil
}

// Get 获取配置项（点分键）
func (m *Module) Get(key string) (interface{}, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.getByPath(key)
}

// getByPath 按点分键路径查找
func (m *Module) getByPath(key string) (interface{}, error) {
	switch key {
	case "workspace.path":
		return m.cfg.WorkspacePath, nil
	case "markitdown.command":
		return m.cfg.Markitdown.Command, nil
	case "markitdown.pythonPath":
		return m.cfg.Markitdown.PythonPath, nil
	case "llm.baseUrl":
		return m.cfg.LLM.BaseURL, nil
	case "llm.apiKey":
		return nil, fmt.Errorf("[config] 密钥不通过 Get 读取，使用 UpsertSecrets")
	case "llm.model":
		return m.cfg.LLM.Model, nil
	case "llm.temperature":
		return m.cfg.LLM.Temperature, nil
	case "embed.baseUrl":
		return m.cfg.Embed.BaseURL, nil
	case "embed.apiKey":
		return nil, fmt.Errorf("[config] 密钥不通过 Get 读取，使用 UpsertSecrets")
	case "embed.model":
		return m.cfg.Embed.Model, nil
	case "embed.dimensions":
		return m.cfg.Embed.Dimensions, nil
	case "git.authorName":
		return m.cfg.Git.AuthorName, nil
	case "git.authorEmail":
		return m.cfg.Git.AuthorEmail, nil
	case "autoCommit.enabled":
		return m.cfg.AutoCommit.Enabled, nil
	case "autoCommit.debounceSec":
		return m.cfg.AutoCommit.DebounceSec, nil
	case "index.chunkSize":
		return m.cfg.Index.ChunkSize, nil
	case "index.chunkOverlap":
		return m.cfg.Index.ChunkOverlap, nil
	case "index.scanIntervalSec":
		return m.cfg.Index.ScanIntervalSec, nil
	case "recent.windowHours":
		return m.cfg.Recent.WindowHours, nil
	case "rerank.baseUrl":
		return m.cfg.Rerank.BaseURL, nil
	case "rerank.apiKey":
		return nil, fmt.Errorf("[config] 密钥不通过 Get 读取，使用 UpsertSecrets")
	case "rerank.model":
		return m.cfg.Rerank.Model, nil
	case "qa.maxContextChars":
		return m.cfg.QA.MaxContextChars, nil
	case "qa.systemPrompt":
		return m.cfg.QA.SystemPrompt, nil
	case "stats.enabled":
		return m.cfg.Stats.Enabled, nil
	case "tray.enabled":
		return m.cfg.Tray.Enabled, nil
	default:
		return nil, fmt.Errorf("[config] 未知配置键: %s", key)
	}
}

// Set 设置配置项并落盘（广播 settings_changed）
func (m *Module) Set(key string, value interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	oldVal, _ := m.getByPath(key)
	if oldVal == value {
		return nil // 无变化
	}

	if err := m.setByPath(key, value); err != nil {
		return err
	}

	if err := m.save(); err != nil {
		// 落盘失败回滚
		m.setByPath(key, oldVal)
		return fmt.Errorf("[config] 落盘失败已回滚: %w", err)
	}

	if m.events != nil {
		m.events.Notify("settings_changed", map[string]interface{}{"key": key})
	}
	return nil
}

// setByPath 按点分键路径设置
func (m *Module) setByPath(key string, value interface{}) error {
	switch key {
	case "workspace.path":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("[config] workspace.path 需要 string 类型")
		}
		m.cfg.WorkspacePath = v
	case "markitdown.command":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("[config] markitdown.command 需要 string 类型")
		}
		m.cfg.Markitdown.Command = v
	case "markitdown.markitdownCmd":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("[config] markitdown.markitdownCmd 需要 string 类型")
		}
		m.cfg.Markitdown.MarkitdownCmd = v
	case "markitdown.pythonPath":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("[config] markitdown.pythonPath 需要 string 类型")
		}
		m.cfg.Markitdown.PythonPath = v
	case "llm.baseUrl":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("[config] llm.baseUrl 需要 string 类型")
		}
		m.cfg.LLM.BaseURL = v
	case "llm.model":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("[config] llm.model 需要 string 类型")
		}
		m.cfg.LLM.Model = v
	case "llm.temperature":
		v, ok := value.(float64)
		if !ok {
			return fmt.Errorf("[config] llm.temperature 需要 float64 类型")
		}
		m.cfg.LLM.Temperature = v
	case "embed.baseUrl":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("[config] embed.baseUrl 需要 string 类型")
		}
		m.cfg.Embed.BaseURL = v
	case "embed.model":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("[config] embed.model 需要 string 类型")
		}
		m.cfg.Embed.Model = v
	case "embed.dimensions":
		vFloat, ok := value.(float64)
		if !ok {
			return fmt.Errorf("[config] embed.dimensions 需要数字类型")
		}
		m.cfg.Embed.Dimensions = int(vFloat)
	case "git.authorName":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("[config] git.authorName 需要 string 类型")
		}
		m.cfg.Git.AuthorName = v
	case "git.authorEmail":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("[config] git.authorEmail 需要 string 类型")
		}
		m.cfg.Git.AuthorEmail = v
	case "autoCommit.enabled":
		v, ok := value.(bool)
		if !ok {
			return fmt.Errorf("[config] autoCommit.enabled 需要 bool 类型")
		}
		m.cfg.AutoCommit.Enabled = v
	case "autoCommit.debounceSec":
		// 兼容 int（Go 直接调用）与 float64（HTTP/JSON）两种数字类型
		switch v := value.(type) {
		case int:
			m.cfg.AutoCommit.DebounceSec = v
		case float64:
			m.cfg.AutoCommit.DebounceSec = int(v)
		default:
			return fmt.Errorf("[config] autoCommit.debounceSec 需要数字类型")
		}
	case "index.chunkSize":
		vFloat, ok := value.(float64)
		if !ok {
			return fmt.Errorf("[config] index.chunkSize 需要数字类型")
		}
		m.cfg.Index.ChunkSize = int(vFloat)
	case "index.chunkOverlap":
		vFloat, ok := value.(float64)
		if !ok {
			return fmt.Errorf("[config] index.chunkOverlap 需要数字类型")
		}
		m.cfg.Index.ChunkOverlap = int(vFloat)
	case "index.scanIntervalSec":
		vFloat, ok := value.(float64)
		if !ok {
			return fmt.Errorf("[config] index.scanIntervalSec 需要数字类型")
		}
		m.cfg.Index.ScanIntervalSec = int(vFloat)
	case "recent.windowHours":
		vFloat, ok := value.(float64)
		if !ok {
			return fmt.Errorf("[config] recent.windowHours 需要数字类型")
		}
		m.cfg.Recent.WindowHours = int(vFloat)
	case "rerank.baseUrl":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("[config] rerank.baseUrl 需要 string 类型")
		}
		m.cfg.Rerank.BaseURL = v
	case "rerank.model":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("[config] rerank.model 需要 string 类型")
		}
		m.cfg.Rerank.Model = v
	case "qa.maxContextChars":
		vFloat, ok := value.(float64)
		if !ok {
			return fmt.Errorf("[config] qa.maxContextChars 需要数字类型")
		}
		m.cfg.QA.MaxContextChars = int(vFloat)
	case "qa.systemPrompt":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("[config] qa.systemPrompt 需要 string 类型")
		}
		m.cfg.QA.SystemPrompt = v
	case "stats.enabled":
		v, ok := value.(bool)
		if !ok {
			return fmt.Errorf("[config] stats.enabled 需要 bool 类型")
		}
		m.cfg.Stats.Enabled = v
	case "tray.enabled":
		v, ok := value.(bool)
		if !ok {
			return fmt.Errorf("[config] tray.enabled 需要 bool 类型")
		}
		m.cfg.Tray.Enabled = v
	default:
		return fmt.Errorf("[config] 未知配置键: %s", key)
	}
	return nil
}

// Snapshot 返回配置快照（不含 apiKey）
func (m *Module) Snapshot() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return map[string]interface{}{
		"schemaVersion": m.cfg.SchemaVersion,
		"workspacePath": m.cfg.WorkspacePath,
		"markitdown": map[string]interface{}{
			"pythonPath":    m.cfg.Markitdown.PythonPath,
			"command":       m.cfg.Markitdown.Command,
			"markitdownCmd": m.cfg.Markitdown.MarkitdownCmd,
		},
		"llm": map[string]interface{}{
			"baseUrl":     m.cfg.LLM.BaseURL,
			"model":       m.cfg.LLM.Model,
			"temperature": m.cfg.LLM.Temperature,
		},
		"embed": map[string]interface{}{
			"baseUrl":    m.cfg.Embed.BaseURL,
			"model":      m.cfg.Embed.Model,
			"dimensions": m.cfg.Embed.Dimensions,
		},
		"git": map[string]interface{}{
			"authorName":  m.cfg.Git.AuthorName,
			"authorEmail": m.cfg.Git.AuthorEmail,
		},
		"autoCommit": map[string]interface{}{
			"enabled":     m.cfg.AutoCommit.Enabled,
			"debounceSec": m.cfg.AutoCommit.DebounceSec,
		},
		"index": map[string]interface{}{
			"chunkSize":       m.cfg.Index.ChunkSize,
			"chunkOverlap":    m.cfg.Index.ChunkOverlap,
			"scanIntervalSec": m.cfg.Index.ScanIntervalSec,
		},
		"recent": map[string]interface{}{
			"windowHours": m.cfg.Recent.WindowHours,
		},
		"rerank": map[string]interface{}{
			"baseUrl": m.cfg.Rerank.BaseURL,
			"model":   m.cfg.Rerank.Model,
		},
		"qa": map[string]interface{}{
			"maxContextChars": m.cfg.QA.MaxContextChars,
			"systemPrompt":    m.cfg.QA.SystemPrompt,
		},
		"stats": map[string]interface{}{
			"enabled": m.cfg.Stats.Enabled,
		},
		"tray": map[string]interface{}{
			"enabled": m.cfg.Tray.Enabled,
		},
	}
}

// UpsertSecrets 更新密钥（不回显）
// 凭据存储已注入时写入凭据存储并清空 config 明文；未注入时回退 config 明文（保持可用）。
func (m *Module) UpsertSecrets(llmKey, embedKey, rerankKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.credStore != nil {
		// 凭据存储优先：任一写入失败即返回错误（不回退明文，避免出现"config 有明文、credstore 有旧值"
		// 导致新密钥不生效的静默不一致）。
		if llmKey != "" {
			if err := m.credStore.Set("llm", "api_key", llmKey); err != nil {
				return fmt.Errorf("[config] 保存 LLM 密钥失败: %w", err)
			}
			m.cfg.LLM.APIKey = ""
		}
		if embedKey != "" {
			if err := m.credStore.Set("embed", "api_key", embedKey); err != nil {
				return fmt.Errorf("[config] 保存嵌入密钥失败: %w", err)
			}
			m.cfg.Embed.APIKey = ""
		}
		if rerankKey != "" {
			if err := m.credStore.Set("rerank", "api_key", rerankKey); err != nil {
				return fmt.Errorf("[config] 保存重排密钥失败: %w", err)
			}
			m.cfg.Rerank.APIKey = ""
		}
		// 标记迁移完成：凭据已入凭据库，启动时无需再迁移明文（即使失败也已在凭据库中）
		_ = m.credStore.MarkPlaintextMigrated()
	} else {
		if llmKey != "" {
			m.cfg.LLM.APIKey = llmKey
		}
		if embedKey != "" {
			m.cfg.Embed.APIKey = embedKey
		}
		if rerankKey != "" {
			m.cfg.Rerank.APIKey = rerankKey
		}
	}
	return m.save()
}

// SetCredStore 注入凭据存储（装配层调用）。
func (m *Module) SetCredStore(store credstore.Store) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.credStore = store
}

// MigrateSecretsToCredStore 将 config 中的明文 llm/embed/rerank api_key 迁移到凭据存储，
// 迁移成功后清空内存与磁盘明文并落盘；任一失败返回错误且不清空原明文（保留原可用状态）。
// 仅迁移非空明文 key。本阶段仅提供实现；llm.go 等读取方改接凭据存储由后续任务（Wave 2）完成。
func (m *Module) MigrateSecretsToCredStore(store credstore.Store) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	type secret struct {
		service string
		key     string
		value   string
	}
	var pending []secret
	if m.cfg.LLM.APIKey != "" {
		pending = append(pending, secret{"llm", "api_key", m.cfg.LLM.APIKey})
	}
	if m.cfg.Embed.APIKey != "" {
		pending = append(pending, secret{"embed", "api_key", m.cfg.Embed.APIKey})
	}
	if m.cfg.Rerank.APIKey != "" {
		pending = append(pending, secret{"rerank", "api_key", m.cfg.Rerank.APIKey})
	}

	// 先全部写入凭据存储：任一失败即中止，原明文保持不变
	for _, s := range pending {
		if err := store.Set(s.service, s.key, s.value); err != nil {
			return fmt.Errorf("[config] 迁移密钥到凭据存储失败 (%s/%s): %w", s.service, s.key, err)
		}
	}

	// 全部成功后才清空内存明文并落盘
	oldLLM, oldEmbed, oldRerank := m.cfg.LLM.APIKey, m.cfg.Embed.APIKey, m.cfg.Rerank.APIKey
	m.cfg.LLM.APIKey = ""
	m.cfg.Embed.APIKey = ""
	m.cfg.Rerank.APIKey = ""
	if err := m.save(); err != nil {
		// 落盘失败：回滚内存明文，保持原可用状态
		m.cfg.LLM.APIKey = oldLLM
		m.cfg.Embed.APIKey = oldEmbed
		m.cfg.Rerank.APIKey = oldRerank
		return fmt.Errorf("[config] 迁移密钥后落盘失败，已回滚明文: %w", err)
	}

	// 标记迁移完成（供启动流程跳过重复迁移）；该步骤失败不回滚——密钥已安全存入凭据存储
	if err := store.MarkPlaintextMigrated(); err != nil {
		return fmt.Errorf("[config] 标记凭据迁移完成失败: %w", err)
	}
	return nil
}

// Workspace 返回工作目录
func (m *Module) Workspace() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.WorkspacePath
}

// Migrate 按 schema_version 增量迁移配置到最新版本。
// 仅在版本低于 latestSchemaVersion 时执行迁移并落盘一次；版本已最新则直接返回，
// 避免每次加载都重写配置文件。
func (m *Module) Migrate() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.migrateLocked()
}

// migrateLocked 在持有锁的情况下执行增量迁移，版本提升成功后落盘。
func (m *Module) migrateLocked() error {
	if m.cfg.SchemaVersion >= latestSchemaVersion {
		return nil
	}
	// 当前最新版本为 v1，暂无字段级迁移；后续 schema_version 升级在此追加步骤。
	m.cfg.SchemaVersion = latestSchemaVersion
	if err := m.save(); err != nil {
		return fmt.Errorf("[config] 迁移后落盘失败: %w", err)
	}
	return nil
}

// GetLLMConfig 获取 LLM 配置（供 llm 模块使用）
func (m *Module) GetLLMConfig() (baseURL, apiKey, model string, temperature float64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.LLM.BaseURL, m.cfg.LLM.APIKey, m.cfg.LLM.Model, m.cfg.LLM.Temperature
}

// GetEmbedConfig 获取 Embed 配置（供 llm 模块使用）
func (m *Module) GetEmbedConfig() (baseURL, apiKey, model string, dimensions int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Embed.BaseURL, m.cfg.Embed.APIKey, m.cfg.Embed.Model, m.cfg.Embed.Dimensions
}

// GetRerankConfig 获取重排配置（供 llm 模块使用）
func (m *Module) GetRerankConfig() (baseURL, apiKey, model string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Rerank.BaseURL, m.cfg.Rerank.APIKey, m.cfg.Rerank.Model
}

// GetAutoCommitConfig 获取自动提交配置
func (m *Module) GetAutoCommitConfig() (enabled bool, debounceSec int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.AutoCommit.Enabled, m.cfg.AutoCommit.DebounceSec
}

// GetGitAuthor 获取 Git 提交作者信息
func (m *Module) GetGitAuthor() (name, email string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Git.AuthorName, m.cfg.Git.AuthorEmail
}

// GetMarkitdown 获取 MarkItDown 三项配置
func (m *Module) GetMarkitdown() (pythonPath, command, markitdownCmd string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Markitdown.PythonPath, m.cfg.Markitdown.Command, m.cfg.Markitdown.MarkitdownCmd
}

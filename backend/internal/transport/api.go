package transport

import (
	"memora/internal/browser"
	"memora/internal/contract"
)

// StorageAPI 存储模块接口
type StorageAPI interface {
	FilesUpsert(f *contract.FileInfo) (int64, error)
	FilesFindByRelPath(relPath string) (*contract.FileInfo, error)
	FilesGet(id int64) (*contract.FileInfo, error)
	FilesList(status, tag string, page, pageSize int, sortOrder string) ([]*contract.FileInfo, int, error)
	FilesMarkStatus(id int64, status, lastError string) error
	FilesRetryStatus(id int64) error // 将 failed 重置为 pending
	ChunksByFile(fileID int64) ([]*contract.Chunk, error)
	TagsList() ([]*contract.TagInfo, error)
	FileTagsListByFile(fileID int64) ([]contract.FileTag, error)
	FileTagsByFiles(fileIDs []int64) (map[int64][]contract.FileTag, error)
	SuggestionsListPending() ([]*contract.TagSuggestion, error)
	SuggestionsSetStatus(id int64, status string) error
	QASessionsList() ([]*contract.QASession, error)
	QASessionsDelete(id int64) error
	QAMessagesBySession(sessionID int64) ([]*contract.QAMessage, error)
	FilesRecent(sinceMs int64, limit int) ([]*contract.FileInfo, error)
	// VectorCount 查询存量向量总数（用于判断维度变更后是否需要自动重建索引）
	VectorCount() (int, error)
	// Ping 探测数据库可用性（/ready、/diagnostics 使用）。
	Ping() error
}

// GitAPI git 模块接口
type GitAPI interface {
	EnsureRepo(path string) error
	Status() (map[string]string, error)
	CommitAuto(files []string) (string, bool, error)
	CommitManual(message string) (string, error)
	Log() ([]*contract.CommitInfo, error)
	DiffStats(hash string) (*contract.DiffStat, error)
	FileHistory(relPath string) ([]*contract.CommitInfo, error)
	ShowFileAt(relPath, hash string) (string, error)
	RestoreFile(relPath, hash string) error
	Head() (*contract.HeadInfo, error)
	CommitFiles(hash string) ([]*contract.CommitFile, error)
	ListTreeAt(hash string) ([]*contract.VersionFile, error)
}

// WatchAPI watch 模块接口
type WatchAPI interface {
	Pause() error
	Resume() error
}

// ExtractAPI extract 模块接口
type ExtractAPI interface {
	Probe(pythonPath, command string) (bool, string)
	ApplyConfig(pythonPath, command, markitdownCmd string)
}

// IndexAPI index 模块接口
type IndexAPI interface {
	FullReindex() error
	// SetEmbedDim 运行期刷新嵌入维度（响应 embed.dimensions 配置变更）。
	// 调用方应随后触发 FullReindex 让存量文件按新维度重嵌入。
	SetEmbedDim(dim int64)
}

// TagAPI tag 模块接口
type TagAPI interface {
	ListLibrary() ([]*contract.TagInfo, error)
	ManualOverride(fileID int64, add, remove []string) error
	AcceptSuggestion(id int64) error
	RejectSuggestion(id int64) error
}

// SearchAPI search 模块接口
type SearchAPI interface {
	Query(q string, tagFilter []string, page int) ([]*contract.SearchResult, int, error)
}

// TimelineAPI timeline 模块接口
type TimelineAPI interface {
	GenerateSummary(commitHash string) (string, error)
	SuggestCommitMessage() (string, error) // AI 根据未提交变动生成提交备注
	Restore(relPath, hash string) error
}

// QAAPI qa 模块接口
type QAAPI interface {
	Ask(req *contract.QARequest) (*contract.QAResponse, error)
	AskStream(req *contract.QARequest, cancel <-chan struct{}) (<-chan string, <-chan *contract.QAResponse)
	Sessions() ([]*contract.QASession, error)
	DeleteSession(id int64) error
}

// StatsAPI stats 模块接口
type StatsAPI interface {
	Enabled() bool
	SetEnabled(v bool) error
	Summary(r *contract.StatsRange) (*contract.StatsMetrics, error)
	Export(format string, r *contract.StatsRange) (string, error)
	Purge() error
}

// ConfigAPI config 模块接口
type ConfigAPI interface {
	Get(key string) (interface{}, error)
	Set(key string, value interface{}) error
	Snapshot() map[string]interface{}
	UpsertSecrets(llmKey, embedKey, rerankKey string) error
	Relocate(workspace string) error
}

// LLMAPI llm 模块接口
type LLMAPI interface {
	TestChat() error
	TestEmbed() error
	TestChatWith(baseURL, apiKey, model string, temperature float64) error
	TestEmbedWith(baseURL, apiKey, model string) error
	TestRerankWith(baseURL, apiKey, model string) error
	ListModels(kind, baseURL, apiKey string) ([]string, error)
}

// BrowserAPI 文件浏览模块接口
type BrowserAPI interface {
	ListDir(workspace, subPath string) ([]*browser.DirEntry, error)
	SearchByName(workspace, query string, limit int) ([]*browser.SearchResult, int, error)
	PickDirectory(initial string) (string, error)
	OpenFile(workspace, relPath string) error
}

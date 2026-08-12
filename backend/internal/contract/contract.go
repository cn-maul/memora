// Package contract 定义所有模块的接口契约。
// 模块间只认接口不认实现，实现位于 internal/<模块名>/ 下。
package contract

// ──────────────────────────── 基础类型 ────────────────────────────

// FileInfo 文件元数据
type FileInfo struct {
	ID            int64  `json:"id"`
	RelPath       string `json:"relPath"`
	Size          int64  `json:"size"`
	Mtime         int64  `json:"mtime"` // 毫秒
	ContentHash   string `json:"contentHash,omitempty"`
	DocType       string `json:"docType"` // pdf/docx/pptx/xlsx/txt/md/doc(ignored)
	IndexStatus   string `json:"indexStatus"`
	LastError     string `json:"lastError,omitempty"`
	FirstSeenAt   int64  `json:"firstSeenAt"`
	LastIndexedAt int64  `json:"lastIndexedAt,omitempty"`
}

// Chunk 文本分块
type Chunk struct {
	ID       int64  `json:"id"`
	FileID   int64  `json:"fileId"`
	Seq      int    `json:"seq"`
	TokenEst int    `json:"tokenEst"`
	Text     string `json:"text"`
}

// TagInfo 标签
type TagInfo struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Source    string `json:"source"` // predefined|auto_generated|user_confirmed
	Count     int    `json:"count,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

// FileTag 文件-标签关联
type FileTag struct {
	Name   string `json:"name"`
	Origin string `json:"origin"` // auto|manual
}

// TagSuggestion 标签建议
type TagSuggestion struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Reason          string `json:"reason,omitempty"`
	SuggestedByFile int64  `json:"fileId"`
	RelPath         string `json:"relPath,omitempty"`
	Status          string `json:"status"` // pending|accepted|rejected
	CreatedAt       int64  `json:"createdAt"`
}

// CommitSummary 提交摘要
type CommitSummary struct {
	CommitHash  string `json:"commitHash"`
	Summary     string `json:"summary"`
	GeneratedAt int64  `json:"generatedAt"`
}

// CommitInfo 提交信息
type CommitInfo struct {
	Hash    string `json:"hash"`
	Time    int64  `json:"time"`
	Message string `json:"message"`
	Author  string `json:"author"`
}

// CommitFile 单个提交内改动的文件
type CommitFile struct {
	Path   string `json:"path"`
	Status string `json:"status"` // added|modified|deleted
}

// VersionFile 某提交快照中的文件条目（用于"查看该提交的全部文件"）
type VersionFile struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	DocType string `json:"docType,omitempty"` // pdf/docx/pptx/xlsx/txt/md/doc(ignored)；空表示不支持
}

// HeadInfo 当前版本（HEAD）概要
type HeadInfo struct {
	Hash         string `json:"hash"`
	Branch       string `json:"branch,omitempty"` // 当前分支名（无提交时为 master）
	CountFiles   int    `json:"countFiles"`       // 当前提交树上文件总数
	ChangedFiles int    `json:"changedFiles"`     // 相对上一提交改动的文件数
	HasCommits   bool   `json:"hasCommits"`
}

// QASession 问答会话
type QASession struct {
	ID           int64  `json:"id"`
	CreatedAt    int64  `json:"createdAt"`
	Mode         string `json:"mode"` // file|global
	FileID       int64  `json:"fileId,omitempty"`
	MessageCount int    `json:"messageCount"`
}

// QAMessage 问答消息
type QAMessage struct {
	ID        int64  `json:"id"`
	SessionID int64  `json:"sessionId"`
	Role      string `json:"role"` // user|assistant
	Content   string `json:"content"`
	Sources   string `json:"sources,omitempty"` // JSON
	CreatedAt int64  `json:"createdAt"`
}

// SearchResult 搜索结果项
type SearchResult struct {
	FileID        int64     `json:"fileId"`
	RelPath       string    `json:"relPath"`
	HitText       string    `json:"hitText"`
	Score         float64   `json:"score"`
	Tags          []FileTag `json:"tags"`
	Mtime         int64     `json:"mtime"`
	MatchedChunks int       `json:"matchedChunks"`
}

// StatsMetrics 统计指标
type StatsMetrics struct {
	CommitsByDay    []DayCount  `json:"commitsByDay"`
	FileChanges     FileChanges `json:"fileChanges"`
	HotFiles        []HotFile   `json:"hotFiles"`
	HourBuckets     HourBuckets `json:"hourBuckets"`
	TagDistribution []TagCount  `json:"tagDistribution"`
	IterationRate   float64     `json:"iterationRate"`
}

type DayCount struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

type FileChanges struct {
	Added    int `json:"added"`
	Modified int `json:"modified"`
	Deleted  int `json:"deleted"`
}

type HotFile struct {
	RelPath string `json:"relPath"`
	Count   int    `json:"count"`
}

type HourBuckets struct {
	Morning   int `json:"morning"`
	Afternoon int `json:"afternoon"`
	Evening   int `json:"evening"`
	Night     int `json:"night"`
}

type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// Paginated 分页外层
type Paginated struct {
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
	Total    int `json:"total"`
}

// ──────────────────────────── 4.1 IConfig ────────────────────────────

// IConfig 配置管理
type IConfig interface {
	Get(key string) (interface{}, error)
	Set(key string, value interface{}) error
	Snapshot() map[string]interface{} // 不含 apiKey
	UpsertSecrets(llmKey, embedKey, rerankKey string) error
	Workspace() string
	Migrate() error
	Relocate(workspace string) error
	GetMarkitdown() (pythonPath, command, markitdownCmd string)
}

// ──────────────────────────── 4.2 IEvents ────────────────────────────

// IEvents 极简发布/订阅
type IEvents interface {
	Notify(topic string, data interface{})
	Subscribe(topic string, handler func(data interface{})) func()
}

// ──────────────────────────── 4.3 IStorage ────────────────────────────

// IStorage 存储接口
type IStorage interface {
	// 文件
	FilesUpsert(f *FileInfo) (int64, error)
	FilesFindByRelPath(relPath string) (*FileInfo, error)
	FilesGet(id int64) (*FileInfo, error)
	FilesList(status, tag string, page, pageSize int, sortOrder string) ([]*FileInfo, int, error)
	FilesMarkStatus(id int64, status, lastError string) error
	FilesRetryStatus(id int64) error                           // 将 failed 重置为 pending，供用户手动重试
	FilesRecent(sinceMs int64, limit int) ([]*FileInfo, error) // 最近修改的文件（按 mtime 倒序）

	// 分块
	ChunksReplaceForFile(fileID int64, chunks []*Chunk) error
	ChunksByFile(fileID int64) ([]*Chunk, error)
	ChunksGet(id int64) (*Chunk, error)

	// 向量
	VectorsInsert(chunkID int64, vec []float32, dim int) error
	VectorsDelete(chunkID int64) error
	VectorsLoadAll() ([]VectorEntry, error)
	VectorsSearch(queryVec []float32, topK int) ([]VectorEntry, error)

	// 标签
	TagsList() ([]*TagInfo, error)
	TagsGetByName(name string) (*TagInfo, error)
	TagsCreate(name, source string) (int64, error)

	// 文件标签
	FileTagsReplace(fileID int64, tags []FileTag) error
	FileTagsListByFile(fileID int64) ([]FileTag, error)
	FileTagsListByTag(tagID int64) ([]int64, error)

	// 覆盖记录
	OverridesAppend(fileID int64, tagName, action string) error

	// 建议
	SuggestionsAdd(name, reason string, suggestedByFile int64) (int64, error)
	SuggestionsListPending() ([]*TagSuggestion, error)
	SuggestionsSetStatus(id int64, status string) error

	// 摘要
	SummariesUpsert(hash, summary string, genAt int64) error
	SummariesGet(hash string) (*CommitSummary, error)

	// 问答
	QASessionsCreate(mode string, fileID int64) (int64, error)
	QASessionsList() ([]*QASession, error)
	QASessionsDelete(id int64) error
	QAMessagesAppend(sessionID int64, role, content, sources string, createdAt int64) (int64, error)
	QAMessagesBySession(sessionID int64) ([]*QAMessage, error)

	// 恢复
	RecoverPending() error

	// 生命周期
	Close() error
}

// VectorEntry 内存向量索引条目
type VectorEntry struct {
	ChunkID int64     `json:"chunkId"`
	Vec     []float32 `json:"vec"`
	Score   float64   `json:"score,omitempty"`
}

// ──────────────────────────── 4.4 ILLM ────────────────────────────

type ChatOptions struct {
	Temperature float64
	MaxTokens   int
	JSONMode    bool
}

// ThinkChunkPrefix 流式"思考过程"块前缀。
// 推理模型（SenseNova reasoning、DeepSeek-R1 等）的思维链经 delta.reasoning
// 增量返回，与最终答案 delta.content 混在同一流里。带此前缀的块表示思考过程，
// 前端单独渲染（折叠区），后端不计入最终回答（不落库）。
const ThinkChunkPrefix = "\x00MTHINK\x00"

// ILLM 模型网关
type ILLM interface {
	Chat(system, user string, opts *ChatOptions) (string, error)
	ChatJSON(system, user, schemaDesc string, result interface{}) error
	Embed(texts []string) ([][]float32, error)
	TestChat() error
	TestEmbed() error
}

// ──────────────────────────── 4.5 IGit ────────────────────────────

type DiffStat struct {
	Added    int      `json:"added"`
	Modified int      `json:"modified"`
	Deleted  int      `json:"deleted"`
	Files    []string `json:"files,omitempty"`
}

// IGit 版本管理
type IGit interface {
	EnsureRepo(path string) error
	Status() (map[string]string, error)              // relPath → code（??/M/D/A）
	CommitAuto(files []string) (string, bool, error) // hash, skipped, err
	CommitManual(message string) (string, error)
	Log() ([]*CommitInfo, error)
	DiffStats(hash string) (*DiffStat, error)
	FileHistory(relPath string) ([]*CommitInfo, error)
	ShowFileAt(relPath, hash string) (string, error)
	RestoreFile(relPath, hash string) error
	Head() (*HeadInfo, error)                       // 当前版本概要
	CommitFiles(hash string) ([]*CommitFile, error) // 提交改动的文件明细
	ListTreeAt(hash string) ([]*VersionFile, error) // 提交快照的全部文件
}

// ──────────────────────────── 4.6 IWatch ────────────────────────────

type FileChange struct {
	Added    []string `json:"added"`
	Modified []string `json:"modified"`
	Removed  []string `json:"removed"`
}

// IWatch 文件监视
type IWatch interface {
	Start() error
	Stop() error
	Pause() error
	Resume() error
	Changes() <-chan *FileChange
}

// ──────────────────────────── 4.7 Extract DTOs ────────────────────────────

type ProbeResult struct {
	Ok      bool   `json:"ok"`
	Message string `json:"message"`
}

type ExtractResult struct {
	Text     string `json:"text"`
	CacheKey string `json:"cacheKey"`
}

// ───────────────────────────── 4.12 QA DTOs ─────────────────────────────

type QARequest struct {
	SessionID int64  `json:"sessionId,omitempty"`
	Mode      string `json:"mode"` // file|global
	FileID    int64  `json:"fileId,omitempty"`
	Question  string `json:"question"`
}

type QAResponse struct {
	Answer    string     `json:"answer"`
	Sources   []QASource `json:"sources"`
	SessionID int64      `json:"sessionId"`
	Error     string     `json:"error,omitempty"`
}

type QASource struct {
	RelPath string `json:"relPath"`
	Seq     int    `json:"seq"`
}

// ──────────────────────────── 4.13 Stats DTOs ────────────────────────────

type StatsRange struct {
	Range string `json:"range"` // week|month|quarter|custom
	From  int64  `json:"from,omitempty"`
	To    int64  `json:"to,omitempty"`
}

// ──────────────────────────── 4.14 TaskQueue DTOs ────────────────────────────

type Task struct {
	Type    string      `json:"type"` // extract|tag|summarize|reindex|delete_index
	Payload interface{} `json:"payload"`
}

type QueueStatus struct {
	Running int `json:"running"`
	Pending int `json:"pending"`
}

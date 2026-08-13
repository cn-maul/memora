// Package assembler (续) — Wails v3 服务定义
//
// 以下 13 个服务将业务模块方法暴露给 Wails 前端绑定。
// 每个服务方法签名约束：参数 struct/基础类型，返回值可 JSON 序列化，error 直接返回。
package assembler

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"memora/internal/browser"
	"memora/internal/contract"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// ======================================================================
// WorkspaceService 工作区服务
// ======================================================================

// WorkspaceService 工作区（初始化、状态、诊断）
type WorkspaceService struct { App *App }

// WorkspaceInfo 工作区信息（返回给前端）
type WorkspaceInfo struct {
	Workspace string                 `json:"workspace"`
	DataDir   string                 `json:"dataDir"`
	Generation string                `json:"generation"`
	Version   string                 `json:"version"`
	Commit    string                 `json:"commit"`
	BuildTime string                 `json:"buildTime"`
	Config    map[string]interface{} `json:"config"`
	Head      *contract.HeadInfo     `json:"head,omitempty"`
}

func (s *WorkspaceService) Info() (*WorkspaceInfo, error) {
	snapshot := s.App.Config.Snapshot()
	gen := ""
	if rt := s.App.runtime.Current(); rt != nil {
		gen = rt.Generation
	}
	info := &WorkspaceInfo{
		Workspace:  s.App.wsPath,
		DataDir:    "",
		Generation: gen,
		Version:    BuildVersion,
		Commit:     BuildCommit,
		BuildTime:  BuildTime,
		Config:     snapshot,
	}
	if rt := s.App.runtime.Current(); rt != nil {
		info.DataDir = rt.DataDir
	}
	if head, err := s.App.Git.Head(); err == nil {
		info.Head = head
	}
	return info, nil
}

// InitRequest 工作区初始化请求
type InitRequest struct {
	Workspace string `json:"workspace"`
}

func (s *WorkspaceService) Init(req InitRequest) error {
	if req.Workspace == "" {
		return fmt.Errorf("工作区路径不能为空")
	}
	return s.App.RebuildWorkspace(req.Workspace)
}

// ======================================================================
// FilesService 文件服务
// ======================================================================

// FileListRequest 文件列表请求
type FileListRequest struct {
	Status   string `json:"status"`
	Tag      string `json:"tag"`
	Page     int    `json:"page"`
	PageSize int    `json:"pageSize"`
	Sort     string `json:"sort"`
}

// FileListResponse 文件列表响应
type FileListResponse struct {
	Items      []*contract.FileInfo `json:"items"`
	Total      int                  `json:"total"`
	Page       int                  `json:"page"`
}

// RecentFilesResponse 最近文件响应
type RecentFilesResponse struct {
	Items  []*contract.FileInfo `json:"items"`
	Window int64                `json:"window"`
}

type FilesService struct { App *App }

func (s *FilesService) List(req FileListRequest) (*FileListResponse, error) {
	files, total, err := s.App.Storage.FilesList(req.Status, req.Tag, req.Page, req.PageSize, req.Sort)
	if err != nil {
		return nil, err
	}
	return &FileListResponse{Items: files, Total: total, Page: req.Page}, nil
}

func (s *FilesService) Recent(req FileListRequest) (*RecentFilesResponse, error) {
	files, err := s.App.Storage.FilesRecent(int64(req.PageSize), req.PageSize)
	if err != nil {
		return nil, err
	}
	return &RecentFilesResponse{Items: files, Window: int64(req.PageSize)}, nil
}

func (s *FilesService) Get(id int64) (*contract.FileInfo, error) {
	return s.App.Storage.FilesGet(id)
}

// FileHistoryResponse 文件历史版本
type FileHistoryResponse struct {
	Commits []*contract.CommitInfo `json:"commits"`
}

func (s *FilesService) History(id int64) (*FileHistoryResponse, error) {
	info, err := s.App.Storage.FilesGet(id)
	if err != nil {
		return nil, err
	}
	commits, err := s.App.Git.FileHistory(info.RelPath)
	if err != nil {
		return &FileHistoryResponse{Commits: nil}, nil
	}
	return &FileHistoryResponse{Commits: commits}, nil
}

func (s *FilesService) DownloadHistory(relPath, hash string) (string, error) {
	content, err := s.App.Git.ShowFileAt(relPath, hash)
	return content, err
}

func (s *FilesService) Resolve(relPath string) (int64, error) {
	info, err := s.App.Storage.FilesFindByRelPath(relPath)
	if err != nil {
		return 0, err
	}
	return info.ID, nil
}

// RestoreFileRequest 恢复文件请求
type RestoreFileRequest struct {
	ID        int64  `json:"id"`
	CommitHash string `json:"commitHash"`
}

func (s *FilesService) Restore(req RestoreFileRequest) error {
	info, err := s.App.Storage.FilesGet(req.ID)
	if err != nil {
		return err
	}
	return s.App.Git.RestoreFile(info.RelPath, req.CommitHash)
}

func (s *FilesService) CommitFileContent(hash, path string) (string, error) {
	return s.App.Git.ShowFileAt(path, hash)
}

// UpdateTagsRequest 更新文件标签
type UpdateTagsRequest struct {
	ID     int64    `json:"id"`
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

func (s *FilesService) UpdateTags(req UpdateTagsRequest) error {
	return s.App.Tag.ManualOverride(req.ID, req.Add, req.Remove)
}

func (s *FilesService) Retry(id int64) error {
	return s.App.Storage.FilesRetryStatus(id)
}

// ======================================================================
// SearchService 搜索服务
// ======================================================================

// SearchRequest 搜索请求
type SearchRequest struct {
	Q       string   `json:"q"`
	Tag     string   `json:"tag"`
	Page    int      `json:"page"`
	TagFilter []string `json:"tagFilter"`
}

// SearchResponse 搜索响应
type SearchResponse struct {
	Items  []*contract.SearchResult `json:"items"`
	Total  int                      `json:"total"`
	Page   int                      `json:"page"`
}

type SearchService struct { App *App }

func (s *SearchService) Search(req SearchRequest) (*SearchResponse, error) {
	tagFilter := []string{}
	if req.Tag != "" {
		tagFilter = append(tagFilter, req.Tag)
	}
	if len(req.TagFilter) > 0 {
		tagFilter = req.TagFilter
	}
	results, total, err := s.App.Search.Query(req.Q, tagFilter, req.Page)
	if err != nil {
		return nil, err
	}
	return &SearchResponse{Items: results, Total: total, Page: req.Page}, nil
}

// ======================================================================
// IndexService 索引服务
// ======================================================================

type IndexService struct { App *App }

func (s *IndexService) Reindex() error {
	return s.App.TriggerReindex()
}

// ======================================================================
// BrowseService 文件浏览服务
// ======================================================================

// BrowseDirRequest 浏览目录请求
type BrowseDirRequest struct {
	Path string `json:"path"`
}

// BrowseDirResponse 浏览目录响应
type BrowseDirResponse struct {
	Entries []*browser.DirEntry `json:"entries"`
}

// BrowseSearchRequest 文件搜索请求
type BrowseSearchRequest struct {
	Q      string `json:"q"`
	Limit  int    `json:"limit"`
}

// BrowseSearchResponse 文件搜索响应
type BrowseSearchResponse struct {
	Results []*browser.SearchResult `json:"results"`
	Total   int                     `json:"total"`
}

// BrowsePickDirResponse 选择目录响应
type BrowsePickDirResponse struct {
	Path string `json:"path"`
}

// PickDirectoryResult 目录选择结果
type PickDirectoryResult struct {
	Path string `json:"path"`
}

type BrowseService struct { App *App }

func (s *BrowseService) Dir(req BrowseDirRequest) (*BrowseDirResponse, error) {
	entries, err := s.App.Browser.ListDir(s.App.wsPath, req.Path)
	if err != nil {
		return nil, err
	}
	return &BrowseDirResponse{Entries: entries}, nil
}

func (s *BrowseService) SearchByName(req BrowseSearchRequest) (*BrowseSearchResponse, error) {
	results, total, err := s.App.Browser.SearchByName(s.App.wsPath, req.Q, req.Limit)
	if err != nil {
		return nil, err
	}
	return &BrowseSearchResponse{Results: results, Total: total}, nil
}

func (s *BrowseService) OpenFile(relPath string) error {
	return s.App.Browser.OpenFile(s.App.wsPath, relPath)
}

func (s *BrowseService) PickDir(initial string) (*PickDirectoryResult, error) {
	// 使用原生目录选择对话框，超时 3 分钟
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	type pickResult struct {
		path string
		err  error
	}
	ch := make(chan pickResult, 1)
	go func() {
		p, err := s.App.Browser.PickDirectory(initial)
		ch <- pickResult{path: p, err: err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		return &PickDirectoryResult{Path: res.path}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("目录选择超时")
	}
}

// ======================================================================
// TagsService 标签服务
// ======================================================================

// TagsListResponse 标签库响应
type TagsListResponse struct {
	Tags []*contract.TagInfo `json:"tags"`
}

// TagSuggestionsResponse 标签建议响应
type TagSuggestionsResponse struct {
	Suggestions []*contract.TagSuggestion `json:"suggestions"`
}

type TagsService struct { App *App }

func (s *TagsService) List() (*TagsListResponse, error) {
	tags, err := s.App.Tag.ListLibrary()
	if err != nil {
		return nil, err
	}
	return &TagsListResponse{Tags: tags}, nil
}

func (s *TagsService) Suggestions() (*TagSuggestionsResponse, error) {
	suggs, err := s.App.Storage.SuggestionsListPending()
	if err != nil {
		return nil, err
	}
	return &TagSuggestionsResponse{Suggestions: suggs}, nil
}

func (s *TagsService) AcceptSuggestion(id int64) error {
	return s.App.Tag.AcceptSuggestion(id)
}

func (s *TagsService) RejectSuggestion(id int64) error {
	return s.App.Tag.RejectSuggestion(id)
}

// ======================================================================
// QAService 问答服务（含流式问答）
// ======================================================================

// QASessionsResponse 会话列表响应
type QASessionsResponse struct {
	Sessions []*contract.QASession `json:"sessions"`
}

// QAMessagesResponse 消息列表响应
type QAMessagesResponse struct {
	Messages []*contract.QAMessage `json:"messages"`
}

// QAStreamRequest 流式问答请求
type QAStreamRequest struct {
	Question  string `json:"question"`
	Mode      string `json:"mode"`
	FileID    int64  `json:"fileId"`
	SessionID int64  `json:"sessionId"`
}

// QADoneResult 流式问答完成结果
type QADoneResult struct {
	SessionID int64            `json:"sessionId"`
	Sources   []contract.QASource `json:"sources"`
}

type QAService struct { App *App }

func (s *QAService) Sessions() (*QASessionsResponse, error) {
	sessions, err := s.App.QA.Sessions()
	if err != nil {
		return nil, err
	}
	return &QASessionsResponse{Sessions: sessions}, nil
}

func (s *QAService) Messages(sessionID int64) (*QAMessagesResponse, error) {
	messages, err := s.App.Storage.QAMessagesBySession(sessionID)
	if err != nil {
		return nil, err
	}
	return &QAMessagesResponse{Messages: messages}, nil
}

func (s *QAService) DeleteSession(id int64) error {
	return s.App.QA.DeleteSession(id)
}

// AskStream 流式问答：在 goroutine 中执行，通过 Wails 全局事件向所有已连接窗口推送 chunk/done/error。
// 参数 windowName 为保留参数（当前通过 application.Get() 广播到所有窗口）。
func (s *QAService) AskStream(req QAStreamRequest, _ string) error {
	if req.Question == "" {
		return fmt.Errorf("问题不能为空")
	}
	if req.Mode == "file" && req.FileID <= 0 {
		return fmt.Errorf("file 模式需要 fileId")
	}
	askReq := &contract.QARequest{
		Question:  req.Question,
		Mode:      req.Mode,
		FileID:    req.FileID,
		SessionID: req.SessionID,
	}
	cancelCh := make(chan struct{})
	go func() {
		select {
		case <-s.App.ctx.Done():
			close(cancelCh)
		}
	}()
	chunks, done := s.App.QA.AskStream(askReq, cancelCh)
	id := fmt.Sprintf("%d-%d", time.Now().UnixNano(), req.FileID)
	prefixChunk := "qa:chunk:" + id
	prefixDone := "qa:done:" + id
	prefixError := "qa:error:" + id

	for chunk := range chunks {
		emit(prefixChunk, chunk)
	}
	res := <-done
	if res.Error != "" {
		emit(prefixError, res.Error)
		return nil
	}
	emit(prefixDone, &QADoneResult{
		SessionID: res.SessionID,
		Sources:   res.Sources,
	})
	return nil
}

// emit 通过 Wails 全局应用的事件总线广播一条事件。
// 注意：Wails v3 中 application.Get() 在 app.Run() 之后才返回非 nil，
// 因此 AskStream 不会在 app.Run() 之前被调用。
func emit(name string, data interface{}) {
	app := application.Get()
	if app == nil {
		return
	}
	app.Event.Emit(name, data)
}
// ======================================================================
// StatsService 统计服务
// ======================================================================

// StatsRequest 统计请求
type StatsRequest struct {
	Range string `json:"range"`
	From  int64  `json:"from"`
	To    int64  `json:"to"`
}

// StatsResponse 统计响应
type StatsResponse struct {
	Enabled  bool                     `json:"enabled"`
	Metrics  *contract.StatsMetrics   `json:"metrics"`
}

// SetStatsEnabledRequest
type SetStatsEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

type StatsService struct { App *App }

func (s *StatsService) Get(req StatsRequest) (*StatsResponse, error) {
	rng := &contract.StatsRange{Range: req.Range, From: req.From, To: req.To}
	resp := &StatsResponse{Enabled: s.App.Stats.Enabled()}
	if resp.Enabled {
		m, err := s.App.Stats.Summary(rng)
		if err != nil {
			return resp, err
		}
		resp.Metrics = m
	}
	return resp, nil
}

func (s *StatsService) Export(format string, req StatsRequest) (string, error) {
	rng := &contract.StatsRange{Range: req.Range, From: req.From, To: req.To}
	return s.App.Stats.Export(format, rng)
}

func (s *StatsService) SetEnabled(req SetStatsEnabledRequest) error {
	return s.App.Stats.SetEnabled(req.Enabled)
}

func (s *StatsService) Purge() error {
	return s.App.Stats.Purge()
}

// ======================================================================
// CommitsService 提交服务
// ======================================================================

// AutoCommitResult 自动提交结果
type AutoCommitResult struct {
	Skipped bool              `json:"skipped"`
	Hash    string            `json:"hash,omitempty"`
	Message string            `json:"message,omitempty"`
	AI      string            `json:"ai,omitempty"`
}

// CommitFileStatus 提交文件状态
type CommitFileStatus struct {
	RelPath string `json:"relPath"`
	Code    string `json:"code"`
}

// CommitStatusResponse 提交状态响应
type CommitStatusResponse struct {
	Files []CommitFileStatus `json:"files"`
	Count int                `json:"count"`
}

// SuggestCommitResult 建议提交消息
type SuggestCommitResult struct {
	Suggestion string `json:"suggestion"`
}

// ManualCommitRequest 手动提交请求
type ManualCommitRequest struct {
	Message string `json:"message"`
}

// ManualCommitResult 手动提交结果
type ManualCommitResult struct {
	Hash    string `json:"hash"`
	Skipped bool   `json:"skipped"`
}

// CommitListResponse 提交列表
type CommitListResponse struct {
	Commits []*contract.CommitInfo `json:"commits"`
}

// CommitFilesByHashResponse 按哈希取提交改动文件
type CommitFilesByHashResponse struct {
	Files []*contract.CommitFile `json:"files"`
}

// CommitTreeAtResponse 按哈希取快照文件
type CommitTreeAtResponse struct {
	Files []*contract.VersionFile `json:"files"`
}

type CommitsService struct { App *App }

func (s *CommitsService) AutoCommit() (*AutoCommitResult, error) {
	hash, skipped, err := s.App.Git.CommitAuto(nil)
	if err != nil {
		return nil, err
	}
	if skipped {
		return &AutoCommitResult{Skipped: true}, nil
	}
	return &AutoCommitResult{Hash: hash}, nil
}

func (s *CommitsService) AutoCommitWithFiles(files []string) (*AutoCommitResult, error) {
	hash, skipped, err := s.App.Git.CommitAuto(files)
	if err != nil {
		return nil, err
	}
	if skipped {
		return &AutoCommitResult{Skipped: true}, nil
	}
	return &AutoCommitResult{Hash: hash}, nil
}

func (s *CommitsService) Status() (*CommitStatusResponse, error) {
	statusMap, err := s.App.Git.Status()
	if err != nil {
		return nil, err
	}
	files := make([]CommitFileStatus, 0, len(statusMap))
	for path, code := range statusMap {
		files = append(files, CommitFileStatus{RelPath: path, Code: code})
	}
	return &CommitStatusResponse{Files: files, Count: len(files)}, nil
}

func (s *CommitsService) Suggest() (*SuggestCommitResult, error) {
	msg, err := s.App.Timeline.SuggestCommitMessage()
	if err != nil {
		return nil, err
	}
	return &SuggestCommitResult{Suggestion: msg}, nil
}

func (s *CommitsService) Manual(req ManualCommitRequest) (*ManualCommitResult, error) {
	hash, err := s.App.Git.CommitManual(req.Message)
	if err != nil {
		return nil, err
	}
	return &ManualCommitResult{Hash: hash}, nil
}

func (s *CommitsService) List() (*CommitListResponse, error) {
	log, err := s.App.Git.Log()
	if err != nil {
		return nil, err
	}
	return &CommitListResponse{Commits: log}, nil
}

func (s *CommitsService) Files(hash string) (*CommitFilesByHashResponse, error) {
	files, err := s.App.Git.CommitFiles(hash)
	if err != nil {
		return nil, err
	}
	return &CommitFilesByHashResponse{Files: files}, nil
}

func (s *CommitsService) TreeAt(hash string) (*CommitTreeAtResponse, error) {
	files, err := s.App.Git.ListTreeAt(hash)
	if err != nil {
		return nil, err
	}
	return &CommitTreeAtResponse{Files: files}, nil
}

func (s *CommitsService) Diff(hash string) (*contract.DiffStat, error) {
	return s.App.Git.DiffStats(hash)
}

// ======================================================================
// SettingsService 设置服务
// ======================================================================

// UpdateSettingsResult 更新设置结果
type UpdateSettingsResult struct {
	RestartRequired  []string `json:"restartRequired"`
	ReindexRequired  bool     `json:"reindexRequired"`
}

// UpdateSecretsRequest 更新密钥请求
type UpdateSecretsRequest struct {
	LLMKey      string `json:"llmApiKey"`
	EmbedKey    string `json:"embedApiKey"`
	RerankKey   string `json:"rerankApiKey"`
}

type SettingsService struct { App *App }

func (s *SettingsService) Get() (map[string]interface{}, error) {
	return s.App.Config.Snapshot(), nil
}

// updateSettings 处理一般设置更新，识别需要重启/重建索引的键
func (s *SettingsService) UpdateSettings(settings map[string]interface{}) (*UpdateSettingsResult, error) {
	var restartRequired []string
	var reindexRequired bool

	for key, value := range settings {
		switch key {
		case "embed.dimensions":
			// 维度变更：更新索引模块维度并触发重建
			if dim, ok := value.(float64); ok {
				s.App.Index.SetEmbedDim(int64(dim))
			}
			reindexRequired = true
		case "stats.enabled":
			if enabled, ok := value.(bool); ok {
				_ = s.App.Stats.SetEnabled(enabled)
			}
		case "markitdown.pythonPath", "markitdown.command", "markitdown.markitdownCmd":
			// 热更新提取配置
			pythonPath, _ := settings["markitdown.pythonPath"].(string)
			command, _ := settings["markitdown.command"].(string)
			markitdownCmd, _ := settings["markitdown.markitdownCmd"].(string)
			s.App.Extract.ApplyConfig(pythonPath, command, markitdownCmd)
		default:
			restartRequired = append(restartRequired, key)
		}
		if err := s.App.Config.Set(key, value); err != nil {
			return nil, err
		}
	}
	if reindexRequired {
		_ = s.App.TriggerReindex()
	}
	return &UpdateSettingsResult{
		RestartRequired: restartRequired,
		ReindexRequired: reindexRequired,
	}, nil
}

func (s *SettingsService) UpdateSecrets(req UpdateSecretsRequest) error {
	return s.App.Config.UpsertSecrets(req.LLMKey, req.EmbedKey, req.RerankKey)
}

func (s *SettingsService) Relocate(workspace string) error {
	return s.App.Config.Relocate(workspace)
}

// ======================================================================
// TestService 测试服务（MarkItDown、LLM 连通、模型列表、Python 探测）
// ======================================================================

// TestMarkItDownRequest
type TestMarkItDownRequest struct {
	PythonPath string `json:"pythonPath"`
	Command    string `json:"command"`
}

// LLMTestRequest
type LLMTestRequest struct {
	Type        string  `json:"type"` // chat|embed|rerank|models
	BaseURL     string  `json:"baseUrl"`
	APIKey      string  `json:"apiKey"`
	Model       string  `json:"model"`
	Temperature float64 `json:"temperature"`
	Kind        string  `json:"kind"`
}

// LLMTestResult LLM 测试结果
type LLMTestResult struct {
	Ok      bool     `json:"ok"`
	Message string   `json:"message"`
	Models  []string `json:"models,omitempty"`
}

// PythonDetectResult Python 探测结果
type PythonDetectResult struct {
	Path          string `json:"path"`
	Ok            bool   `json:"ok"`
	Version       string `json:"version,omitempty"`
	MarkitdownCmd string `json:"markitdownCmd,omitempty"`
	Error         string `json:"error,omitempty"`
}

type TestService struct { App *App }

func (s *TestService) TestMarkItDown(req TestMarkItDownRequest) (*contract.ProbeResult, error) {
	ok, msg := s.App.Extract.Probe(req.PythonPath, req.Command)
	if ok {
		return &contract.ProbeResult{Ok: true, Message: msg}, nil
	}
	return &contract.ProbeResult{Ok: false, Message: msg}, nil
}

func (s *TestService) TestLLM(req LLMTestRequest) (*LLMTestResult, error) {
	var err error
	switch req.Type {
	case "chat":
		if req.BaseURL != "" && req.APIKey != "" {
			err = s.App.LLM.TestChatWith(req.BaseURL, req.APIKey, req.Model, req.Temperature)
		} else {
			err = s.App.LLM.TestChat()
		}
	case "embed":
		if req.BaseURL != "" && req.APIKey != "" {
			err = s.App.LLM.TestEmbedWith(req.BaseURL, req.APIKey, req.Model)
		} else {
			err = s.App.LLM.TestEmbed()
		}
	case "rerank":
		err = s.App.LLM.TestRerankWith(req.BaseURL, req.APIKey, req.Model)
	case "models":
		models, err2 := s.App.LLM.ListModels(req.Kind, req.BaseURL, req.APIKey)
		if err2 != nil {
			return &LLMTestResult{Ok: false, Message: err2.Error()}, nil
		}
		return &LLMTestResult{Ok: true, Models: models}, nil
	default:
		return &LLMTestResult{Ok: false, Message: "未知测试类型"}, nil
	}
	if err != nil {
		return &LLMTestResult{Ok: false, Message: err.Error()}, nil
	}
	return &LLMTestResult{Ok: true, Message: "测试成功"}, nil
}

func (s *TestService) DetectPython() (*PythonDetectResult, error) {
	// 1. 先查 PATH（排除 Windows Store 假 python）
	candidates := []string{"python", "python3", "py"}
	for _, c := range candidates {
		p, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		if runtime.GOOS == "windows" && strings.Contains(strings.ToLower(p), "windowsapps") {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return &PythonDetectResult{
				Path:          p,
				Ok:            true,
				Version:       probePyVersion(p),
				MarkitdownCmd: formatMarkitdownCmd(p),
			}, nil
		}
	}

	// 2. 搜索 Windows 常见 Python 安装目录
	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			u, err := user.Current()
			if err == nil {
				localAppData = filepath.Join(u.HomeDir, "AppData", "Local")
			}
		}
		if localAppData != "" {
			// 2a. Windows Store Python: %LOCALAPPDATA%\Python\pythoncore-<version>-64\python.exe
			pythonDir := filepath.Join(localAppData, "Python")
			if entries, err := os.ReadDir(pythonDir); err == nil {
				for _, entry := range entries {
					if entry.IsDir() && strings.HasPrefix(entry.Name(), "pythoncore-") {
						candidate := filepath.Join(pythonDir, entry.Name(), "python.exe")
						if _, err := os.Stat(candidate); err == nil {
							return &PythonDetectResult{
								Path:          candidate,
								Ok:            true,
								Version:       probePyVersion(candidate),
								MarkitdownCmd: formatMarkitdownCmd(candidate),
							}, nil
						}
					}
				}
			}
			// 2b. 常规安装器: %LOCALAPPDATA%\Programs\Python\Python<version>\python.exe
			programsDir := filepath.Join(localAppData, "Programs", "Python")
			if entries, err := os.ReadDir(programsDir); err == nil {
				for _, entry := range entries {
					if entry.IsDir() && strings.HasPrefix(entry.Name(), "Python") {
						candidate := filepath.Join(programsDir, entry.Name(), "python.exe")
						if _, err := os.Stat(candidate); err == nil {
							return &PythonDetectResult{
								Path:          candidate,
								Ok:            true,
								Version:       probePyVersion(candidate),
								MarkitdownCmd: formatMarkitdownCmd(candidate),
							}, nil
						}
					}
				}
			}
		}
		// 2c. Program Files: C:\Program Files\Python<version>\python.exe
		programFiles := os.Getenv("PROGRAMFILES")
		if programFiles != "" {
			pythonDir := filepath.Join(programFiles, "Python")
			if entries, err := os.ReadDir(pythonDir); err == nil {
				for _, entry := range entries {
					if entry.IsDir() && strings.HasPrefix(entry.Name(), "Python") {
						candidate := filepath.Join(pythonDir, entry.Name(), "python.exe")
						if _, err := os.Stat(candidate); err == nil {
							return &PythonDetectResult{
								Path:          candidate,
								Ok:            true,
								Version:       probePyVersion(candidate),
								MarkitdownCmd: formatMarkitdownCmd(candidate),
							}, nil
						}
					}
				}
			}
		}
	}

	return &PythonDetectResult{Ok: false, Error: "未检测到 Python"}, nil
}

func formatMarkitdownCmd(path string) string {
	// Windows 路径用反斜杠，需要转义
	p := strings.ReplaceAll(filepath.ToSlash(path), `/`, `\\`)
	return fmt.Sprintf(`"%s" -m markitdown "{file}"`, p)
}

func probePyVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ======================================================================
// QueueService 任务队列服务
// ======================================================================

// QueueStatusResponse 队列状态响应
type QueueStatusResponse struct {
	Running int  `json:"running"`
	Pending int  `json:"pending"`
	Paused  bool `json:"paused"`
}

type QueueService struct { App *App }

func (s *QueueService) Status() (*QueueStatusResponse, error) {
	running, pending, paused := s.App.TaskQueue.Status()
	return &QueueStatusResponse{Running: running, Pending: pending, Paused: paused}, nil
}

func (s *QueueService) Pause() error {
	return s.App.TaskQueue.Pause()
}

func (s *QueueService) Resume() error {
	return s.App.TaskQueue.Resume()
}

// ======================================================================
// DiagnosticsService 诊断服务
// ======================================================================

// DiagnosticsInfo 诊断信息
type DiagnosticsInfo struct {
	Version    string `json:"version"`
	Generation string `json:"generation"`
	QueueDepth int    `json:"queueDepth"`
	StorageOK  bool   `json:"storageOk"`
}

type DiagnosticsService struct { App *App }

func (s *DiagnosticsService) Info() (*DiagnosticsInfo, error) {
	gen := ""
	if rt := s.App.runtime.Current(); rt != nil {
		gen = rt.Generation
	}
	running, pending, _ := s.App.TaskQueue.Status()
	storageOK := true
	if s.App.Storage != nil {
		storageOK = (s.App.Storage.Ping() == nil)
	}
	return &DiagnosticsInfo{
		Version:    BuildVersion,
		Generation: gen,
		QueueDepth: running + pending,
		StorageOK:  storageOK,
	}, nil
}

func (s *DiagnosticsService) Health() string {
	return "ok"
}

func (s *DiagnosticsService) Ready() map[string]interface{} {
	result := map[string]interface{}{"status": "ok"}
	if s.App.Storage != nil && s.App.Storage.Ping() != nil {
		result["status"] = "not_ready"
		result["reasons"] = []string{"storage"}
	}
	return result
}

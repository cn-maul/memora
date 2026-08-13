// Package stats 统计模块
// 指标聚合、图表数据、导出、隐私开关
package stats

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"memora/internal/contract"
)

// IGit stats 所需的 git 接口
type IGit interface {
	Log() ([]*contract.CommitInfo, error)
	DiffStats(hash string) (*contract.DiffStat, error)
}

// IStorage stats 所需的 storage 接口
type IStorage interface {
	TagsList() ([]*contract.TagInfo, error)
}

// IConfig stats 所需的配置接口
type IConfig interface {
	Get(key string) (interface{}, error)
}

// Module 统计模块
type Module struct {
	git     IGit
	storage IStorage
	cfg     IConfig
	enabled bool
}

// New 创建统计模块
func New(git IGit, storage IStorage, cfg IConfig) *Module {
	enabled := true
	if cfg != nil {
		if v, err := cfg.Get("stats.enabled"); err == nil {
			if b, ok := v.(bool); ok {
				enabled = b
			}
		}
	}
	return &Module{
		git:     git,
		storage: storage,
		cfg:     cfg,
		enabled: enabled,
	}
}

// Enabled 返回统计开关状态
func (m *Module) Enabled() bool {
	return m.enabled
}

// SetEnabled 设置统计开关
func (m *Module) SetEnabled(v bool) error {
	m.enabled = v
	return nil
}

// Summary 获取统计摘要
func (m *Module) Summary(r *contract.StatsRange) (*contract.StatsMetrics, error) {
	if !m.enabled {
		return nil, fmt.Errorf("统计已关闭")
	}

	metrics := &contract.StatsMetrics{}

	// 获取所有提交
	commits, err := m.git.Log()
	if err != nil {
		return nil, fmt.Errorf("[stats] 获取提交日志失败: %w", err)
	}

	// 按天统计提交数
	dayCount := make(map[string]int)
	hourBuckets := contract.HourBuckets{}
	totalAdded, totalModified, totalDeleted := 0, 0, 0
	fileChangeCount := make(map[string]int)
	fileChangeDays := make(map[string]map[string]bool) // relPath → set of dates

	// 计算时间下界（用于提前终止：go-git Log 按时间倒序返回，越过下界即停止）
	var cutoff int64
	if r.Range != "" && r.Range != "custom" {
		now := time.Now()
		switch r.Range {
		case "week":
			cutoff = now.AddDate(0, 0, -7).UnixMilli()
		case "month":
			cutoff = now.AddDate(0, -1, 0).UnixMilli()
		case "quarter":
			cutoff = now.AddDate(0, -3, 0).UnixMilli()
		}
	} else if r.From > 0 {
		cutoff = r.From
	}
	// 文件级 diff 上限：避免上万提交时逐次 git diff 拖垮页面加载。
	// 提交数/时段统计不受影响（无需 diff），仅热点榜/文件变更/迭代速率在超出时近似。
	const maxFileStats = 300
	fileStatsDone := 0

	for _, commit := range commits {
		if cutoff > 0 && commit.Time < cutoff {
			break
		}
		if r.To > 0 && commit.Time > r.To {
			continue
		}

		t := time.UnixMilli(commit.Time)
		dayKey := t.Format("2006-01-02")
		dayCount[dayKey]++

		hour := t.Hour()
		switch {
		case hour >= 6 && hour < 12:
			hourBuckets.Morning++
		case hour >= 12 && hour < 18:
			hourBuckets.Afternoon++
		case hour >= 18 && hour < 24:
			hourBuckets.Evening++
		default:
			hourBuckets.Night++
		}

		if fileStatsDone < maxFileStats {
			stat, err := m.git.DiffStats(commit.Hash)
			if err == nil {
				totalAdded += stat.Added
				totalModified += stat.Modified
				totalDeleted += stat.Deleted
				for _, f := range stat.Files {
					fileChangeCount[f]++
					if fileChangeDays[f] == nil {
						fileChangeDays[f] = make(map[string]bool)
					}
					fileChangeDays[f][dayKey] = true
				}
			}
			fileStatsDone++
		}
	}

	// 组装 commitsByDay
	for date, count := range dayCount {
		metrics.CommitsByDay = append(metrics.CommitsByDay, contract.DayCount{
			Date:  date,
			Count: count,
		})
	}
	sort.Slice(metrics.CommitsByDay, func(i, j int) bool {
		return metrics.CommitsByDay[i].Date < metrics.CommitsByDay[j].Date
	})

	metrics.FileChanges = contract.FileChanges{
		Added:    totalAdded,
		Modified: totalModified,
		Deleted:  totalDeleted,
	}

	metrics.HourBuckets = hourBuckets

	// 热度榜 Top N（从 fileChangeCount 取）
	type fileCount struct {
		relPath string
		count   int
	}
	var fcList []fileCount
	for path, count := range fileChangeCount {
		fcList = append(fcList, fileCount{relPath: path, count: count})
	}
	sort.Slice(fcList, func(i, j int) bool {
		return fcList[i].count > fcList[j].count
	})
	for i := 0; i < 10 && i < len(fcList); i++ {
		metrics.HotFiles = append(metrics.HotFiles, contract.HotFile{
			RelPath: fcList[i].relPath,
			Count:   fcList[i].count,
		})
	}

	// 标签分布
	tags, err := m.storage.TagsList()
	if err == nil {
		for _, tag := range tags {
			metrics.TagDistribution = append(metrics.TagDistribution, contract.TagCount{
				Tag:   tag.Name,
				Count: tag.Count,
			})
		}
	}

	// 迭代速率（按文件：活跃文件数 / 天数）
	totalDays := len(dayCount)
	if totalDays > 0 {
		activeFiles := len(fileChangeDays)
		if activeFiles == 0 {
			metrics.IterationRate = 0
		} else {
			metrics.IterationRate = float64(activeFiles) / float64(totalDays)
		}
	}

	return metrics, nil
}

// Export 导出统计报告
func (m *Module) Export(format string, r *contract.StatsRange) (string, error) {
	metrics, err := m.Summary(r)
	if err != nil {
		return "", err
	}

	switch format {
	case "csv":
		return m.exportCSV(metrics)
	case "md":
		return m.exportMarkdown(metrics)
	default:
		return m.exportMarkdown(metrics)
	}
}

// exportCSV 导出 CSV
func (m *Module) exportCSV(metrics *contract.StatsMetrics) (string, error) {
	var b strings.Builder
	b.WriteString("date,commits\n")
	for _, d := range metrics.CommitsByDay {
		b.WriteString(fmt.Sprintf("%s,%d\n", d.Date, d.Count))
	}
	b.WriteString("\nchanges,count\n")
	b.WriteString(fmt.Sprintf("added,%d\n", metrics.FileChanges.Added))
	b.WriteString(fmt.Sprintf("modified,%d\n", metrics.FileChanges.Modified))
	b.WriteString(fmt.Sprintf("deleted,%d\n", metrics.FileChanges.Deleted))
	return b.String(), nil
}

// exportMarkdown 导出 Markdown
func (m *Module) exportMarkdown(metrics *contract.StatsMetrics) (string, error) {
	var b strings.Builder
	b.WriteString("# 工作简报\n\n")

	b.WriteString("## 提交活跃度\n\n")
	b.WriteString("| 日期 | 提交数 |\n|------|--------|\n")
	for _, d := range metrics.CommitsByDay {
		b.WriteString(fmt.Sprintf("| %s | %d |\n", d.Date, d.Count))
	}

	b.WriteString("\n## 文件改动\n\n")
	b.WriteString(fmt.Sprintf("- 新增：%d\n", metrics.FileChanges.Added))
	b.WriteString(fmt.Sprintf("- 修改：%d\n", metrics.FileChanges.Modified))
	b.WriteString(fmt.Sprintf("- 删除：%d\n\n", metrics.FileChanges.Deleted))

	b.WriteString("## 时段分布\n\n")
	b.WriteString(fmt.Sprintf("- 上午：%d\n", metrics.HourBuckets.Morning))
	b.WriteString(fmt.Sprintf("- 下午：%d\n", metrics.HourBuckets.Afternoon))
	b.WriteString(fmt.Sprintf("- 傍晚：%d\n", metrics.HourBuckets.Evening))
	b.WriteString(fmt.Sprintf("- 夜间：%d\n\n", metrics.HourBuckets.Night))

	b.WriteString("## 标签分布\n\n")
	b.WriteString("| 标签 | 数量 |\n|------|------|\n")
	for _, t := range metrics.TagDistribution {
		b.WriteString(fmt.Sprintf("| %s | %d |\n", t.Tag, t.Count))
	}

	return b.String(), nil
}

// Purge 清除统计缓存
func (m *Module) Purge() error {
	// 统计模块无持久化缓存，仅内存汇总，调用 Summary 时自动重算。
	// 注意：不清除 enabled 开关——用户主动关闭统计后执行 Purge 不应被重新打开（修复）。
	return nil
}

package qa

import (
	"strings"
	"testing"

	"memora/internal/contract"
)

func TestExtractFilenameKeyword(t *testing.T) {
	cases := []struct {
		question string
		want     string
	}{
		{"帮我看看“预算表”里的数字", "预算表"},
		{"《方案.xlsx》里写了什么", "方案.xlsx"},
		{"帮我查一下会议纪要的内容", "会议纪要的内容"},
		{"这个文档讲了什么？", "文档讲了什么"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := extractFilenameKeyword(c.question); got != c.want {
			t.Errorf("extractFilenameKeyword(%q) = %q, want %q", c.question, got, c.want)
		}
	}
}

// linkifySourceRefs：回答中的文件路径（含反引号/裸路径）应规整为 [文件=path] 标记，
// 已有的 [文件=...] 标记不得被二次嵌套
func TestLinkifySourceRefs(t *testing.T) {
	sources := []contract.QASource{
		{RelPath: "10分类汇总.xlsx", Seq: 1},
		{RelPath: `知识库\行测·职测\资料分析\统计术语.md`, Seq: 3},
	}

	answer := "- **包含数据**：\n" +
		"  - `10分类汇总.xlsx`：包含销售数据。\n" +
		"- **知识文件**：\n" +
		"  - `知识库\\行测·职测\\资料分析\\统计术语.md`：统计术语解释。\n" +
		"- 已标记的：[文件=10分类汇总.xlsx, 段落=1] 也是数据。裸路径 10分类汇总.xlsx 也要转。"

	got := linkifySourceRefs(answer, sources)
	t.Logf("result:\n%s", got)

	// 反引号包裹的路径应变成标记（不再带反引号）
	if strings.Contains(got, "`10分类汇总.xlsx`") {
		t.Fatalf("反引号路径未规整:\n%s", got)
	}
	// 裸路径应变成标记
	if strings.Contains(got, "10分类汇总.xlsx") && !strings.Contains(got, "[文件=10分类汇总.xlsx]") {
		t.Fatalf("裸路径未规整:\n%s", got)
	}
	// 已存在的标记原样保留（不产生 [[文件= 嵌套）
	if strings.Contains(got, "[[文件=") {
		t.Fatalf("已有标记被二次嵌套:\n%s", got)
	}
	// 中文路径（含反斜杠）应规整
	if !strings.Contains(got, "[文件=知识库\\行测·职测\\资料分析\\统计术语.md]") {
		t.Fatalf("中文路径未规整:\n%s", got)
	}
}

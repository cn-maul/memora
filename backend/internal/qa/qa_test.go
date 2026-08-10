package qa

import "testing"

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

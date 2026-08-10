package index

import "testing"

func TestDetectDocTypePPTXLSX(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"a.pdf", "pdf"},
		{"a.docx", "docx"},
		{"a.pptx", "pptx"},
		{"a.xlsx", "xlsx"},
		{"a.txt", "txt"},
		{"a.md", "md"},
		{"a.doc", "ignored"},
		{"a.PPTX", "pptx"}, // 大小写不敏感
		{"a.XLSX", "xlsx"},
		{"a.png", ""},
		{"dir/b.pptx", "pptx"},
	}
	for _, c := range cases {
		if got := detectDocType(c.path); got != c.want {
			t.Errorf("detectDocType(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

package agent

import "testing"

func TestParseVSCodeFileExt(t *testing.T) {
	cases := []struct {
		name, title, want string
	}{
		{"标准三段", "main.go — haruka — Visual Studio Code", ".go"},
		{"Insiders", "README.md — thesis — Visual Studio Code - Insiders", ".md"},
		{"无扩展名文件", "设置 — haruka — Visual Studio Code", ""},
		{"两段不足", "haruka — Visual Studio Code", ""},
		{"非 VS Code 产品名", "main.go — haruka — 某编辑器", ""},
		{"过长扩展名不采信", "photo.12345678 — x — Visual Studio Code", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseVSCodeFileExt(tc.title); got != tc.want {
				t.Fatalf("ext = %q, want %q", got, tc.want)
			}
		})
	}
}

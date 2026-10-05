package agent

import "testing"

func TestParseJetBrainsTitle(t *testing.T) {
	cases := []struct {
		name, title, wantProject string
	}{
		{"标准三段", "haruka – main.go – IntelliJ IDEA", "haruka"},
		{"PyCharm", "aurora – models.py – PyCharm 2023.2", "aurora"},
		{"中文项目名", "论文 – chapter1.tex – IntelliJ IDEA", "论文"},
		{"两段不足", "haruka – IntelliJ IDEA", ""},
		{"产品名不在表内", "foo – bar – 某编辑器", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseJetBrainsTitle(tc.title)
			if got.Project != tc.wantProject {
				t.Fatalf("project = %q, want %q", got.Project, tc.wantProject)
			}
		})
	}
}

func TestParseVSCodeTitle(t *testing.T) {
	cases := []struct {
		name, title, wantProject, wantExt string
	}{
		{"标准三段", "main.go — haruka — Visual Studio Code", "haruka", ".go"},
		{"Insiders", "README.md — thesis — Visual Studio Code - Insiders", "thesis", ".md"},
		{"无扩展名文件", "设置 — haruka — Visual Studio Code", "haruka", ""},
		{"两段不足", "haruka — Visual Studio Code", "", ""},
		{"中文目录", "设计稿 — 网站重构 — VSCodium", "网站重构", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseVSCodeTitle(tc.title)
			if got.Project != tc.wantProject || got.FileExt != tc.wantExt {
				t.Fatalf("got (%q, %q), want (%q, %q)",
					got.Project, got.FileExt, tc.wantProject, tc.wantExt)
			}
		})
	}
}

func TestParseIDETitleUnknownIDE(t *testing.T) {
	if got := parseIDETitle("notepad", "x – y – IntelliJ IDEA"); got.Project != "" {
		t.Fatalf("unknown ide should yield empty, got %q", got.Project)
	}
}

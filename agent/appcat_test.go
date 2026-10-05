package agent

import (
	"strings"
	"testing"
)

func testRuntime(t *testing.T, mutate func(*Config)) *Runtime {
	t.Helper()
	cfg := defaultConfig()
	cfg.Server = "https://haruka.example.com"
	cfg.Token = "tok"
	cfg.DeviceID = "d_test01"
	if mutate != nil {
		mutate(&cfg)
	}
	rt, err := ValidateConfig(cfg)
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	return &rt
}

func TestClassifyIDEWithAlias(t *testing.T) {
	rt := testRuntime(t, func(c *Config) {
		c.Activity.Aliases = map[string]string{"jetbrains:haruka": "aurora", "vscode:haruka": "aurora"}
	})
	cl := NewClassifier(rt)

	got := cl.Classify("idea64.exe", "haruka – main.go – IntelliJ IDEA")
	if got.Kind != "ide" || got.App != "IntelliJ IDEA" || got.Project != "aurora" {
		t.Fatalf("jetbrains alias: %+v", got)
	}
	got = cl.Classify("Code.exe", "main.go — haruka — Visual Studio Code")
	if got.Kind != "ide" || got.Project != "aurora" {
		t.Fatalf("vscode alias: %+v", got)
	}
	if got.FileType != "" {
		t.Fatalf("file_type 默认关闭，不应上报: %+v", got)
	}
}

func TestClassifyUnmappedPolicy(t *testing.T) {
	rt := testRuntime(t, nil)
	got := NewClassifier(rt).Classify("Code.exe", "secret.go — secret-client — Visual Studio Code")
	if got.Project != "" {
		t.Fatalf("unmapped=hidden 不应外发项目名: %+v", got)
	}

	rt = testRuntime(t, func(c *Config) { c.Activity.Unmapped = "hash" })
	got = NewClassifier(rt).Classify("Code.exe", "secret.go — secret-client — Visual Studio Code")
	if !strings.HasPrefix(got.Project, "p-") || len(got.Project) != len("p-")+4 || got.Project == "p-client" {
		t.Fatalf("unmapped=hash 应为不可逆短哈希: %+v", got)
	}
}

func TestClassifyFileTypeOptIn(t *testing.T) {
	rt := testRuntime(t, func(c *Config) {
		c.Privacy.FileType = true
		c.Activity.Aliases = map[string]string{"vscode:haruka": "aurora"}
	})
	got := NewClassifier(rt).Classify("Code.exe", "main.go — haruka — Visual Studio Code")
	if got.FileType != ".go" {
		t.Fatalf("file_type 开启后应为扩展名: %+v", got)
	}
}

func TestClassifyPrivateApp(t *testing.T) {
	rt := testRuntime(t, func(c *Config) {
		c.Activity.PrivateApps = []string{"*Bank*"}
	})
	cl := NewClassifier(rt)
	got := cl.Classify("SomeBank.exe", "账户总览")
	if got.Kind != "private" || got.Category != "private" || got.App != "" || got.Project != "" {
		t.Fatalf("私密应用应整级匿名化: %+v", got)
	}
	got = cl.Classify("KeePassXC.exe", "数据库名 – KeePassXC")
	if got.Kind != "private" {
		t.Fatalf("默认私密清单未命中 KeePass*: %+v", got)
	}
}

func TestClassifyBrowserDropsTitle(t *testing.T) {
	cl := NewClassifier(testRuntime(t, nil))
	got := cl.Classify("chrome.exe", "内部系统 – 登录 — Google Chrome")
	if got.Kind != "browser" || got.App != "Google Chrome" {
		t.Fatalf("browser: %+v", got)
	}
	if got.Project != "" || got.FileType != "" {
		t.Fatalf("浏览器不应携带标题派生字段: %+v", got)
	}
}

func TestClassifyUnknownProcess(t *testing.T) {
	cl := NewClassifier(testRuntime(t, nil))
	got := cl.Classify("SomeTool.exe", "whatever")
	if got.Kind != "app" || got.App != "SomeTool.exe" || got.Category != "other" {
		t.Fatalf("unknown process: %+v", got)
	}
}

package agent

import (
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

func TestClassifyIDE(t *testing.T) {
	cl := NewClassifier(testRuntime(t, nil))

	got := cl.Classify("idea64.exe", "haruka – main.go – IntelliJ IDEA")
	if got.Kind != "ide" || got.App != "IntelliJ IDEA" || got.Category != "coding" {
		t.Fatalf("jetbrains: %+v", got)
	}
	got = cl.Classify("Code.exe", "main.go — haruka — Visual Studio Code")
	if got.Kind != "ide" || got.App != "Visual Studio Code" {
		t.Fatalf("vscode: %+v", got)
	}
	if got.FileType != "" {
		t.Fatalf("file_type 默认关闭，不应上报: %+v", got)
	}
}

func TestClassifyFileTypeOptIn(t *testing.T) {
	rt := testRuntime(t, func(c *Config) { c.Privacy.FileType = true })
	got := NewClassifier(rt).Classify("Code.exe", "main.go — haruka — Visual Studio Code")
	if got.FileType != ".go" {
		t.Fatalf("file_type 开启后应为扩展名: %+v", got)
	}
}

func TestClassifyBuiltinAppMap(t *testing.T) {
	cl := NewClassifier(testRuntime(t, nil))
	got := cl.Classify("bilibili.exe", "bilibili")
	if got.App != "B 站" || got.Category != "media" {
		t.Fatalf("内置映射表未命中 bilibili.exe: %+v", got)
	}
	got = cl.Classify("Weixin.exe", "微信")
	if got.App != "微信" || got.Category != "other" {
		t.Fatalf("内置映射表未命中 weixin.exe: %+v", got)
	}
}

func TestClassifyPrivateApp(t *testing.T) {
	rt := testRuntime(t, func(c *Config) {
		c.Activity.PrivateApps = []string{"*Bank*"}
	})
	cl := NewClassifier(rt)
	got := cl.Classify("SomeBank.exe", "账户总览")
	if got.Kind != "private" || got.Category != "private" || got.App != "" {
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
	if got.FileType != "" {
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

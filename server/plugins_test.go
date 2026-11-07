package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "haruka/internal/plugins/persona" // 注册真实 persona 插件（capabilities 两档用）
	"haruka/protocol"

	"gopkg.in/yaml.v3"
)

func yamlSection(t *testing.T, s string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(s), &n); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	if len(n.Content) == 0 {
		t.Fatal("空配置节")
	}
	return n.Content[0]
}

// 声明型插件全链路：配置节 → 装载 → 同名 custom 观测透传进快照 → TTL 到期缺席。
func TestDeclaredPluginLifecycle(t *testing.T) {
	te := newTestEnv(t, nil)
	initTestPlugins(t, te, map[string]*yaml.Node{
		"reading": yamlSection(t, "title: 正在读\nfields:\n  - key: title\n    label: 书名\n  - key: author\n"),
	})
	dev := te.addDevice(t, protocol.KindDesktop, 10*time.Minute)
	te.heartbeat(t, dev)
	te.emit(t, dev, protocol.TypeCustom, te.now,
		protocol.CustomData{Source: "reading", Payload: json.RawMessage(`{"title":"三体","author":"刘慈欣"}`)})

	body, err := te.e.SnapshotJSON()
	if err != nil {
		t.Fatal(err)
	}
	var snap map[string]json.RawMessage
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	raw, ok := snap["reading"]
	if !ok {
		t.Fatalf("reading 节应出现在快照: %s", body)
	}
	var sec map[string]any
	if err := json.Unmarshal(raw, &sec); err != nil {
		t.Fatal(err)
	}
	if sec["title"] != "三体" || sec["author"] != "刘慈欣" || sec["as_of"] == nil {
		t.Fatalf("透传 payload + as_of 不符: %v", sec)
	}

	// TTL（30 分钟）过后读时重判 → 节缺席。
	te.now = te.now.Add(31 * time.Minute)
	body, _ = te.e.SnapshotJSON()
	snap = map[string]json.RawMessage{}
	json.Unmarshal(body, &snap)
	if _, ok := snap["reading"]; ok {
		t.Fatalf("TTL 过期后 reading 节应缺席: %s", body)
	}
}

// 单一装载路径的拒绝面：未知节、无效声明、非法插件名一律启动失败。
func TestInitPluginsRejects(t *testing.T) {
	cases := map[string]map[string]string{
		"未知节 typo":       {"wakatimeX": "foo: 1\n"},
		"声明缺 title":      {"reading": "fields:\n  - key: a\n"},
		"声明缺 fields":     {"reading": "title: 正在读\n"},
		"声明 field 空 key": {"reading": "title: X\nfields:\n  - key: \"\"\n"},
		"非法插件名":          {"Bad Name!": "title: X\nfields:\n  - key: a\n"},
	}
	for name, sections := range cases {
		te := newTestEnv(t, nil)
		nodes := map[string]*yaml.Node{}
		for k, v := range sections {
			nodes[k] = yamlSection(t, v)
		}
		te.e.rt.PluginSections = nodes
		if err := te.e.InitPlugins(); err == nil {
			t.Errorf("%s 应拒绝启动", name)
		}
	}
}

// capabilities 两档保真：公开档无 choices/settings；设备令牌档完整。
func TestCapabilitiesTwoFidelity(t *testing.T) {
	te := newTestEnv(t, nil)
	initTestPlugins(t, te, map[string]*yaml.Node{
		"persona": yamlSection(t, "list:\n  - Muika\n  - 真冬\ndefault: Muika\n"),
	})
	token, err := te.e.OwnerTokenGenerate(false)
	if err != nil {
		t.Fatal(err)
	}

	fetch := func(bearer string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		te.e.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("capabilities 应 200: %d", rec.Code)
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	pub := fetch("")
	priv := fetch(token) // 测试库的 owner_token_hash 存哈希——直接用真实令牌
	pubRaw, _ := json.Marshal(pub)
	if strings.Contains(string(pubRaw), "真冬") {
		t.Fatalf("公开档不得泄露人格名单: %s", pubRaw)
	}
	plugins, _ := priv["plugins"].([]any)
	if len(plugins) == 0 {
		t.Fatal("清单不应为空")
	}
	privRaw, _ := json.Marshal(priv)
	if !strings.Contains(string(privRaw), "真冬") {
		t.Fatalf("令牌档应包含 choices: %s", privRaw)
	}
}

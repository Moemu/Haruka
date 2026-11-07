package persona

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"haruka/internal/host"
	"haruka/server"

	"gopkg.in/yaml.v3"
)

type fakeHost struct {
	db  *sql.DB
	now time.Time
	cfg map[string]*yaml.Node
}

func (f *fakeHost) DB() *sql.DB          { return f.db }
func (f *fakeHost) Logger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func (f *fakeHost) Now() time.Time       { return f.now }
func (f *fakeHost) Config(name string) *yaml.Node {
	if f.cfg == nil {
		return nil
	}
	return f.cfg[name]
}

func sectionNode(t *testing.T, s string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(s), &n); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	if len(n.Content) == 0 {
		return nil
	}
	return n.Content[0]
}

func newFakeHost(t *testing.T, section string) *fakeHost {
	t.Helper()
	db, err := server.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	fh := &fakeHost{db: db, now: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	if section != "" {
		fh.cfg = map[string]*yaml.Node{"persona": sectionNode(t, section)}
	}
	return fh
}

func newPlugin(t *testing.T, fh *fakeHost) *Plugin {
	p, err := New(fh)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p.(*Plugin)
}

func deliver(ts time.Time, payload string) host.Delivered {
	return host.Delivered{DeviceID: "d_test", TS: ts, ReceivedAt: ts, Source: "persona", Payload: json.RawMessage(payload)}
}

func snapshot(t *testing.T, p *Plugin, fh *fakeHost) map[string]any {
	t.Helper()
	sec, err := p.ContributeSnapshot(fh.DB(), fh.now)
	if err != nil {
		t.Fatalf("ContributeSnapshot: %v", err)
	}
	if sec == nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(sec, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

const validSection = "list:\n  - Muika\n  - 真冬\ndefault: Muika\n"

func TestValidate(t *testing.T) {
	// 缺节停用，不校验。
	p := newPlugin(t, newFakeHost(t, ""))
	if p.Enabled() {
		t.Fatal("缺节应停用")
	}
	// 启用但缺列表 → 拒绝启动。
	p = newPlugin(t, newFakeHost(t, "enabled: true\n"))
	if !p.Enabled() {
		t.Fatal("有节默认启用")
	}
	if err := p.Validate(); err == nil {
		t.Fatal("启用但缺 list 应拒绝启动")
	}
	// default 不在列表 → 拒绝启动。
	p = newPlugin(t, newFakeHost(t, "list: [真冬]\ndefault: Muika\n"))
	if err := p.Validate(); err == nil {
		t.Fatal("default 不在 list 应拒绝启动")
	}
	// 重复项 → 拒绝启动。
	p = newPlugin(t, newFakeHost(t, "list: [Muika, Muika]\n"))
	if err := p.Validate(); err == nil {
		t.Fatal("list 重复项应拒绝启动")
	}
	// 合法配置。
	p = newPlugin(t, newFakeHost(t, validSection))
	if err := p.Validate(); err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
}

// 切换/清除/默认人格回退：封闭列表校验。
func TestSwitchClearAndDefault(t *testing.T) {
	fh := newFakeHost(t, validSection)
	p := newPlugin(t, fh)

	// 不在列表中的人格拒收。
	if err := p.HandleEvent(deliver(fh.now, `{"persona":"未知体"}`), fh.DB()); err == nil {
		t.Fatal("不在列表中的人格应被拒收")
	}
	// 列表内切换生效。
	if err := p.HandleEvent(deliver(fh.now, `{"persona":"真冬","text":"安静一会儿"}`), fh.DB()); err != nil {
		t.Fatal(err)
	}
	snap := snapshot(t, p, fh)
	if snap["persona"] != "真冬" || snap["text"] != "安静一会儿" {
		t.Fatalf("接管应生效: %v", snap)
	}
	// 清除回落默认人格。
	if err := p.HandleEvent(deliver(fh.now.Add(time.Minute), `{"clear":true}`), fh.DB()); err != nil {
		t.Fatal(err)
	}
	snap = snapshot(t, p, fh)
	if snap["persona"] != "Muika" || snap["default"] != true {
		t.Fatalf("清除应回落默认人格: %v", snap)
	}
	// 墓碑防复活。
	if err := p.HandleEvent(deliver(fh.now.Add(-time.Hour), `{"persona":"真冬"}`), fh.DB()); err != nil {
		t.Fatal(err)
	}
	if snap = snapshot(t, p, fh); snap["persona"] != "Muika" {
		t.Fatalf("清除墓碑之后的旧设置不得复活: %v", snap)
	}
}

// 无默认人格且无接管 → 节缺席。
func TestAbsentWithoutDefault(t *testing.T) {
	fh := newFakeHost(t, "list: [Muika]\n")
	p := newPlugin(t, fh)
	if snap := snapshot(t, p, fh); snap != nil {
		t.Fatalf("无默认且无接管应节缺席: %v", snap)
	}
}

// choices 属私有清单槽位（随 Describe 下发，capabilities 端按令牌分级输出）。
func TestChoicesInDescriptor(t *testing.T) {
	p := newPlugin(t, newFakeHost(t, validSection))
	d := p.Describe()
	if len(d.Choices) != 2 || d.Choices[0] != "Muika" || d.Choices[1] != "真冬" {
		t.Fatalf("choices 应为配置列表: %v", d.Choices)
	}
	if d.Title != "当前人格" {
		t.Fatalf("title 不符: %q", d.Title)
	}
}

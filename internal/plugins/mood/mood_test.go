package mood

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

// fakeHost：临时库 + 注入时钟（与 wakatime 包同构，不共享以免测试互耦）。
type fakeHost struct {
	db  *sql.DB
	now time.Time
}

func (f *fakeHost) DB() *sql.DB          { return f.db }
func (f *fakeHost) Logger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func (f *fakeHost) Now() time.Time       { return f.now }
func (f *fakeHost) Config(string) *yaml.Node {
	return nil
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	db, err := server.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &fakeHost{db: db, now: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
}

func deliver(ts time.Time, payload string) host.Delivered {
	return host.Delivered{DeviceID: "d_test", TS: ts, ReceivedAt: ts, Source: "mood", Payload: json.RawMessage(payload)}
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

// 接管态排序与防复活（原 server 断言 5，语义归插件后迁到这里）。
func TestOrderingAndTombstone(t *testing.T) {
	fh := newFakeHost(t)
	p, err := New(fh)
	if err != nil {
		t.Fatal(err)
	}
	m := p.(*Plugin)

	if err := m.HandleEvent(deliver(fh.now, `{"mood":"focused","text":"赶 deadline 中"}`), fh.DB()); err != nil {
		t.Fatal(err)
	}
	if snap := snapshot(t, m, fh); snap["mood"] != "focused" {
		t.Fatalf("设置应生效: %v", snap)
	}

	// 清除（另一设备、一小时后）。
	fh.now = fh.now.Add(time.Hour)
	if err := m.HandleEvent(deliver(fh.now, `{"clear":true}`), fh.DB()); err != nil {
		t.Fatal(err)
	}
	if snap := snapshot(t, m, fh); snap != nil {
		t.Fatalf("清除后 mood 应消失: %v", snap)
	}

	// 重放 30 分钟前的旧设置：ts ≤ cleared_at，不得复活。
	if err := m.HandleEvent(deliver(fh.now.Add(-30*time.Minute), `{"mood":"chilling"}`), fh.DB()); err != nil {
		t.Fatal(err)
	}
	if snap := snapshot(t, m, fh); snap != nil {
		t.Fatalf("清除墓碑之后的旧设置不得复活: %v", snap)
	}

	// 墓碑之后的新设置正常生效。
	if err := m.HandleEvent(deliver(fh.now.Add(time.Minute), `{"mood":"tired"}`), fh.DB()); err != nil {
		t.Fatal(err)
	}
	if snap := snapshot(t, m, fh); snap["mood"] != "tired" {
		t.Fatalf("墓碑后的新设置应生效: %v", snap)
	}
}

// until 缺席无限期生效；带期限的照旧过期。
func TestUntilExpiry(t *testing.T) {
	fh := newFakeHost(t)
	p, _ := New(fh)
	m := p.(*Plugin)

	if err := m.HandleEvent(deliver(fh.now, `{"mood":"真冬"}`), fh.DB()); err != nil {
		t.Fatal(err)
	}
	fh.now = fh.now.Add(5 * time.Hour)
	if snap := snapshot(t, m, fh); snap["mood"] != "真冬" || snap["until"] != nil {
		t.Fatalf("until 缺席应无限期生效: %v", snap)
	}

	if err := m.HandleEvent(deliver(fh.now, `{"mood":"tired","until":"`+fh.now.Add(time.Hour).UTC().Format(time.RFC3339)+`"}`), fh.DB()); err != nil {
		t.Fatal(err)
	}
	fh.now = fh.now.Add(2 * time.Hour)
	if snap := snapshot(t, m, fh); snap != nil {
		t.Fatalf("带期限状态过期后应消失: %v", snap)
	}
}

func TestPayloadValidation(t *testing.T) {
	fh := newFakeHost(t)
	p, _ := New(fh)
	m := p.(*Plugin)
	cases := map[string]string{
		"缺 mood":          `{}`,
		"URL 文本":          `{"mood":"happy","text":"看 https://secret.example"}`,
		"坐标 mood":         `{"mood":"31.19, 121.43"}`,
		"路径 mood":         `{"mood":"a/b"}`,
		"text 超长":         `{"mood":"happy","text":"` + string(make([]rune, 61)) + `"}`,
		"未知字段":            `{"mood":"happy","window":"x"}`,
		"until 非 RFC3339": `{"mood":"happy","until":"明天"}`,
	}
	for name, payload := range cases {
		if err := m.HandleEvent(deliver(fh.now, payload), fh.DB()); err == nil {
			t.Errorf("%s 应被拒收", name)
		}
	}
}

// manual 时代遗留状态迁移：manual_state 活跃行 → 插件命名空间。
func TestLegacyMigration(t *testing.T) {
	fh := newFakeHost(t)
	_, err := fh.DB().Exec(`CREATE TABLE manual_state (
		id INTEGER PRIMARY KEY CHECK (id = 1), updated_ts TEXT NOT NULL,
		cleared INTEGER NOT NULL DEFAULT 1, cleared_ts TEXT, data TEXT, until TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.DB().Exec(`INSERT INTO manual_state (id, updated_ts, cleared, data) VALUES
		(1, '2026-11-07T11:00:00Z', 0, '{"mood":"真冬","text":"安静一会儿"}')`); err != nil {
		t.Fatal(err)
	}
	p, err := New(fh)
	if err != nil {
		t.Fatal(err)
	}
	snap := snapshot(t, p.(*Plugin), fh)
	if snap["mood"] != "真冬" || snap["text"] != "安静一会儿" {
		t.Fatalf("遗留状态应迁移: %v", snap)
	}
}

func TestDefaultEnabled(t *testing.T) {
	p, err := New(newFakeHost(t))
	if err != nil || !p.Enabled() {
		t.Fatalf("缺节应默认启用: %v", err)
	}
}

package wakatime

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"haruka/internal/host"
	"haruka/server"

	"gopkg.in/yaml.v3"
)

// fakeHost：临时库 + 注入时钟 + 配置节。
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

func newFakeHost(t *testing.T, sections map[string]string) *fakeHost {
	t.Helper()
	db, err := server.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := map[string]*yaml.Node{}
	for name, s := range sections {
		cfg[name] = sectionNode(t, s)
	}
	return &fakeHost{db: db, now: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC), cfg: cfg}
}

// fakeWakaTime 起一个假官方 API，校验 Bearer token 与 range 参数。
func fakeWakaTime(t *testing.T, token string) *httptest.Server {
	t.Helper()
	auth := func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer "+token }
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/users/current/summaries", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("range") != "Today" || !auth(r) {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"grand_total": map[string]any{"total_seconds": 9930.5}}},
		})
	})
	mux.HandleFunc("/api/v1/users/current/all_time_since_today", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "bad", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"total_seconds": 4448280.2}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestConfigSemantics(t *testing.T) {
	// 缺节停用；有节默认启用；启用但缺 token → 拒绝启动。
	p, err := New(newFakeHost(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled() {
		t.Fatal("缺节应停用")
	}
	p, err = New(newFakeHost(t, map[string]string{"wakatime": "enabled: true\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled() {
		t.Fatal("有节默认启用")
	}
	if err := p.(*Plugin).Validate(); err == nil {
		t.Fatal("启用但缺 token 应拒绝启动")
	}
	p, err = New(newFakeHost(t, map[string]string{"wakatime": "token: tok\nenabled: false\n"}))
	if err != nil || p.Enabled() {
		t.Fatalf("enabled: false 应停用: %v", err)
	}
}

func TestPollAndSnapshot(t *testing.T) {
	fh := newFakeHost(t, map[string]string{"wakatime": "token: tok\n"})
	p, _ := New(fh)
	srv := fakeWakaTime(t, "tok")

	if err := p.(*Plugin).poll(context.Background(), srv.Client(), srv.URL+"/api/v1/users/current/"); err != nil {
		t.Fatalf("poll: %v", err)
	}
	sec, err := p.(*Plugin).ContributeSnapshot(fh.DB(), fh.now)
	if err != nil || sec == nil {
		t.Fatalf("ContributeSnapshot: %v %s", err, sec)
	}
	var cv CodingV
	if err := json.Unmarshal(sec, &cv); err != nil {
		t.Fatal(err)
	}
	if cv.TodaySeconds == nil || *cv.TodaySeconds != 9930.5 || cv.TotalSeconds != 4448280.2 {
		t.Fatalf("coding 数据不符: %+v", cv)
	}
	if cv.AsOf != host.FormatTime(fh.now) {
		t.Fatalf("as_of 应使用注入时钟: %q", cv.AsOf)
	}
}

func TestStaleGuard(t *testing.T) {
	fh := newFakeHost(t, map[string]string{"wakatime": "token: tok\n"})
	p, _ := New(fh)
	write := func(asOf time.Time) {
		raw, _ := json.Marshal(CodingV{TodaySeconds: fptr(3600), TotalSeconds: 100000, AsOf: host.FormatTime(asOf)})
		if err := host.StateSet(fh.DB(), "wakatime", "state", string(raw)); err != nil {
			t.Fatal(err)
		}
	}

	write(fh.now.Add(-25 * time.Hour))
	sec, err := p.(*Plugin).ContributeSnapshot(fh.DB(), fh.now)
	if err != nil || sec == nil {
		t.Fatal("过期数据也应保留累计")
	}
	var cv CodingV
	json.Unmarshal(sec, &cv)
	if cv.TodaySeconds != nil || cv.TotalSeconds != 100000 {
		t.Fatalf("过期今日口径未下线: %+v", cv)
	}

	write(fh.now.Add(-23 * time.Hour))
	sec, _ = p.(*Plugin).ContributeSnapshot(fh.DB(), fh.now)
	json.Unmarshal(sec, &cv)
	if cv.TodaySeconds == nil || *cv.TodaySeconds != 3600 {
		t.Fatalf("窗口内的今日口径被误下线: %+v", cv)
	}
}

func TestPollFailureKeepsOldValue(t *testing.T) {
	fh := newFakeHost(t, map[string]string{"wakatime": "token: tok\n"})
	p, _ := New(fh)
	old := `{"total_seconds":2000,"as_of":"2026-11-07T00:00:00Z"}`
	if err := host.StateSet(fh.DB(), "wakatime", "state", old); err != nil {
		t.Fatal(err)
	}
	if err := p.(*Plugin).poll(context.Background(), &http.Client{Timeout: 2 * time.Second}, "http://127.0.0.1:1/api/v1/users/current/"); err == nil {
		t.Fatal("连接拒绝应报错")
	}
	got, ok, _ := host.StateGet(fh.DB(), "wakatime", "state")
	if !ok || got != old {
		t.Fatalf("旧值被改动: %q", got)
	}
}

func TestLegacyKeyMigration(t *testing.T) {
	fh := newFakeHost(t, map[string]string{"wakatime": "token: tok\n"})
	legacy := `{"total_seconds":999,"as_of":"2026-11-07T00:00:00Z"}`
	if _, err := fh.DB().Exec(`INSERT INTO server_state (key, value) VALUES ('wakatime', ?)`, legacy); err != nil {
		t.Fatal(err)
	}
	p, err := New(fh)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, _ := host.StateGet(fh.DB(), "wakatime", "state")
	if !ok || got != legacy {
		t.Fatalf("旧键应迁移到插件命名空间: %q", got)
	}
	_ = p
}

func fptr(f float64) *float64 { return &f }

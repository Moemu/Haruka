package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func fptr(f float64) *float64 { return &f }

// fakeWakaTime 起一个假官方 API，校验 Basic auth 与 range 参数，返回固定数据。
func fakeWakaTime(t *testing.T, apiKey string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/users/current/summaries", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("range") != "Today" {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		if got, want := r.Header.Get("Authorization"), "Basic "+base64.StdEncoding.EncodeToString([]byte(apiKey)); got != want {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"grand_total": map[string]any{"total_seconds": 9930.5},
				"languages": []map[string]any{
					{"name": "Go", "total_seconds": 8000},
				},
			}},
		})
	})
	mux.HandleFunc("/api/v1/users/current/all_time_since_today", func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Basic "+base64.StdEncoding.EncodeToString([]byte(apiKey)); got != want {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"total_seconds": 4448280.2}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func readCoding(t *testing.T, e *Engine) string {
	t.Helper()
	v, ok, err := stateGetRow(e.db, wakatimeStateKey)
	if err != nil {
		t.Fatalf("readCoding: %v", err)
	}
	if !ok {
		t.Fatal("server_state 缺少 wakatime 键")
	}
	return v
}

func TestWakaTimePollAndSnapshot(t *testing.T) {
	te := newTestEnv(t, func(c *Config) { c.WakaTime.APIKey = "waka_test_key" })
	srv := fakeWakaTime(t, "waka_test_key")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := te.e.pollWakaTime(ctx, srv.Client(), srv.URL+"/api/v1/users/current/", "waka_test_key"); err != nil {
		t.Fatalf("pollWakaTime: %v", err)
	}

	snap, err := te.e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	cv := snap.Coding
	if cv == nil {
		t.Fatal("Snapshot 缺少 coding 字段")
	}
	if cv.TodaySeconds == nil || *cv.TodaySeconds != 9930.5 {
		t.Errorf("today_seconds = %v, want 9930.5", cv.TodaySeconds)
	}
	if cv.TotalSeconds != 4448280.2 {
		t.Errorf("total_seconds = %v, want 4448280.2", cv.TotalSeconds)
	}
	if cv.AsOf != formatTime(te.now) {
		t.Errorf("as_of = %q, want %q（应使用引擎时钟）", cv.AsOf, formatTime(te.now))
	}
}

func TestWakaTimeStaleGuard(t *testing.T) {
	te := newTestEnv(t, nil)
	writeCoding := func(asOf time.Time) {
		t.Helper()
		cv := CodingV{TodaySeconds: fptr(3600), TotalSeconds: 100000, AsOf: formatTime(asOf)}
		raw, _ := json.Marshal(cv)
		if _, err := te.e.db.Exec(`INSERT INTO server_state (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, wakatimeStateKey, string(raw)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// as_of 超过一天：今日口径下线，累计保留。
	writeCoding(te.now.Add(-25 * time.Hour))
	snap, err := te.e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Coding == nil {
		t.Fatal("过期数据也应保留累计")
	}
	if snap.Coding.TodaySeconds != nil {
		t.Errorf("过期今日口径未下线: %+v", snap.Coding)
	}
	if snap.Coding.TotalSeconds != 100000 {
		t.Errorf("累计被误删: %+v", snap.Coding)
	}

	// 23 小时前仍在窗口内：今日口径保留。
	writeCoding(te.now.Add(-23 * time.Hour))
	snap, err = te.e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Coding == nil || snap.Coding.TodaySeconds == nil || *snap.Coding.TodaySeconds != 3600 {
		t.Errorf("窗口内的今日口径被误下线: %+v", snap.Coding)
	}
}

func TestWakaTimePollFailureKeepsOldValue(t *testing.T) {
	te := newTestEnv(t, nil)
	cv := CodingV{TodaySeconds: fptr(60), TotalSeconds: 2000, AsOf: formatTime(te.now)}
	raw, _ := json.Marshal(cv)
	if _, err := te.e.db.Exec(`INSERT INTO server_state (key, value) VALUES (?, ?)`, wakatimeStateKey, string(raw)); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// 拉取失败（连接拒绝）：报错且旧值原样保留。
	ctx := context.Background()
	if err := te.e.pollWakaTime(ctx, &http.Client{Timeout: 2 * time.Second}, "http://127.0.0.1:1/api/v1/users/current/", "k"); err == nil {
		t.Fatal("连接拒绝应报错")
	}
	got := readCoding(t, te.e)
	if got != string(raw) {
		t.Errorf("旧值被改动: got %s want %s", got, raw)
	}
}

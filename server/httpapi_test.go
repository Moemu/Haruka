package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"haruka/protocol"
)

// HTTP 层测试：严格校验、设备绑定、ETag、受信代理取 IP。

type httpFixture struct {
	ts     *httptest.Server
	e      *Engine
	token  string
	device Device
}

func newHTTPFixture(t *testing.T) *httpFixture {
	te := newTestEnv(t, nil)
	dev, token, err := te.e.DeviceAdd("test-dev", protocol.KindDesktop, 10*time.Minute, "server")
	if err != nil {
		t.Fatalf("DeviceAdd: %v", err)
	}
	hs := httptest.NewServer(te.e.Handler())
	t.Cleanup(hs.Close)
	return &httpFixture{ts: hs, e: te.e, token: token, device: dev}
}

func (f *httpFixture) post(t *testing.T, path, token string, body any) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, f.ts.URL+path, bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func sampleEnvelope(deviceID string) map[string]any {
	ev, _ := protocol.NewEvent(protocol.TypePresence, 1, time.Now(), protocol.PresenceData{State: "active", IdleMinutes: 0})
	env := protocol.BuildEnvelope(deviceID, protocol.KindDesktop, false, []protocol.Event{ev})
	raw, _ := json.Marshal(env)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestIngestHappyPath(t *testing.T) {
	f := newHTTPFixture(t)
	resp := f.post(t, "/v1/ingest", f.token, sampleEnvelope(f.device.ID))
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestIngestStrictValidation(t *testing.T) {
	f := newHTTPFixture(t)

	badPayloads := map[string]map[string]any{
		"未知字段": {
			"type": "activity",
			"data": map[string]any{"kind": "ide", "app": "IDE", "category": "coding", "window_title": "aurora – main.go"},
		},
		"坐标进 text": {
			"type": "manual",
			"data": map[string]any{"mood": "focused", "text": "31.19, 121.43"},
		},
		"路径进 project": {
			"type": "activity",
			"data": map[string]any{"kind": "ide", "app": "IDE", "category": "coding", "project": "../etc/passwd"},
		},
		"URL 进 text": {
			"type": "manual",
			"data": map[string]any{"mood": "focused", "text": "看 https://secret.example.com"},
		},
		"precision 不一致": {
			"type": "location",
			"data": map[string]any{"source": "ip", "precision": "city", "city": "上海", "district": "徐汇区"},
		},
		"坐标字段": {
			"type": "location",
			"data": map[string]any{"source": "gps_coarse", "precision": "city", "city": "上海", "latitude": 31.19},
		},
		"非法 mood": {
			"type": "manual",
			"data": map[string]any{"mood": "ecstatic"},
		},
		"未知类型": {
			"type": "health",
			"data": map[string]any{"bpm": 76},
		},
	}
	for name, payload := range badPayloads {
		t.Run(name, func(t *testing.T) {
			ev := map[string]any{
				"id":  fmt.Sprintf("e_%032x", time.Now().UnixNano()),
				"seq": 1, "ts": formatTime(time.Now()), "type": payload["type"], "data": payload["data"],
			}
			env := map[string]any{
				"v": 1, "device_id": f.device.ID, "device_kind": "desktop",
				"sent_at": formatTime(time.Now()), "events": []any{ev},
			}
			resp := f.post(t, "/v1/ingest", f.token, env)
			defer resp.Body.Close()
			if resp.StatusCode != 400 {
				t.Fatalf("应拒绝（400），得到 %d", resp.StatusCode)
			}
		})
	}
}

func TestIngestDeviceBinding(t *testing.T) {
	f := newHTTPFixture(t)

	// 无令牌。
	if resp := f.post(t, "/v1/ingest", "", sampleEnvelope(f.device.ID)); resp.StatusCode != 401 {
		t.Fatalf("缺令牌应 401，得到 %d", resp.StatusCode)
	}
	// 错误令牌。
	if resp := f.post(t, "/v1/ingest", "wrong-token", sampleEnvelope(f.device.ID)); resp.StatusCode != 401 {
		t.Fatalf("错令牌应 401，得到 %d", resp.StatusCode)
	}
	// 令牌有效但 device_id 是别的设备：403（令牌绑定登记设备）。
	if resp := f.post(t, "/v1/ingest", f.token, sampleEnvelope("d_other01")); resp.StatusCode != 403 {
		t.Fatalf("device_id 不匹配应 403，得到 %d", resp.StatusCode)
	}
}

func TestPublicStatusETagAndHidden(t *testing.T) {
	f := newHTTPFixture(t)

	resp, err := http.Get(f.ts.URL + "/v1/public/status")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	etag := resp.Header.Get("ETag")
	cache := resp.Header.Get("Cache-Control")
	resp.Body.Close()
	if resp.StatusCode != 200 || etag == "" || cache != "no-cache" {
		t.Fatalf("首请求应 200 且带 ETag/no-cache: %d %q %q", resp.StatusCode, etag, cache)
	}
	var snap PublicStatus
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("解码快照: %v", err)
	}

	// If-None-Match → 304。
	req, _ := http.NewRequest(http.MethodGet, f.ts.URL+"/v1/public/status", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 304 {
		t.Fatalf("条件请求应 304，得到 %d", resp2.StatusCode)
	}

	// owner hide → 快照只剩 visibility。
	if _, err := f.e.OwnerTokenGenerate(false); err != nil {
		t.Fatal(err)
	}
	_ = f.e // hide 直接走引擎（CLI 同路径）
	if err := f.e.Hide(nil); err != nil {
		t.Fatal(err)
	}
	resp3, _ := http.Get(f.ts.URL + "/v1/public/status")
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	var hidden PublicStatus
	if err := json.Unmarshal(body3, &hidden); err != nil {
		t.Fatal(err)
	}
	if hidden.Visibility != "hidden" || hidden.Location != nil || hidden.Mood != nil {
		t.Fatalf("隐身快照应只剩 visibility: %+v", hidden)
	}
}

func TestWhoamiIP(t *testing.T) {
	f := newHTTPFixture(t)
	resp, err := http.Get(f.ts.URL + "/v1/whoami-ip")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("解码: %v", err)
	}
	if body.IP == "" || net.ParseIP(body.IP) == nil {
		t.Fatalf("应返回合法出口 IP: %+v", body)
	}
}

func TestControlRequiresOwnerToken(t *testing.T) {
	f := newHTTPFixture(t)
	resp := f.post(t, "/v1/control", "not-owner", map[string]any{"action": "hide"})
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("无 owner 令牌应 401，得到 %d", resp.StatusCode)
	}
}

func TestClientIPTrustedProxy(t *testing.T) {
	trusted := []*net.IPNet{parseTrustedProxy("127.0.0.1")}

	// Caddy 场景：socket 对端是受信回环，XFF 最后一跳是真实设备。
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.9")
	if got := ClientIP(req, trusted); got == nil || got.String() != "10.0.0.9" {
		t.Fatalf("应取最右非受信跳 10.0.0.9，得到 %v", got)
	}

	// 无 XFF：用 socket 对端。
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.RemoteAddr = "203.0.113.7:9999"
	if got := ClientIP(req2, trusted); got == nil || got.String() != "203.0.113.7" {
		t.Fatalf("应取对端地址: %v", got)
	}

	// 不可信来源伪造 XFF：忽略伪造头，取 socket 对端。
	req3 := httptest.NewRequest("GET", "/", nil)
	req3.RemoteAddr = "8.8.8.8:1234"
	req3.Header.Set("X-Forwarded-For", "5.5.5.5")
	if got := ClientIP(req3, trusted); got == nil || got.String() != "8.8.8.8" {
		t.Fatalf("伪造 XFF 应被忽略: %v", got)
	}
}

func TestRootRedirectsToStatus(t *testing.T) {
	f := newHTTPFixture(t)
	// 不跟随重定向，才能断言 302 本身。
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	// 仅精确根路径跳转；未知路径仍应 404，不吞掉错误。
	resp, err := client.Get(f.ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("/ 应 302，得到 %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/status" {
		t.Fatalf("Location = %q，应为 /status", loc)
	}

	resp, err = client.Get(f.ts.URL + "/no-such-path")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知路径应 404，得到 %d", resp.StatusCode)
	}
}

func TestStatusPageServed(t *testing.T) {
	f := newHTTPFixture(t)

	resp, err := http.Get(f.ts.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/status 应 200，得到 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("HTML 必须携带 CSP 与 nosniff")
	}
	if resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("ETag") == "" {
		t.Fatal("静态页也应 no-cache + ETag，保证升级后立即生效")
	}
	for _, marker := range []string{`id="hero-state"`, `id="hero-word"`, "status.js", "/icon.webp"} {
		if !bytes.Contains(body, []byte(marker)) {
			t.Fatalf("页面缺少标记 %q", marker)
		}
	}

	// 条件请求 → 304。
	req, _ := http.NewRequest(http.MethodGet, f.ts.URL+"/status", nil)
	req.Header.Set("If-None-Match", resp.Header.Get("ETag"))
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 304 {
		t.Fatalf("静态页条件请求应 304，得到 %d", resp2.StatusCode)
	}

	for _, tc := range []struct{ path, ctype string }{
		{"/status.css", "text/css"},
		{"/status.js", "text/javascript"},
		{"/icon.webp", "image/webp"},
	} {
		r, err := http.Get(f.ts.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), tc.ctype) {
			t.Fatalf("%s 应 200 且为 %s：%d %q", tc.path, tc.ctype, r.StatusCode, r.Header.Get("Content-Type"))
		}
	}
}

// 断言 10（M2 验收）：公开响应不得含 IP、坐标、标题/路径等敏感片段。
// 上游契约就是这份 JSON——组件归下游实现后，公开内容的边界全靠这里守住。
func TestPublicResponseNoSensitiveFragments(t *testing.T) {
	te := newTestEnv(t, nil)
	dev := te.addDevice(t, protocol.KindDesktop, 10*time.Minute)
	te.heartbeat(t, dev)
	te.emit(t, dev, protocol.TypePresence, te.now, protocol.PresenceData{State: "active"})
	te.emit(t, dev, protocol.TypeActivity, te.now,
		protocol.ActivityData{Kind: "ide", App: "Visual Studio Code", Category: "coding"})
	te.emit(t, dev, protocol.TypeLocation, te.now,
		protocol.LocationData{Source: "ip", Precision: "district", Country: "中国", Province: "上海市", City: "上海", District: "徐汇区"})
	te.emit(t, dev, protocol.TypeManual, te.now,
		protocol.ManualData{Mood: "😊", Text: "今天也在好好生活", Until: te.now.Add(2 * time.Hour).UTC().Format(time.RFC3339)})

	req := httptest.NewRequest(http.MethodGet, "/v1/public/status", nil)
	rec := httptest.NewRecorder()
	te.e.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("快照应 200，得到 %d", rec.Code)
	}
	raw := rec.Body.String()

	// 出口 IP 203.0.113.7 走过 ingest，绝不能出现在公开响应里；
	// 任意 IPv4 / 坐标模式同样不得出现。
	for name, re := range map[string]*regexp.Regexp{
		"IPv4": regexp.MustCompile(`(?:\d{1,3}\.){3}\d{1,3}`),
		"坐标":   regexp.MustCompile(`\d{1,3}\.\d{1,3}\s*,\s*\d{1,3}\.\d{1,3}`),
	} {
		if re.MatchString(raw) {
			t.Fatalf("公开响应含 %s 模式: %s", name, raw)
		}
	}
	if strings.Contains(raw, "203.0.113.7") {
		t.Fatal("公开响应含出口 IP")
	}

	// 字段名白名单 walk：快照结构里不允许出现承载原始观测的键。
	denied := map[string]bool{
		"ip": true, "title": true, "window_title": true,
		"lat": true, "lon": true, "latitude": true, "longitude": true,
		"coord": true, "path": true, "file": true,
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, sub := range x {
				if denied[strings.ToLower(k)] {
					t.Fatalf("公开快照出现敏感键 %q", k)
				}
				walk(sub)
			}
		case []any:
			for _, sub := range x {
				walk(sub)
			}
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("解码快照: %v", err)
	}
	walk(decoded)
}

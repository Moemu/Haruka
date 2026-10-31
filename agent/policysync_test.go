package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"haruka/protocol"
)

// 断言（审查 P1-3）：定位策略必须同步服务端并确认成功，失败持续重试。
func TestPolicySyncConfirmsAndRetries(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	var fail bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, r.URL.Path)
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	rt := testRuntime(t, func(c *Config) { c.Location.Mode = "off" })
	rt.ServerURL = ts.URL
	ps := NewPolicySync(rt, discardLogger())

	// 服务端故障：不得确认。
	fail = true
	ps.syncOnce(context.Background())
	if ps.Confirmed() != "" {
		t.Fatalf("同步失败时不得确认策略: %q", ps.Confirmed())
	}
	// 恢复：确认成功。
	fail = false
	ps.syncOnce(context.Background())
	if ps.Confirmed() != "off" {
		t.Fatalf("同步成功应确认 off: %q", ps.Confirmed())
	}
	if len(requests) != 2 {
		t.Fatalf("应恰好两次请求: %v", requests)
	}
}

// location 采集器在策略未确认前不得发送本地解析事件（避免双源）。
func TestLocationCollectorGatesOnPolicySync(t *testing.T) {
	rt := testRuntime(t, func(c *Config) {
		c.Location.Mode = "local"
		c.Location.LocalDB = "../../tmp-dev/ip2region.xdb"
	})
	deps := Deps{DeviceID: "d_test", DeviceKind: protocol.KindDesktop, Seq: &Seq{mu: make(chan struct{}, 1)}, Log: discardLogger()}
	loc := NewLocationCollector(deps, "local")
	loc.serverURL = "http://127.0.0.1:1" // 不可达也不该被调用
	if err := loc.EnableLocal(rt.Cfg.Location.LocalDB); err != nil {
		t.Skip("xdb 未下载，跳过")
	}
	// 策略未确认：不发送，也不触碰 whoami-ip。
	events, err := loc.Tick(time.Now())
	if err != nil || len(events) != 0 {
		t.Fatalf("策略未确认前不得发送位置事件: %v %v", events, err)
	}
	ps := NewPolicySync(rt, discardLogger())
	ps.mu.Lock()
	ps.confirmed = "local"
	ps.mu.Unlock()
	loc.deps.Policy = ps
	// 确认后发送（whoami-ip 不可达 → 返回错误，证明确实发起了请求）。
	if _, err := loc.Tick(time.Now()); err == nil {
		t.Fatal("确认后应发起 whoami-ip 请求")
	}
}

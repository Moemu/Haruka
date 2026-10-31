package server

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// 状态页静态资源随二进制分发（设计案 §10：独立页由 GET /status 提供）。
//
//go:embed web/status.html web/status.css web/status.js web/icon.webp web/bg-desktop.webp web/bg-mobile.webp
var webFiles embed.FS

type webAsset struct {
	body []byte
	etag string
}

var webAssets = map[string]webAsset{}

func init() {
	for _, name := range []string{"status.html", "status.css", "status.js", "icon.webp", "bg-desktop.webp", "bg-mobile.webp"} {
		body, err := webFiles.ReadFile("web/" + name)
		if err != nil {
			panic(err) // go:embed 在编译期保证文件存在
		}
		sum := sha256.Sum256(body)
		webAssets[name] = webAsset{body: body, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
	}
}

// Handler 组装全部 HTTP 路由。
func (e *Engine) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", e.handleHealthz)
	mux.HandleFunc("POST /v1/ingest", e.handleIngest)
	mux.HandleFunc("POST /v1/control", e.handleControl)
	mux.HandleFunc("POST /v1/device-policy", e.handleDevicePolicy)
	mux.HandleFunc("POST /v1/device-capability", e.handleDeviceCapability)
	mux.HandleFunc("GET /v1/public/status", e.handlePublicStatus)
	mux.HandleFunc("GET /v1/whoami-ip", e.handleWhoamiIP)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/status", http.StatusFound) })
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) { e.serveWeb(w, r, "status.html") })
	mux.HandleFunc("GET /status.css", func(w http.ResponseWriter, r *http.Request) { e.serveWeb(w, r, "status.css") })
	mux.HandleFunc("GET /status.js", func(w http.ResponseWriter, r *http.Request) { e.serveWeb(w, r, "status.js") })
	mux.HandleFunc("GET /icon.webp", func(w http.ResponseWriter, r *http.Request) { e.serveWeb(w, r, "icon.webp") })
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) { e.serveWeb(w, r, "icon.webp") })
	mux.HandleFunc("GET /bg-desktop.webp", func(w http.ResponseWriter, r *http.Request) { e.serveWeb(w, r, "bg-desktop.webp") })
	mux.HandleFunc("GET /bg-mobile.webp", func(w http.ResponseWriter, r *http.Request) { e.serveWeb(w, r, "bg-mobile.webp") })
	if e.rt.RateRPM > 0 {
		return e.limit(mux)
	}
	return mux
}

// rateLimiter 按 ClientIP 做令牌桶限流。桶表有硬上限，溢出后并入共享桶，
// 防伪造来源 IP 撑爆内存。单阈值不分路由（ponytail: 公开/认证接口分阈值，被滥用时再加）。
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*rateBucket
	shared  *rate.Limiter
	limit   rate.Limit
	burst   int
}

type rateBucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

const (
	rateBucketCap      = 8192
	rateBucketIdleTime = time.Hour
)

func newRateLimiter(rpm, burst int) *rateLimiter {
	perReq := rate.Every(time.Minute / time.Duration(rpm)) // rpm 次/分钟 = 每 rpm 分之一分钟一次
	l := &rateLimiter{
		buckets: make(map[string]*rateBucket),
		shared:  rate.NewLimiter(perReq, burst),
		limit:   perReq,
		burst:   burst,
	}
	go l.cleanup()
	return l
}

func (l *rateLimiter) cleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		l.mu.Lock()
		for key, b := range l.buckets {
			if time.Since(b.lastSeen) > rateBucketIdleTime {
				delete(l.buckets, key)
			}
		}
		l.mu.Unlock()
	}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= rateBucketCap {
			return l.shared.Allow()
		}
		b = &rateBucket{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.buckets[key] = b
	}
	b.lastSeen = time.Now()
	return b.limiter.Allow()
}

// limit 用每 IP 令牌桶包住全部路由；healthz 豁免，监控探针不受限。
func (e *Engine) limit(next http.Handler) http.Handler {
	l := newRateLimiter(e.rt.RateRPM, e.rt.RateBurst)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || l.allow(ClientIP(r, e.trustedNets()).String()) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Retry-After", "1")
		httpError(w, http.StatusTooManyRequests, "请求过于频繁")
	})
}

// serveWeb 输出内置状态页资源：no-cache + ETag 保证升级后页面立即生效；
// HTML 额外带 CSP（页面零外部依赖，全部 self）与 no-referrer。
func (e *Engine) serveWeb(w http.ResponseWriter, r *http.Request, name string) {
	a := webAssets[name]
	switch name {
	case "status.html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self'; script-src 'self'; "+
				"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
	case "status.css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case "status.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case "icon.webp":
		w.Header().Set("Content-Type", "image/webp")
	case "bg-desktop.webp", "bg-mobile.webp":
		w.Header().Set("Content-Type", "image/webp")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", a.etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Header.Get("If-None-Match") == a.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(a.body)
}

func (e *Engine) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
}

// ClientIP 按受信代理白名单取设备 IP：从右向左跳过受信代理，
// 第一个非受信跳即设备；未配置受信代理时用 socket 对端（设计案 6.3 节）。
func ClientIP(r *http.Request, trusted []*net.IPNet) net.IP {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}
	remote := net.ParseIP(remoteHost)

	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return remote
	}
	parts := strings.Split(xff, ",")
	ips := make([]net.IP, 0, len(parts)+1)
	for _, p := range parts {
		if ip := net.ParseIP(strings.TrimSpace(p)); ip != nil {
			ips = append(ips, ip)
		}
	}
	ips = append(ips, remote)
	for i := len(ips) - 1; i >= 0; i-- {
		if ipInTrusted(ips[i], trusted) {
			continue
		}
		return ips[i]
	}
	return ips[0]
}

func ipInTrusted(ip net.IP, trusted []*net.IPNet) bool {
	for _, n := range trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func parseTrustedProxy(s string) *net.IPNet {
	if _, n, err := net.ParseCIDR(s); err == nil {
		return n
	}
	if ip := net.ParseIP(s); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return &net.IPNet{IP: ip4, Mask: net.CIDRMask(32, 32)}
		}
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}
	}
	return nil
}

func (e *Engine) trustedNets() []*net.IPNet {
	var out []*net.IPNet
	for _, s := range e.rt.Cfg.TrustedProxies {
		if n := parseTrustedProxy(s); n != nil {
			out = append(out, n)
		}
	}
	return out
}

func (e *Engine) handleIngest(w http.ResponseWriter, r *http.Request) {
	device, ok := e.authDevice(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		httpError(w, http.StatusBadRequest, "请求体过大或读取失败")
		return
	}
	env, err := ValidateEnvelope(body)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	n, err := e.IngestEnvelope(device, ClientIP(r, e.trustedNets()), env)
	if err != nil {
		if errors.Is(err, errForbidden) {
			httpError(w, http.StatusForbidden, "device_id 与令牌登记不符")
			return
		}
		httpError(w, http.StatusInternalServerError, "处理失败")
		e.log.Error("ingest 处理失败", "device", device.ID, "error", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": n})
}

func (e *Engine) authDevice(w http.ResponseWriter, r *http.Request) (Device, bool) {
	device, ok := e.authDeviceQuiet(r)
	if !ok {
		httpError(w, http.StatusUnauthorized, "令牌无效")
		return Device{}, false
	}
	return device, true
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

type controlRequest struct {
	Action string     `json:"action"` // hide | show
	Until  *time.Time `json:"until,omitempty"`
}

func decodeRequest(w http.ResponseWriter, r *http.Request, out any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("请求体只能包含一个 JSON 对象")
	}
	return nil
}

func (e *Engine) handleControl(w http.ResponseWriter, r *http.Request) {
	if !e.authOwner(r) {
		httpError(w, http.StatusUnauthorized, "owner 令牌无效")
		return
	}
	var req controlRequest
	if err := decodeRequest(w, r, &req); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch req.Action {
	case "hide":
		if err := e.Hide(req.Until); err != nil {
			httpError(w, http.StatusInternalServerError, "hide 失败")
			return
		}
	case "show":
		if err := e.Show(); err != nil {
			httpError(w, http.StatusInternalServerError, "show 失败")
			return
		}
	default:
		httpError(w, http.StatusBadRequest, "action 只能是 hide 或 show")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

type policyRequest struct {
	DeviceID string `json:"device_id"`
	Policy   string `json:"policy"` // server | local | off
}

// capabilityRequest 是设备自报 presence 能力的请求体。
type capabilityRequest struct {
	Presence string `json:"presence"` // ok | degraded | unsupported
}

// handleDeviceCapability 记录设备的 presence 能力（agent 启动探测后上报，
// 仅设备令牌可改自己）。unknown presence 的 activity 展示资格据此判定
// （设计案 7.5 节）——服务端依据来源设备的能力，不看聚合后的 unknown。
func (e *Engine) handleDeviceCapability(w http.ResponseWriter, r *http.Request) {
	device, ok := e.authDevice(w, r)
	if !ok {
		return
	}
	var req capabilityRequest
	if err := decodeRequest(w, r, &req); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Presence == "" {
		httpError(w, http.StatusBadRequest, "presence 不能为空")
		return
	}
	if err := e.SetDeviceCapability(device.ID, req.Presence); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (e *Engine) handleDevicePolicy(w http.ResponseWriter, r *http.Request) {
	owner := e.authOwner(r)
	device, deviceOK := e.authDeviceQuiet(r)
	if !owner && !deviceOK {
		httpError(w, http.StatusUnauthorized, "令牌无效")
		return
	}
	var req policyRequest
	if err := decodeRequest(w, r, &req); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.DeviceID != "" {
		// 指定设备：需要 owner 令牌，或设备令牌恰好登记为该设备。
		if owner {
			// owner 可改任意设备
		} else if deviceOK && device.ID == req.DeviceID {
			// 设备改自己
		} else {
			httpError(w, http.StatusUnauthorized, "无权修改该设备策略")
			return
		}
	} else {
		device, ok := e.authDevice(w, r)
		if !ok {
			return
		}
		req.DeviceID = device.ID
	}
	if err := e.SetDevicePolicy(req.DeviceID, req.Policy); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (e *Engine) authDeviceQuiet(r *http.Request) (Device, bool) {
	token := bearerToken(r)
	if token == "" {
		return Device{}, false
	}
	d, err := e.DeviceByToken(token)
	return d, err == nil
}

func (e *Engine) authOwner(r *http.Request) bool {
	token := bearerToken(r)
	return token != "" && e.OwnerAuth(token)
}

func (e *Engine) handlePublicStatus(w http.ResponseWriter, r *http.Request) {
	snap, err := e.Snapshot()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "快照生成失败")
		e.log.Error("快照生成失败", "error", err)
		return
	}
	body, err := json.Marshal(snap)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "序列化失败")
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// no-cache + ETag：允许缓存但逐次重验证，敏感状态变化不被缓存放大（设计案 6.4 节）。
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", etag)
	if e.corsOrigin(r) != "" {
		w.Header().Set("Access-Control-Allow-Origin", e.corsOrigin(r))
		w.Header().Set("Vary", "Origin")
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

func (e *Engine) corsOrigin(r *http.Request) string {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return ""
	}
	for _, allowed := range e.rt.Cfg.CORSOrigins {
		if allowed == "*" || allowed == origin {
			return origin
		}
	}
	return ""
}

// handleWhoamiIP 返回调用方出口 IP（方案 C 用）；不写日志、不落盘。
func (e *Engine) handleWhoamiIP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ip := ClientIP(r, e.trustedNets())
	writeJSON(w, http.StatusOK, map[string]string{"ip": ip.String()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// PolicySync 把桌面端的定位配置（location.mode）同步为服务端策略
// （POST /v1/device-policy，设计案 6.3 节）。「关闭定位」必须由服务端
// 强制执行：只停本地采集器挡不住服务端的 IP 派生。同步失败会持续重试，
// 且在确认成功之前，方案 C 的本地位置事件暂停发送（避免双源）。
type PolicySync struct {
	serverURL string
	token     string
	deviceID  string
	desired   string
	log       *slog.Logger
	client    *http.Client

	mu        sync.Mutex
	confirmed string
}

func NewPolicySync(rt *Runtime, log *slog.Logger) *PolicySync {
	return &PolicySync{
		serverURL: rt.ServerURL,
		token:     rt.Cfg.Token,
		deviceID:  rt.Cfg.DeviceID,
		desired:   rt.Cfg.Location.Mode,
		log:       log,
		client:    &http.Client{Timeout: 5 * time.Second},
	}
}

// Confirmed 返回服务端已确认的策略（"" 表示尚未确认）。
func (p *PolicySync) Confirmed() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.confirmed
}

// Run 循环同步直到 ctx 结束：未确认时立即尝试并按分钟重试。
func (p *PolicySync) Run(ctx context.Context) {
	p.syncOnce(ctx)
	if p.Confirmed() == p.desired {
		return // 已一致：无需常驻
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if p.Confirmed() == p.desired {
				return
			}
			p.syncOnce(ctx)
		}
	}
}

func (p *PolicySync) syncOnce(ctx context.Context) {
	body, _ := json.Marshal(map[string]string{"device_id": p.deviceID, "policy": p.desired})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.serverURL+"/v1/device-policy", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.client.Do(req)
	if err != nil {
		p.log.Warn("定位策略同步失败（本地位置事件暂停发送）", "policy", p.desired, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		p.log.Warn("定位策略同步被拒绝（本地位置事件暂停发送）", "policy", p.desired, "status", resp.Status)
		return
	}
	p.mu.Lock()
	p.confirmed = p.desired
	p.mu.Unlock()
	p.log.Info("定位策略已同步服务端", "policy", p.desired)
}

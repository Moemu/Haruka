package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// CapSync 定期重新探测并同步 presence 能力，避免会话变化后沿用旧资格。
// 临时故障、暂停和采集器关闭上报 degraded；只有确认不支持才上报
// unsupported。服务端对能力记录设置有效期，失联后恢复保守展示。
type CapSync struct {
	serverURL string
	token     string
	probe     func() Capability
	interval  time.Duration
	log       *slog.Logger
	client    *http.Client
}

func NewCapSync(rt *Runtime, probe func() Capability, log *slog.Logger) *CapSync {
	interval := rt.Heartbeat
	if interval <= 0 {
		interval = time.Minute
	}
	return &CapSync{
		serverURL: rt.ServerURL,
		token:     rt.Cfg.Token,
		probe:     probe,
		interval:  interval,
		log:       log,
		client:    &http.Client{Timeout: 5 * time.Second},
	}
}

// Run 定期重新探测并续报；失败时服务端的能力资格按 TTL 过期。
func (c *CapSync) Run(ctx context.Context) {
	c.syncOnce(ctx)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.syncOnce(ctx)
		}
	}
}

func (c *CapSync) syncOnce(ctx context.Context) {
	capability := c.probe()
	if capability != CapOK && capability != CapUnsupported {
		capability = CapDegraded
	}
	body, _ := json.Marshal(struct {
		Presence Capability `json:"presence"`
	}{capability})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL+"/v1/device-capability", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		c.log.Warn("presence 能力同步失败", "capability", capability, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		c.log.Warn("presence 能力上报被拒绝", "capability", capability, "status", resp.Status)
		return
	}
}

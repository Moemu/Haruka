// Package wakatime 是拉取型插件的参考实现：服务端每 5 分钟拉取 WakaTime
// 官方 API 的今日与累计编码时长，存 server_state 插件命名空间，快照贡献
// 顶层 "coding" 节。token 为个人 OAuth token（wakatime.com/oauth 生成），
// API key 自 2026-11-01 起仅限插件使用。
package wakatime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"haruka/internal/host"
)

const (
	apiBaseURL  = "https://api.wakatime.com/api/v1/users/current/"
	pollEvery   = 5 * time.Minute
	freshCutoff = 24 * time.Hour // as_of 超过此值视为今日口径失真
)

// CodingV 是编码时长的存储与公开形态（WakaTime 口径：编辑器活动时长）。
// TodaySeconds 缺席表示数据过旧、今日口径已下线；为 0 表示当天还没写代码。
type CodingV struct {
	TodaySeconds *float64 `json:"today_seconds,omitempty"`
	TotalSeconds float64  `json:"total_seconds"`
	AsOf         string   `json:"as_of"`
}

type config struct {
	Enabled *bool  `yaml:"enabled"`
	Token   string `yaml:"token"`
}

// Plugin 实现拉取型数据源：Runner（轮询）+ Contributor（coding 节）。
type Plugin struct {
	h       host.Host
	enabled bool
	token   string
}

func init() { host.Register(New) }

// New 构造插件；配置节解析失败在这里直接报错（启动期拒绝启动）。
func New(h host.Host) (host.Plugin, error) {
	var cfg config
	node := h.Config("wakatime")
	if node != nil {
		if err := node.Decode(&cfg); err != nil {
			return nil, fmt.Errorf("wakatime 配置节解析失败: %w", err)
		}
	}
	p := &Plugin{h: h, enabled: node != nil && (cfg.Enabled == nil || *cfg.Enabled), token: cfg.Token}
	if p.enabled {
		// 0.1.x 旧键迁移：server_state 的历史键 "wakatime" → 插件命名空间。
		if _, ok, _ := host.StateGet(h.DB(), "wakatime", "state"); !ok {
			var legacy string
			if err := h.DB().QueryRow(`SELECT value FROM server_state WHERE key = 'wakatime'`).Scan(&legacy); err == nil {
				if err := host.StateSet(h.DB(), "wakatime", "state", legacy); err != nil {
					return nil, err
				}
			} else if err != nil && err.Error() != "sql: no rows in result set" {
				return nil, err
			}
		}
	}
	return p, nil
}

func (p *Plugin) Name() string  { return "wakatime" }
func (p *Plugin) Enabled() bool { return p.enabled }

// Validate 实现三态语义：启用但未配置 token → 拒绝启动。
func (p *Plugin) Validate() error {
	if p.token == "" {
		return fmt.Errorf("wakatime 插件已启用但未配置 token（wakatime.com/oauth 生成）")
	}
	return nil
}

// Run 周期拉取编码时长；拉取失败保留旧值，等下一拍重试。ctx 结束返回。
func (p *Plugin) Run(ctx context.Context) {
	client := &http.Client{Timeout: 10 * time.Second}
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		if err := p.poll(ctx, client, apiBaseURL); err != nil {
			p.h.Logger().Warn("WakaTime 拉取失败，保留旧值", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Plugin) poll(ctx context.Context, client *http.Client, base string) error {
	cv, err := fetch(ctx, client, base, p.token)
	if err != nil {
		return err
	}
	cv.AsOf = host.FormatTime(p.h.Now())
	raw, err := json.Marshal(cv)
	if err != nil {
		return err
	}
	return host.StateSet(p.h.DB(), "wakatime", "state", string(raw))
}

func fetch(ctx context.Context, client *http.Client, base, token string) (CodingV, error) {
	req := func(path string, out any) error {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return err
		}
		r.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(r)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}

	var summaries struct {
		Data []struct {
			GrandTotal struct {
				TotalSeconds float64 `json:"total_seconds"`
			} `json:"grand_total"`
		} `json:"data"`
	}
	if err := req("summaries?range=Today", &summaries); err != nil {
		return CodingV{}, err
	}
	if len(summaries.Data) < 1 {
		return CodingV{}, fmt.Errorf("summaries 返回空数据")
	}
	today := summaries.Data[0].GrandTotal.TotalSeconds

	var allTime struct {
		Data struct {
			TotalSeconds float64 `json:"total_seconds"`
		} `json:"data"`
	}
	if err := req("all_time_since_today", &allTime); err != nil {
		return CodingV{}, err
	}
	return CodingV{TodaySeconds: &today, TotalSeconds: allTime.Data.TotalSeconds}, nil
}

// SnapshotKey 贡献顶层 "coding" 节。
func (p *Plugin) SnapshotKey() string { return "coding" }

// ContributeSnapshot 读时重算 24h 新鲜度守卫：as_of 超过一天说明轮询长期
// 失败，今日口径失真（昨天的余数），只保留仍然真实的累计。
func (p *Plugin) ContributeSnapshot(q host.Queryer, now time.Time) (json.RawMessage, error) {
	v, ok, err := host.StateGet(q, "wakatime", "state")
	if err != nil || !ok {
		return nil, err
	}
	var cv CodingV
	if json.Unmarshal([]byte(v), &cv) != nil {
		return nil, nil
	}
	if cv.TotalSeconds <= 0 && (cv.TodaySeconds == nil || *cv.TodaySeconds <= 0) {
		return nil, nil
	}
	if asOf, err := host.ParseTime(cv.AsOf); err != nil || now.Sub(asOf) > freshCutoff {
		cv.TodaySeconds = nil
	}
	return json.Marshal(cv)
}

// Describe 实现清单：字段交给状态页通用渲染（today_seconds 缺席 = 只看累计）。
func (p *Plugin) Describe() host.Descriptor {
	return host.Descriptor{
		Title: "WakaTime",
		Icon:  "coding",
		Fields: []host.Field{
			{Key: "today_seconds", Format: "duration"},
			{Key: "total_seconds", Label: "累计", Format: "duration_total"},
		},
	}
}

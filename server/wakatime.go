package server

// WakaTime 数据源：服务端定期拉取官方 API 的今日与累计编码时长，
// 结果存入 server_state（键 "wakatime"），Snapshot 原样带出。
// api_key 为空即停用；拉取失败保留旧值，等下一拍重试。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	wakatimeBaseURL     = "https://api.wakatime.com/api/v1/users/current/"
	wakatimePollEvery   = 5 * time.Minute
	wakatimeStateKey    = "wakatime"
	wakatimeFreshCutoff = 24 * time.Hour // as_of 超过此值视为今日口径失真
)

// CodingV 是编码时长的存储与公开形态（WakaTime 口径：编辑器活动时长）。
// TodaySeconds 为 nil 表示数据过旧、今日口径已下线（见 Snapshot 的新鲜度守卫）；
// 为 0 表示当天确实还没写代码。
type CodingV struct {
	TodaySeconds *float64 `json:"today_seconds,omitempty"`
	TotalSeconds float64  `json:"total_seconds"`
	AsOf         string   `json:"as_of"`
}

// RunWakaTime 周期拉取编码时长；未配置 api_key 时立即返回。ctx 结束返回。
func (e *Engine) RunWakaTime(ctx context.Context) {
	apiKey := e.rt.Cfg.WakaTime.APIKey
	if apiKey == "" {
		return
	}
	e.log.Info("WakaTime 数据源已启用")
	client := &http.Client{Timeout: 10 * time.Second}
	ticker := time.NewTicker(wakatimePollEvery)
	defer ticker.Stop()
	for {
		if err := e.pollWakaTime(ctx, client, wakatimeBaseURL, apiKey); err != nil {
			e.log.Warn("WakaTime 拉取失败，保留旧值", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (e *Engine) pollWakaTime(ctx context.Context, client *http.Client, base, apiKey string) error {
	cv, err := fetchWakaTime(ctx, client, base, apiKey)
	if err != nil {
		return err
	}
	cv.AsOf = formatTime(e.now())
	raw, err := json.Marshal(cv)
	if err != nil {
		return err
	}
	_, err = e.db.Exec(`INSERT INTO server_state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, wakatimeStateKey, string(raw))
	return err
}

func fetchWakaTime(ctx context.Context, client *http.Client, base, apiKey string) (CodingV, error) {
	// Basic auth 值是 API key 原文的 base64（WakaTime 约定，非 user:pass）。
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(apiKey))
	req := func(path string, out any) error {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return err
		}
		r.Header.Set("Authorization", auth)
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

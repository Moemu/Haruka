package server

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是 haruka-server.yml 的原始形态。
type Config struct {
	Listen string `yaml:"listen"`
	DBPath string `yaml:"db"`

	TrustedProxies []string `yaml:"trusted_proxies"`
	CORSOrigins    []string `yaml:"cors_origins"`

	RateLimit struct {
		RPM   int `yaml:"rpm"`
		Burst int `yaml:"burst"`
	} `yaml:"rate_limit"`

	// LogFile 为空输出 stdout（Docker 推荐）；填写后按 10MB 轮转、保留 1 个旧文件。
	LogFile string `yaml:"log_file"`

	FreshnessLag    string `yaml:"freshness_lag"`
	FutureTolerance string `yaml:"future_tolerance"`
	ClockSkewWarn   string `yaml:"clock_skew_warn"`

	HeartbeatDefaultInterval string `yaml:"heartbeat_default_interval"`

	Retention struct {
		Events string `yaml:"events"`
	} `yaml:"retention"`

	OnHide string `yaml:"on_hide"` // discard | keep

	Aggregation struct {
		LocationSourcePriority []string `yaml:"location_source_priority"`
	} `yaml:"aggregation"`

	// LocationLookupDB 是方案 A 的离线解析库（ip2region.xdb）路径；
	// 空表示禁用派生（设备未上报位置时公开快照就没有位置字段）。
	LocationLookupDB string `yaml:"location_lookup_db"`

	// WakaTime 数据源：api_key 为空即停用（见 server/wakatime.go）。
	WakaTime struct {
		APIKey string `yaml:"api_key"`
	} `yaml:"wakatime"`
}

// Runtime 是补全默认值后的可运行配置。
type Runtime struct {
	Cfg Config

	Listen string
	DBPath string

	FreshnessLag    time.Duration
	FutureTolerance time.Duration
	ClockSkewWarn   time.Duration

	HeartbeatDefault time.Duration
	EventsRetention  time.Duration

	RateRPM   int
	RateBurst int

	LocationPriority []string
}

func defaultConfig() Config {
	var c Config
	c.Listen = "127.0.0.1:8080"
	c.DBPath = "haruka.db"
	c.TrustedProxies = nil
	c.RateLimit.RPM = 120
	c.RateLimit.Burst = 60
	c.FreshnessLag = "10m"
	c.FutureTolerance = "5m"
	c.ClockSkewWarn = "24h"
	c.HeartbeatDefaultInterval = "120s"
	c.Retention.Events = "168h"
	c.OnHide = "discard"
	c.Aggregation.LocationSourcePriority = []string{"android", "desktop"}
	return c
}

func LoadConfig(path string) (Runtime, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Runtime{}, fmt.Errorf("读取配置 %s: %w", path, err)
	}
	cfg := defaultConfig()
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Runtime{}, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	return ValidateConfig(cfg)
}

func ValidateConfig(cfg Config) (Runtime, error) {
	rt := Runtime{Cfg: cfg}
	if cfg.Listen == "" || cfg.DBPath == "" {
		return rt, fmt.Errorf("listen 与 db 不能为空")
	}
	var err error
	if rt.FreshnessLag, err = parseDur(cfg.FreshnessLag, "freshness_lag"); err != nil {
		return rt, err
	}
	if rt.FutureTolerance, err = parseDur(cfg.FutureTolerance, "future_tolerance"); err != nil {
		return rt, err
	}
	if rt.ClockSkewWarn, err = parseDur(cfg.ClockSkewWarn, "clock_skew_warn"); err != nil {
		return rt, err
	}
	if rt.HeartbeatDefault, err = parseDur(cfg.HeartbeatDefaultInterval, "heartbeat_default_interval"); err != nil {
		return rt, err
	}
	if rt.EventsRetention, err = parseDur(cfg.Retention.Events, "retention.events"); err != nil {
		return rt, err
	}
	if rt.EventsRetention <= 0 {
		rt.EventsRetention = 1 << 62 // 用户显式要求长保留时不淘汰
	}
	switch cfg.OnHide {
	case "discard", "keep":
	default:
		return rt, fmt.Errorf("on_hide 只能是 discard 或 keep")
	}
	if cfg.RateLimit.RPM < 0 {
		return rt, fmt.Errorf("rate_limit.rpm 不能为负")
	}
	rt.RateRPM = cfg.RateLimit.RPM
	rt.RateBurst = cfg.RateLimit.Burst
	if rt.RateRPM > 0 && rt.RateBurst <= 0 {
		rt.RateBurst = 60
	}
	rt.Listen = cfg.Listen
	rt.DBPath = cfg.DBPath
	rt.LocationPriority = cfg.Aggregation.LocationSourcePriority
	if len(rt.LocationPriority) == 0 {
		rt.LocationPriority = []string{"android", "desktop"}
	}
	return rt, nil
}

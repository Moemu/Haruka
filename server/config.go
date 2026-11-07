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
}

// reservedConfigKeys 是核心配置键（保留名黑名单）：插件（无论编译型还是
// 声明型）不得占用同名配置节，装载时这些节归核心。与 Config 的 yaml 标签
// 保持一致——新增核心字段时必须同步此表。
var reservedConfigKeys = map[string]bool{
	"listen": true, "db": true, "trusted_proxies": true, "cors_origins": true,
	"rate_limit": true, "log_file": true, "freshness_lag": true,
	"future_tolerance": true, "clock_skew_warn": true, "heartbeat_default_interval": true,
	"retention": true, "on_hide": true, "aggregation": true, "location_lookup_db": true,
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

	// PluginSections 是 yml 顶层里归插件命名空间的节（非保留名）：
	// 键 = 插件名，值 = 该节原始节点。装载路径唯一——核心不解释节内容，
	// 编译型插件经 Host.Config 自取同名节，无同名编译插件的节由声明型
	// 插件构造器接管（见 plugins.go 的 InitPlugins）。
	PluginSections map[string]*yaml.Node
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
	rt, err := ValidateConfig(cfg)
	if err != nil {
		return rt, err
	}
	// 插件命名空间节：单一装载路径——核心不解释内容，只按名字分派。
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return rt, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	if len(doc.Content) > 0 {
		for i := 0; i+1 < len(doc.Content[0].Content); i += 2 {
			key := doc.Content[0].Content[i].Value
			if reservedConfigKeys[key] {
				continue
			}
			if rt.PluginSections == nil {
				rt.PluginSections = map[string]*yaml.Node{}
			}
			rt.PluginSections[key] = doc.Content[0].Content[i+1]
		}
	}
	return rt, nil
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

package agent

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是 haruka-agent.yml 的原始形态（字符串字段便于 YAML 表达时长）。
type Config struct {
	Server     string `yaml:"server"`
	Token      string `yaml:"token"`
	DeviceID   string `yaml:"device_id"`
	DeviceKind string `yaml:"device_kind"`

	HeartbeatInterval string `yaml:"heartbeat_interval"`
	WindowInterval    string `yaml:"window_interval"`
	LocationInterval  string `yaml:"location_interval"`
	FlushInterval     string `yaml:"flush_interval"`
	BatchSize         int    `yaml:"batch_size"`

	Collectors struct {
		Presence bool `yaml:"presence"`
		Activity bool `yaml:"activity"`
		Location bool `yaml:"location"`
	} `yaml:"collectors"`

	Location struct {
		Mode     string `yaml:"mode"` // server | local | off
		LocalDB  string `yaml:"local_db"`
		Override string `yaml:"override"`
	} `yaml:"location"`

	Privacy struct {
		AwayThreshold string `yaml:"away_threshold"`
		HomeDistrict  string `yaml:"home_district"`
		FileType      bool   `yaml:"file_type"`
	} `yaml:"privacy"`

	Activity struct {
		Aliases     map[string]string  `yaml:"aliases"`
		Unmapped    string             `yaml:"unmapped"` // hidden | hash | null
		PrivateApps []string           `yaml:"private_apps"`
		AppMap      map[string]AppMeta `yaml:"app_map"`
	} `yaml:"activity"`

	Spool struct {
		MaxAge  string `yaml:"max_age"`
		MaxSize string `yaml:"max_size"`
	} `yaml:"spool"`
}

// Runtime 是校验并补全默认值后的可运行配置。
type Runtime struct {
	Cfg Config

	ServerURL   string
	Heartbeat   time.Duration
	WindowTick  time.Duration
	LocationInt time.Duration
	Flush       time.Duration
	BatchSize   int
	AwayAfter   time.Duration
	SpoolMaxAge time.Duration
	SpoolMaxMax int64
}

func defaultConfig() Config {
	var c Config
	c.DeviceKind = "desktop"
	c.HeartbeatInterval = "60s"
	c.WindowInterval = "30s"
	c.LocationInterval = "30m"
	c.FlushInterval = "30s"
	c.BatchSize = 20
	c.Collectors.Presence = true
	c.Collectors.Activity = true
	c.Collectors.Location = false
	c.Location.Mode = "server"
	c.Privacy.AwayThreshold = "5m"
	c.Activity.Unmapped = "hidden"
	c.Spool.MaxAge = "168h"
	c.Spool.MaxSize = "10MB"
	return c
}

// LoadConfig 读取 YAML 并做校验，返回 Runtime。
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

	cfg.Server = strings.TrimRight(strings.TrimSpace(cfg.Server), "/")
	if cfg.Server == "" {
		return rt, fmt.Errorf("server 不能为空")
	}
	if !strings.HasPrefix(cfg.Server, "https://") && !strings.HasPrefix(cfg.Server, "http://") {
		return rt, fmt.Errorf("server 必须以 https:// 或 http:// 开头（本地调试可用 http）")
	}
	u, err := url.Parse(cfg.Server)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return rt, fmt.Errorf("server 必须是无用户名、查询参数和片段的有效 URL")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return rt, fmt.Errorf("非回环地址的 server 必须使用 HTTPS")
		}
	}
	rt.ServerURL = cfg.Server

	if cfg.Token == "" {
		return rt, fmt.Errorf("token 不能为空")
	}
	if cfg.DeviceID == "" {
		return rt, fmt.Errorf("device_id 不能为空")
	}
	if cfg.DeviceKind != "desktop" && cfg.DeviceKind != "android" {
		return rt, fmt.Errorf("device_kind 只能是 desktop 或 android")
	}

	if rt.Heartbeat, err = parseDur(cfg.HeartbeatInterval, "heartbeat_interval"); err != nil {
		return rt, err
	}
	if rt.WindowTick, err = parseDur(cfg.WindowInterval, "window_interval"); err != nil {
		return rt, err
	}
	if rt.LocationInt, err = parseDur(cfg.LocationInterval, "location_interval"); err != nil {
		return rt, err
	}
	if rt.Flush, err = parseDur(cfg.FlushInterval, "flush_interval"); err != nil {
		return rt, err
	}
	if rt.AwayAfter, err = parseDur(cfg.Privacy.AwayThreshold, "privacy.away_threshold"); err != nil {
		return rt, err
	}
	if rt.SpoolMaxAge, err = parseDur(cfg.Spool.MaxAge, "spool.max_age"); err != nil {
		return rt, err
	}
	if rt.SpoolMaxMax, err = parseSize(cfg.Spool.MaxSize, "spool.max_size"); err != nil {
		return rt, err
	}

	if cfg.BatchSize <= 0 {
		rt.BatchSize = 20
	} else {
		rt.BatchSize = cfg.BatchSize
	}
	if rt.BatchSize > 100 {
		rt.BatchSize = 100 // 协议单包上限 100 事件（设计案 5.1 节）
	}

	switch cfg.Location.Mode {
	case "server", "local", "off":
	default:
		return rt, fmt.Errorf("location.mode 只能是 server、local 或 off")
	}
	if cfg.Location.Override != "" || cfg.Privacy.HomeDistrict != "" {
		return rt, fmt.Errorf("location.override 和 privacy.home_district 尚未实现，请留空")
	}
	switch cfg.Activity.Unmapped {
	case "hidden", "hash", "null":
	default:
		return rt, fmt.Errorf("activity.unmapped 只能是 hidden、hash 或 null")
	}
	return rt, nil
}

func parseDur(s, field string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("%s 不能为空", field)
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s 不是合法的正时长: %q", field, s)
	}
	return d, nil
}

func parseSize(s, field string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return 0, fmt.Errorf("%s 不能为空", field)
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "KB"):
		mult, s = 1<<10, strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "MB"):
		mult, s = 1<<20, strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "GB"):
		mult, s = 1<<30, strings.TrimSuffix(s, "GB")
	}
	var n int64
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("%s 不是合法的容量: %q", field, s)
	}
	return n * mult, nil
}

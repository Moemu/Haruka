package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateConfigDefaults(t *testing.T) {
	cfg := defaultConfig()
	cfg.Server = "https://haruka.example.com/"
	cfg.Token = "tok"
	cfg.DeviceID = "d_test01"
	rt, err := ValidateConfig(cfg)
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	if rt.ServerURL != "https://haruka.example.com" {
		t.Fatalf("末尾斜杠应去除: %q", rt.ServerURL)
	}
	if rt.Heartbeat != time.Minute || rt.WindowTick != 30*time.Second {
		t.Fatalf("默认间隔错误: %v %v", rt.Heartbeat, rt.WindowTick)
	}
	if rt.SpoolMaxAge != 7*24*time.Hour || rt.SpoolMaxMax != 10<<20 {
		t.Fatalf("默认 spool 保留策略错误: %v %v", rt.SpoolMaxAge, rt.SpoolMaxMax)
	}
}

func TestValidateConfigRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"缺 server", func(c *Config) { c.Server = "" }, "server"},
		{"server 协议", func(c *Config) { c.Server = "ftp://x" }, "server"},
		{"公网明文", func(c *Config) { c.Server = "http://example.com" }, "HTTPS"},
		{"URL 凭据", func(c *Config) { c.Server = "https://user:pass@example.com" }, "URL"},
		{"URL 查询", func(c *Config) { c.Server = "https://example.com?token=test" }, "URL"},
		{"在家模式要求 local", func(c *Config) { c.Privacy.HomeDistrict = "测试区" }, "仅支持 location.mode: local"},
		{"覆盖要求 local", func(c *Config) { c.Location.Override = "上海" }, "仅支持 location.mode: local"},
		{"覆盖含 URL", func(c *Config) {
			c.Location.Mode = "local"
			c.Location.Override = "https://x.example.com"
		}, "禁止包含 URL"},
		{"覆盖过长", func(c *Config) {
			c.Location.Mode = "local"
			c.Location.Override = strings.Repeat("市", 65)
		}, "过长"},
		{"缺 token", func(c *Config) { c.Token = "" }, "token"},
		{"缺 device_id", func(c *Config) { c.DeviceID = "" }, "device_id"},
		{"location 模式", func(c *Config) { c.Location.Mode = "auto" }, "location.mode"},
		{"非法时长", func(c *Config) { c.HeartbeatInterval = "soon" }, "heartbeat_interval"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Server = "https://h.example.com"
			cfg.Token = "t"
			cfg.DeviceID = "d_x"
			tc.mutate(&cfg)
			_, err := ValidateConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望错误包含 %q，得到 %v", tc.want, err)
			}
		})
	}
}

func TestValidateConfigOverrideTrims(t *testing.T) {
	cfg := defaultConfig()
	cfg.Server = "https://h.example.com"
	cfg.Token = "t"
	cfg.DeviceID = "d_x"
	cfg.Location.Mode = "local"
	cfg.Collectors.Location = true
	cfg.Location.Override = " 杭州市 "
	cfg.Privacy.HomeDistrict = " 杭州市 "
	rt, err := ValidateConfig(cfg)
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	if rt.Cfg.Location.Override != "杭州市" || rt.Cfg.Privacy.HomeDistrict != "杭州市" {
		t.Fatalf("override/home_district 应去空白: %q %q", rt.Cfg.Location.Override, rt.Cfg.Privacy.HomeDistrict)
	}
}

func TestLoadConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yml")
	content := `
server: https://haruka.example.com
token: tok
device_id: d_test01
heartbeat_interval: 45s
collectors:
  presence: true
  activity: true
  location: false
location:
  mode: server
activity:
  aliases:
    "vscode:haruka": aurora
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if rt.Heartbeat != 45*time.Second {
		t.Fatalf("heartbeat = %v", rt.Heartbeat)
	}
	if rt.Cfg.Collectors.Location {
		t.Fatal("location 采集应为 false")
	}
}

func TestSeqMonotonicAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s1, err := OpenSeq(dir)
	if err != nil {
		t.Fatal(err)
	}
	s1.Next()
	s1.Next()
	s2, err := OpenSeq(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Next(); got != 3 {
		t.Fatalf("重开后序号应延续，得到 %d", got)
	}
}

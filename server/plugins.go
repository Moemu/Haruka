package server

// 插件系统装载与运行（设计案 §7.7）：单一装载路径——编译型插件来自
// internal/host 注册表，声明型插件由本文件的通用构造器从配置节构造，
// 两者此后走完全相同的校验/运行/快照/清单路径。核心不含任何具体插件名。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"haruka/internal/host"
	"haruka/protocol"

	"gopkg.in/yaml.v3"
)

// engineHost 让 Engine 满足 host.Host（adapter 避免与 Engine 既有方法撞名）。
type engineHost struct{ e *Engine }

func (h engineHost) DB() *sql.DB                   { return h.e.db }
func (h engineHost) Logger() *slog.Logger          { return h.e.log }
func (h engineHost) Now() time.Time                { return h.e.now() }
func (h engineHost) Config(name string) *yaml.Node { return h.e.rt.PluginSections[name] }

// InitPlugins 构造全部插件（编译注册 + 配置节声明），执行启用插件的启动
// 校验，并收集事件订阅者。任何配置冲突/无效都返回错误、拒绝启动（三态
// 语义：启用但无效绝不静默降级为「事实上禁用」）。
func (e *Engine) InitPlugins() error {
	taken := map[string]bool{}
	for _, make := range host.Factories() {
		p, err := make(engineHost{e})
		if err != nil {
			return err
		}
		name := p.Name()
		if !host.ValidName(name) {
			return fmt.Errorf("插件名非法（须匹配 %s）: %q", host.NamePattern, name)
		}
		if reservedConfigKeys[name] {
			return fmt.Errorf("插件名 %q 与核心配置键冲突", name)
		}
		if taken[name] {
			return fmt.Errorf("插件名重复注册: %q", name)
		}
		taken[name] = true
		e.plugins = append(e.plugins, p)
	}
	// 声明型插件：顶层节有名字、无同名编译插件 → 接管（同一契约、同一节名）。
	names := make([]string, 0, len(e.rt.PluginSections))
	for name := range e.rt.PluginSections {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if taken[name] {
			continue
		}
		if !host.ValidName(name) {
			return fmt.Errorf("配置节 %q 不是合法插件名（须匹配 %s），且不是核心配置键", name, host.NamePattern)
		}
		p, err := newDeclaredPlugin(name, e.rt.PluginSections[name], engineHost{e})
		if err != nil {
			return err
		}
		taken[name] = true
		e.plugins = append(e.plugins, p)
	}
	sort.Slice(e.plugins, func(i, j int) bool { return e.plugins[i].Name() < e.plugins[j].Name() })

	for _, p := range e.plugins {
		if !p.Enabled() {
			e.log.Info("插件已停用", "plugin", p.Name())
			continue
		}
		if v, ok := p.(host.Validator); ok {
			if err := v.Validate(); err != nil {
				return fmt.Errorf("插件 %s 启动校验失败: %w", p.Name(), err)
			}
		}
		e.log.Info("插件已启用", "plugin", p.Name())
		if h, ok := p.(host.EventHandler); ok {
			e.eventHandlers = append(e.eventHandlers, h)
		}
	}
	return nil
}

// RunPlugins 启动所有已启用的拉取型插件；须在 InitPlugins 之后调用。
func (e *Engine) RunPlugins(ctx context.Context) {
	for _, p := range e.plugins {
		if r, ok := p.(host.Runner); ok && p.Enabled() {
			go r.Run(ctx)
		}
	}
}

// --- 声明型插件：零服务端代码的通用实现 ---

// declaredConfig 是声明型插件的配置节形态（无 source 字段：数据源隐式 =
// 同名 custom 事件）。
type declaredConfig struct {
	Enabled *bool        `yaml:"enabled"`
	Title   string       `yaml:"title"`
	Icon    string       `yaml:"icon"`
	Fields  []host.Field `yaml:"fields"`
}

// declaredPlugin 是声明型插件的通用实现：数据 = 同名 custom 事件的最新
// 观测（core 已通用入库 current_state("custom:"+name)），展示 = 配置声明。
type declaredPlugin struct {
	name    string
	cfg     declaredConfig
	host    host.Host
	enabled bool
}

// newDeclaredPlugin 从配置节构造声明型插件。enabled:false 的节容忍其余
// 字段为脏值（三态语义）；enabled 的节缺 title/fields 即拒绝启动。
func newDeclaredPlugin(name string, node *yaml.Node, h host.Host) (host.Plugin, error) {
	var cfg declaredConfig
	if node != nil {
		if err := node.Decode(&cfg); err != nil {
			return nil, fmt.Errorf("插件 %s 的配置节解析失败: %w", name, err)
		}
	}
	enabled := cfg.Enabled == nil || *cfg.Enabled
	p := &declaredPlugin{name: name, cfg: cfg, host: h, enabled: enabled}
	if enabled {
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("插件 %s 启动校验失败: %w", name, err)
		}
	}
	return p, nil
}

func (p *declaredPlugin) Name() string  { return p.name }
func (p *declaredPlugin) Enabled() bool { return p.enabled }

// Validate 实现三态语义中的「启用但无效 → 拒绝启动」。
func (p *declaredPlugin) Validate() error {
	if p.cfg.Title == "" {
		return fmt.Errorf("声明缺 title（既无同名编译插件，本节按声明型插件解释）")
	}
	if len(p.cfg.Fields) == 0 {
		return fmt.Errorf("声明缺 fields（至少一个展示字段）")
	}
	for _, f := range p.cfg.Fields {
		if f.Key == "" {
			return fmt.Errorf("声明 fields 含空 key")
		}
	}
	return nil
}

// Describe 实现清单：声明即元数据。
func (p *declaredPlugin) Describe() host.Descriptor {
	return host.Descriptor{Title: p.cfg.Title, Icon: p.cfg.Icon, Fields: p.cfg.Fields}
}

// SnapshotKey 贡献顶层节，键 = 插件名（身份统一规则）。
func (p *declaredPlugin) SnapshotKey() string { return p.name }

// ContributeSnapshot 透传同名 custom 事件的最新观测：payload 键平铺 + as_of。
func (p *declaredPlugin) ContributeSnapshot(q host.Queryer, now time.Time) (json.RawMessage, error) {
	var data, tsS, expires string
	err := q.QueryRow(`SELECT data, ts, expires_at FROM current_state WHERE type = ?
		ORDER BY ts DESC, seq DESC LIMIT 1`, "custom:"+p.name).Scan(&data, &tsS, &expires)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// expires_at 读时重判（写入方 TTL 之外再按当前时钟确认一次）。
	if exp, err := parseTime(expires); err != nil || exp.Before(now) {
		return nil, nil
	}
	var d protocol.CustomData
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		return nil, err
	}
	section := map[string]json.RawMessage{}
	if err := json.Unmarshal(d.Payload, &section); err != nil {
		return nil, err
	}
	section["as_of"] = json.RawMessage(`"` + tsS + `"`)
	return json.Marshal(section)
}

// Package persona 是人格插件：身份 = 服务端配置的封闭列表（谁在活动），
// 不是自由造词。切换 = 从列表选择，无限期生效，直到再次切换或清除；
// 清除回落默认人格。数据来自 custom:persona 事件（Android 人格入口推送），
// 快照贡献顶层 "persona" 节。
package persona

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"haruka/internal/display"
	"haruka/internal/host"
)

// config：list 是人格列表（顺序即展示序）；default 是无接管时的兜底人格
// （必须 ∈ list）。两者都只能改配置文件后重启生效。
type config struct {
	Enabled *bool    `yaml:"enabled"`
	List    []string `yaml:"list"`
	Default string   `yaml:"default"`
}

// payload 是 custom:persona 事件的 payload schema（Android 端镜像）。
// 无 until——人格是身份状态，不是限时情绪。
type payload struct {
	Persona string `json:"persona,omitempty"`
	Text    string `json:"text,omitempty"`
	Clear   bool   `json:"clear,omitempty"`
}

// state 是插件命名空间里的单行接管态。
type state struct {
	Persona   string `json:"persona,omitempty"`
	Text      string `json:"text,omitempty"`
	Updated   string `json:"updated,omitempty"`
	Cleared   bool   `json:"cleared,omitempty"`
	ClearedAt string `json:"cleared_at,omitempty"`
}

// Plugin 实现推送型数据源：EventHandler（custom:persona）+ Contributor
// （persona 节）+ 私有清单槽位 choices（Android 切换 chips 的数据源）。
type Plugin struct {
	h   host.Host
	cfg config
}

func init() { host.Register(New) }

// New 构造插件。人格需要配置才有意义：缺节 = 停用。
func New(h host.Host) (host.Plugin, error) {
	var cfg config
	node := h.Config("persona")
	if node != nil {
		if err := node.Decode(&cfg); err != nil {
			return nil, fmt.Errorf("persona 配置节解析失败: %w", err)
		}
	}
	return &Plugin{h: h, cfg: cfg}, nil
}

func (p *Plugin) Name() string { return "persona" }

// Enabled：缺节停用；写了节默认启用，enabled: false 显式停用。
func (p *Plugin) Enabled() bool {
	if p.h.Config("persona") == nil {
		return false
	}
	return p.cfg.Enabled == nil || *p.cfg.Enabled
}

// Validate 实现三态语义：启用但列表/默认人格无效 → 拒绝启动。
func (p *Plugin) Validate() error {
	if len(p.cfg.List) == 0 {
		return fmt.Errorf("persona 插件已启用但未配置 list（人格列表）")
	}
	seen := map[string]bool{}
	for _, item := range p.cfg.List {
		if item == "" {
			return fmt.Errorf("persona list 含空项")
		}
		if err := display.Check("persona.list", item, 16); err != nil {
			return err
		}
		if seen[item] {
			return fmt.Errorf("persona list 重复项: %s", item)
		}
		seen[item] = true
	}
	if p.cfg.Default != "" && !seen[p.cfg.Default] {
		return fmt.Errorf("persona default %q 不在 list 中", p.cfg.Default)
	}
	return nil
}

// Sources 订阅同名 custom 事件。
func (p *Plugin) Sources() []string { return []string{"persona"} }

// HandleEvent 实现单行接管态：latest-wins、同秒清除优先、清除写墓碑。
// persona ∈ 配置列表是封闭集校验——不在列表中的值拒收并反馈到端。
func (p *Plugin) HandleEvent(ev host.Delivered, q host.Queryer) error {
	var d payload
	dec := json.NewDecoder(bytes.NewReader(ev.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return fmt.Errorf("persona payload 校验失败: %v", err)
	}
	cur, _, err := host.StateGet(q, "persona", "state")
	if err != nil {
		return err
	}
	var st state
	json.Unmarshal([]byte(cur), &st)
	curTS, _ := host.ParseTime(st.Updated)

	if d.Clear {
		if ev.TS.After(curTS) || ev.TS.Equal(curTS) {
			st = state{Cleared: true, ClearedAt: host.FormatTime(ev.TS), Updated: host.FormatTime(ev.TS)}
		}
	} else {
		if d.Persona == "" {
			return fmt.Errorf("persona payload 缺 persona")
		}
		if !p.inList(d.Persona) {
			return fmt.Errorf("persona %q 不在服务端人格列表中", d.Persona)
		}
		if len([]rune(d.Text)) > 60 {
			return fmt.Errorf("persona payload text 超长")
		}
		if err := display.Check("text", d.Text, 60); err != nil {
			return err
		}
		if st.Cleared && st.ClearedAt != "" {
			if tomb, err := host.ParseTime(st.ClearedAt); err == nil && !ev.TS.After(tomb) {
				return nil
			}
		}
		if !ev.TS.After(curTS) {
			return nil
		}
		st = state{Persona: d.Persona, Text: d.Text, Updated: host.FormatTime(ev.TS)}
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return host.StateSet(q, "persona", "state", string(raw))
}

func (p *Plugin) inList(name string) bool {
	for _, item := range p.cfg.List {
		if item == name {
			return true
		}
	}
	return false
}

// SnapshotKey 贡献顶层 "persona" 节：接管优先；无接管回落默认人格
// （default: true 供前端标注「默认」）；两者皆无 → 节缺席。
func (p *Plugin) SnapshotKey() string { return "persona" }

func (p *Plugin) ContributeSnapshot(q host.Queryer, now time.Time) (json.RawMessage, error) {
	v, ok, err := host.StateGet(q, "persona", "state")
	if err != nil {
		return nil, err
	}
	if ok {
		var st state
		if json.Unmarshal([]byte(v), &st) == nil && !st.Cleared && st.Persona != "" {
			return json.Marshal(struct {
				Persona string `json:"persona"`
				Text    string `json:"text,omitempty"`
				AsOf    string `json:"as_of,omitempty"`
			}{st.Persona, st.Text, st.Updated})
		}
	}
	if p.cfg.Default == "" {
		return nil, nil
	}
	return json.Marshal(struct {
		Persona string `json:"persona"`
		Default bool   `json:"default"`
	}{p.cfg.Default, true})
}

// Describe 实现清单：choices 属私有槽位（人格名单不进公开响应），
// Android 携设备令牌获取后渲染切换 chips。
func (p *Plugin) Describe() host.Descriptor {
	return host.Descriptor{
		Title: "当前人格",
		Icon:  "persona",
		Fields: []host.Field{
			{Key: "persona"},
			{Key: "text", Label: "附言"},
		},
		Choices: p.cfg.List,
	}
}

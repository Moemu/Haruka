// Package mood 是情绪插件：固定六词的通用情绪（封闭种子集，不可配置）+
// 可选附言 + 限时（until，四档有效期）。数据来自 custom:mood 事件（Android
// 情绪入口推送），状态存 server_state 插件命名空间，快照贡献顶层 "mood" 节。
// 这是 manual 时代「手动状态」的回归本位——0.1.7 曾把人格词挤进同一槽位，
// 插件化后情绪与人格（persona 插件）彻底分道。
package mood

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"haruka/internal/display"
	"haruka/internal/host"
)

// Labels 是种子情绪词的中文映射（清单私有副本：状态页通用渲染按此取标签，
// 未知值原样展示）。固定集合——通用情绪无需配置。
var Labels = map[string]string{
	"happy":    "开心",
	"focused":  "专注",
	"tired":    "累了",
	"anxious":  "有点慌",
	"chilling": "放松中",
	"sick":     "不舒服",
}

// payload 是 custom:mood 事件的 payload schema（Android 端镜像）。
type payload struct {
	Mood  string `json:"mood,omitempty"`
	Text  string `json:"text,omitempty"`
	Until string `json:"until,omitempty"` // RFC3339；缺席 = 无限期
	Clear bool   `json:"clear,omitempty"`
}

// state 是插件命名空间里的单行接管态。
type state struct {
	Mood      string `json:"mood,omitempty"`
	Text      string `json:"text,omitempty"`
	Until     string `json:"until,omitempty"`
	Updated   string `json:"updated,omitempty"`
	Cleared   bool   `json:"cleared,omitempty"`
	ClearedAt string `json:"cleared_at,omitempty"`
}

// Plugin 实现推送型数据源：EventHandler（custom:mood）+ Contributor（mood 节）。
type Plugin struct {
	h       host.Host
	enabled bool
}

func init() { host.Register(New) }

// New 构造插件。mood 无必填配置：缺节默认启用（开箱即用）。
func New(h host.Host) (host.Plugin, error) {
	p := &Plugin{h: h, enabled: true}
	if node := h.Config("mood"); node != nil {
		var cfg struct {
			Enabled *bool `yaml:"enabled"`
		}
		if err := node.Decode(&cfg); err != nil {
			return nil, fmt.Errorf("mood 配置节解析失败: %w", err)
		}
		p.enabled = cfg.Enabled == nil || *cfg.Enabled
	}
	if p.enabled {
		p.migrateLegacy()
	}
	return p, nil
}

// migrateLegacy 把 manual 时代（0.1.7 服务端）的活跃手动状态播种进插件命名
// 空间；全新库没有 manual_state 表，一并容忍。
func (p *Plugin) migrateLegacy() {
	db := p.h.DB()
	if _, ok, _ := host.StateGet(db, "mood", "state"); ok {
		return
	}
	var exists int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'manual_state'`).Scan(&exists); err != nil || exists == 0 {
		return
	}
	var updated string
	var cleared bool
	var data sql.NullString
	if err := db.QueryRow(`SELECT updated_ts, cleared, data FROM manual_state WHERE id = 1`).Scan(&updated, &cleared, &data); err != nil {
		return
	}
	if cleared || !data.Valid {
		return
	}
	var d payload
	if json.Unmarshal([]byte(data.String), &d) != nil || d.Mood == "" {
		return
	}
	raw, _ := json.Marshal(state{Mood: d.Mood, Text: d.Text, Until: d.Until, Updated: updated})
	if err := host.StateSet(db, "mood", "state", string(raw)); err == nil {
		p.h.Logger().Info("已迁移 manual 时代的手动状态到 mood 插件", "mood", d.Mood)
	}
}

func (p *Plugin) Name() string  { return "mood" }
func (p *Plugin) Enabled() bool { return p.enabled }

// Sources 订阅同名 custom 事件。
func (p *Plugin) Sources() []string { return []string{"mood"} }

// HandleEvent 实现单行接管态：latest-wins、同秒清除优先、清除写墓碑。
// mood 语义校验在这里（核心只验信封）——校验错误 = 事件拒收并反馈到端。
func (p *Plugin) HandleEvent(ev host.Delivered, q host.Queryer) error {
	var d payload
	dec := json.NewDecoder(bytes.NewReader(ev.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return fmt.Errorf("mood payload 校验失败: %v", err)
	}
	cur, _, err := host.StateGet(q, "mood", "state")
	if err != nil {
		return err
	}
	var st state
	json.Unmarshal([]byte(cur), &st)
	curTS, _ := host.ParseTime(st.Updated)

	if d.Clear {
		// 同秒时清除优先：>=。
		if ev.TS.After(curTS) || ev.TS.Equal(curTS) {
			st = state{Cleared: true, ClearedAt: host.FormatTime(ev.TS), Updated: host.FormatTime(ev.TS)}
		}
	} else {
		if d.Mood == "" {
			return fmt.Errorf("mood payload 缺 mood")
		}
		if err := display.Check("mood", d.Mood, 16); err != nil {
			return err
		}
		if len([]rune(d.Text)) > 60 {
			return fmt.Errorf("mood payload text 超长")
		}
		if err := display.Check("text", d.Text, 60); err != nil {
			return err
		}
		if d.Until != "" {
			if _, err := host.ParseTime(d.Until); err != nil {
				return fmt.Errorf("mood payload until 非 RFC3339")
			}
		}
		// 墓碑：ts ≤ cleared_at 的设置不生效；同秒或更旧让位于已有状态。
		if st.Cleared && st.ClearedAt != "" {
			if tomb, err := host.ParseTime(st.ClearedAt); err == nil && !ev.TS.After(tomb) {
				return nil
			}
		}
		if !ev.TS.After(curTS) {
			return nil
		}
		st = state{Mood: d.Mood, Text: d.Text, Until: d.Until, Updated: host.FormatTime(ev.TS)}
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return host.StateSet(q, "mood", "state", string(raw))
}

// SnapshotKey 贡献顶层 "mood" 节（键形状与 0.1.7 兼容：mood/text/until）。
func (p *Plugin) SnapshotKey() string { return "mood" }

func (p *Plugin) ContributeSnapshot(q host.Queryer, now time.Time) (json.RawMessage, error) {
	v, ok, err := host.StateGet(q, "mood", "state")
	if err != nil || !ok {
		return nil, err
	}
	var st state
	if json.Unmarshal([]byte(v), &st) != nil {
		return nil, nil
	}
	if st.Cleared || st.Mood == "" {
		return nil, nil
	}
	if st.Until != "" {
		if u, err := host.ParseTime(st.Until); err != nil || !u.After(now) {
			return nil, nil // 限时期满，回到「一切如常」
		}
	}
	return json.Marshal(struct {
		Mood  string `json:"mood"`
		Text  string `json:"text,omitempty"`
		Until string `json:"until,omitempty"`
		AsOf  string `json:"as_of,omitempty"`
	}{st.Mood, st.Text, st.Until, st.Updated})
}

// Describe 实现清单：标签映射随清单下发，状态页零硬编码。
func (p *Plugin) Describe() host.Descriptor {
	return host.Descriptor{
		Title: "心情",
		Icon:  "mood",
		Fields: []host.Field{
			{Key: "mood", Labels: Labels},
			{Key: "text", Label: "附言"},
		},
	}
}

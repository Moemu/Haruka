package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"haruka/protocol"
)

// PublicStatus 是 GET /v1/public/status 的响应（设计案 5.4 节）。
type PublicStatus struct {
	Visibility  string     `json:"visibility"`
	HiddenUntil *string    `json:"hidden_until,omitempty"`
	Online      bool       `json:"online"`
	Presence    *PresenceV `json:"presence"`
	Location    *LocationV `json:"location"`
	Activity    *ActivityV `json:"activity"`
	Mood        *MoodV     `json:"mood"`
	Devices     []DeviceV  `json:"devices,omitempty"`
	GeneratedAt string     `json:"generated_at"`
}

type PresenceV struct {
	State string `json:"state"` // active | away | unknown | offline
	// AsOf：展示中状态的观测时间。offline 时取 last_active_at（而不是查询
	// 时刻——否则轮询会不断推进「最后活跃」），无历史观测时为空串。
	AsOf         string `json:"as_of"`
	LastActiveAt string `json:"last_active_at,omitempty"`
}

type LocationV struct {
	Display   string `json:"display"`
	Precision string `json:"precision"`
	AsOf      string `json:"as_of"`
}

type ActivityV struct {
	Display string `json:"display"`
	AsOf    string `json:"as_of"`
}

type MoodV struct {
	Mood  string `json:"mood"`
	Text  string `json:"text,omitempty"`
	Until string `json:"until"`
}

// DeviceV 是一台在线设备的自述状态；Battery 为空表示该设备没有上报电量
// （台式机不发 device 事件）。Name 是登记时指定的设备名。
type DeviceV struct {
	Name    string    `json:"name,omitempty"`
	Kind    string    `json:"kind"`
	Battery *BatteryV `json:"battery,omitempty"`
	Steps   int       `json:"steps,omitempty"`
}

type BatteryV struct {
	Level    int  `json:"level"`
	Charging bool `json:"charging,omitempty"`
}

// Snapshot 读时重算过期（设计案 7.1 节）：内存与缓存只是加速，
// 权威态在表里，服务器重启后首次读取同样正确。
func (e *Engine) Snapshot() (*PublicStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()

	tx, err := e.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	hidden, hiddenUntil, err := e.visibility(tx)
	if err != nil {
		return nil, err
	}
	if hidden {
		out := &PublicStatus{
			Visibility:  "hidden",
			GeneratedAt: formatTime(now),
		}
		if hiddenUntil != "" {
			out.HiddenUntil = &hiddenUntil
		}
		return out, nil
	}

	devices, err := e.deviceListTx(tx)
	if err != nil {
		return nil, err
	}

	// 每设备的在线状态与实时态。presence 行无论是否过期都读取——
	// last_active_at 需要「最后一次 active 观测」的历史；location 行同理
	// 过期后保留旧值并由页面标注时间（设计案 5.5 节）；activity 过期即清空。
	type devState struct {
		dev           Device
		online        bool
		presence      *stateRow
		presenceValid bool
		activity      *stateRow
		location      *stateRow
		device        *stateRow
	}
	states := make([]devState, 0, len(devices))
	for _, d := range devices {
		st := devState{dev: d}
		if hb, err := currentState(tx, d.ID, protocol.TypeHeartbeat); err != nil {
			return nil, err
		} else if hb != nil && hb.receivedAt.Add(3*d.Interval).After(now) {
			st.online = true
		}
		if row, err := currentState(tx, d.ID, protocol.TypePresence); err != nil {
			return nil, err
		} else if row != nil {
			st.presence = row
			st.presenceValid = row.ts.Add(observationTTL(d, protocol.TypePresence)).After(now)
		}
		if row, err := currentState(tx, d.ID, protocol.TypeActivity); err != nil {
			return nil, err
		} else if row != nil && row.ts.Add(observationTTL(d, protocol.TypeActivity)).After(now) {
			st.activity = row
		}
		if row, err := currentState(tx, d.ID, protocol.TypeDevice); err != nil {
			return nil, err
		} else if row != nil && row.ts.Add(observationTTL(d, protocol.TypeDevice)).After(now) {
			st.device = row
		}
		if d.LocationPolicy != "off" { // off 设备的位置一律排除（设计案 6.3 节）
			if row, err := currentState(tx, d.ID, protocol.TypeLocation); err != nil {
				return nil, err
			} else if row != nil {
				st.location = row // 过期保留旧值，页面按 as_of 标注「x 小时前」
			}
		}
		states = append(states, st)
	}

	out := &PublicStatus{Visibility: "visible", GeneratedAt: formatTime(now)}

	// presence 聚合（设计案 7.5 节）：有效设备中任一 active → active；
	// 否则任一 away → away；无在线设备 → offline；在线但无有效 presence → unknown。
	// last_active_at 记录最近一次 active 观测（无论是否过期、设备是否在线）。
	var presenceAsOf time.Time
	var lastActive time.Time
	anyOnline := false
	anyValid := false
	state := "offline"
	for _, st := range states {
		if st.presence != nil {
			var pdActive protocol.PresenceData
			if json.Unmarshal([]byte(st.presence.data), &pdActive) == nil && pdActive.State == protocol.StateActive {
				if st.presence.ts.After(lastActive) {
					lastActive = st.presence.ts
				}
			}
		}
		if !st.online {
			continue
		}
		anyOnline = true
		if !st.presenceValid {
			continue
		}
		var pd protocol.PresenceData
		if json.Unmarshal([]byte(st.presence.data), &pd) != nil {
			continue
		}
		anyValid = true
		if st.presence.ts.After(presenceAsOf) {
			presenceAsOf = st.presence.ts
		}
		switch {
		case pd.State == protocol.StateActive:
			state = "active"
		case pd.State == protocol.StateAway && state != "active":
			state = "away"
		}
	}
	if !anyOnline {
		state = "offline"
	} else if !anyValid {
		state = "unknown"
	}
	out.Online = anyOnline && state != "offline"
	pv := &PresenceV{State: state}
	switch {
	case anyValid:
		pv.AsOf = formatTime(presenceAsOf)
	case !lastActive.IsZero():
		// offline/unknown 且有历史 active 观测：as_of 指向该观测，
		// 页面据此展示「最后活跃于 x 小时前」而不是随轮询推进（P2-10）。
		pv.AsOf = formatTime(lastActive)
	default:
		pv.AsOf = "" // 无历史观测：返回未知，不填当前时间
	}
	if !lastActive.IsZero() {
		pv.LastActiveAt = formatTime(lastActive)
	}
	out.Presence = pv

	// activity 资格（设计案 7.5 节）按「activity 来源设备」判定，不看聚合后
	// 的 unknown——unknown 同时混有「不支持」「过期」「采集故障」，直接放行
	// 会削弱隐藏规则：
	//   在线 + presence 有效且 active → 展示（常规描述）
	//   在线 + 设备明确上报 presence 不支持 → 展示（中性描述「前台应用：…」，
	//     前台窗口被采样不等于用户在操作，不推断「正在写代码」）
	//   away / presence 过期（采集故障）/ 离线 / activity 过期 → 隐藏
	var bestAct *stateRow
	var bestNeutral bool
	for _, st := range states {
		if !st.online || st.activity == nil {
			continue
		}
		stIsActive, stAway := false, false
		if st.presenceValid {
			var pd protocol.PresenceData
			if json.Unmarshal([]byte(st.presence.data), &pd) == nil {
				stIsActive = pd.State == protocol.StateActive
				stAway = pd.State == protocol.StateAway
			}
		}
		neutral := false
		switch {
		case stIsActive:
			// 常规展示（设备明确 active 优先于「不支持」，描述不带保留）
		case stAway:
			continue // 明确离开：隐藏
		case st.presence != nil:
			continue // 已有 presence 过期或无效：不能借能力上报绕过隐藏
		case st.dev.PresenceCapability == "unsupported" && st.dev.PresenceCapabilityAt.Add(3*st.dev.Interval).After(now):
			neutral = true // 明确不支持：中性展示
		default:
			continue // presence 过期或未上报能力：按采集故障处理，隐藏
		}
		if bestAct == nil || st.activity.ts.After(bestAct.ts) {
			bestAct = st.activity
			bestNeutral = neutral
		}
	}
	if bestAct != nil {
		display := activityDisplay(bestAct.data, bestNeutral)
		if display != "" {
			out.Activity = &ActivityV{Display: display, AsOf: formatTime(bestAct.ts)}
		}
	}

	// location：按配置优先级取第一个「设备在线且位置有效」的来源，全空取最新。
	pick := func(kind string) *devState {
		for i := range states {
			st := &states[i]
			if st.online && st.location != nil && st.dev.Kind == kind {
				return st
			}
		}
		return nil
	}
	var locRow *stateRow
	for _, kind := range e.rt.LocationPriority {
		if st := pick(kind); st != nil {
			locRow = st.location
			break
		}
	}
	if locRow == nil {
		for i := range states {
			st := &states[i]
			if st.location != nil && (locRow == nil || st.location.ts.After(locRow.ts)) {
				locRow = st.location
			}
		}
	}
	if locRow != nil {
		var ld protocol.LocationData
		if json.Unmarshal([]byte(locRow.data), &ld) == nil {
			out.Location = &LocationV{
				Display:   locationDisplay(ld),
				Precision: ld.Precision,
				AsOf:      formatTime(locRow.ts),
			}
		}
	}

	// devices：在线设备的自述状态。没有电量数据的设备（台式机不发 device 事件）
	// 不进列表——列表的意义就是电量这类「设备侧状态」。
	var devStatuses []DeviceV
	for _, st := range states {
		if !st.online || st.device == nil {
			continue
		}
		var dd protocol.DeviceData
		if json.Unmarshal([]byte(st.device.data), &dd) != nil || dd.BatteryLevel <= 0 {
			continue
		}
		sv := DeviceV{
			Name:    st.dev.Name,
			Kind:    st.dev.Kind,
			Battery: &BatteryV{Level: dd.BatteryLevel, Charging: dd.Charging},
		}
		if dd.Steps > 0 {
			sv.Steps = dd.Steps
		}
		devStatuses = append(devStatuses, sv)
	}
	out.Devices = devStatuses

	// manual：全局单条，未清除且未过期。
	var updatedTS, data sql.NullString
	var cleared bool
	var until sql.NullString
	err = tx.QueryRow(`SELECT updated_ts, cleared, data, until FROM manual_state WHERE id = 1`).
		Scan(&updatedTS, &cleared, &data, &until)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if !cleared && data.Valid && until.Valid {
		if u, err := parseTime(until.String); err == nil && u.After(now) {
			var md protocol.ManualData
			if json.Unmarshal([]byte(data.String), &md) == nil {
				out.Mood = &MoodV{Mood: md.Mood, Text: md.Text, Until: formatTime(u)}
			}
		}
	}
	return out, nil
}

// activityDisplay 把 activity 事件渲染为展示文案。neutral 为真时只陈述
// 「前台应用」事实（来源设备 presence 明确不支持，无法确认用户在操作）；
// 返回空串表示不展示。动词按类别选择（media 刷、music 听、coding 写代码…）。
func activityDisplay(dataJSON string, neutral bool) string {
	var d protocol.ActivityData
	if json.Unmarshal([]byte(dataJSON), &d) != nil {
		return ""
	}
	if neutral {
		switch {
		case d.Kind == "private":
			return "前台应用：私密应用"
		case d.App != "":
			return "前台应用：" + d.App
		case d.Kind == "browser":
			return "前台应用：浏览器"
		}
		return ""
	}
	if d.Kind == "private" {
		return "使用私密应用"
	}
	if d.Kind == "idle" {
		return "挂机中"
	}
	if d.App == "" {
		if d.Kind == "browser" {
			return "正在浏览网页"
		}
		return ""
	}
	switch d.Category {
	case "coding":
		return fmt.Sprintf("正在用 %s 写代码", d.App)
	case "browsing":
		return fmt.Sprintf("正在用 %s 网上冲浪", d.App)
	case "writing":
		return fmt.Sprintf("正在用 %s 写东西", d.App)
	case "design":
		return fmt.Sprintf("正在用 %s 画画", d.App)
	case "media":
		return fmt.Sprintf("正在刷 %s", d.App)
	case "music":
		return fmt.Sprintf("正在听 %s", d.App)
	}
	return fmt.Sprintf("正在使用 %s", d.App)
}

func locationDisplay(ld protocol.LocationData) string {
	if ld.Zone == protocol.ZoneHome {
		return "在家"
	}
	switch ld.Precision {
	case protocol.PrecisionDistrict:
		return ld.City + " · " + ld.District
	case protocol.PrecisionCity:
		return ld.City
	case protocol.PrecisionProvince:
		return ld.Province
	case protocol.PrecisionCountry:
		return ld.Country
	}
	return ""
}

func (e *Engine) deviceListTx(tx *sql.Tx) ([]Device, error) {
	rows, err := tx.Query(`SELECT ` + deviceColumns + ` FROM devices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		if !d.Revoked {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

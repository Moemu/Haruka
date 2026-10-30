package server

import (
	"bytes"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"haruka/protocol"
)

// 本文件实现设计案 5.3 节的事件分类规则与 5.2 节的字段校验。
// 校验是严格的：未知字段、坐标字段、URL/坐标模式的字符串一律拒绝。

var validMoods = map[string]bool{
	"happy": true, "focused": true, "tired": true,
	"anxious": true, "chilling": true, "sick": true,
}

var (
	errAuth       = fmt.Errorf("鉴权失败")
	errForbidden  = fmt.Errorf("设备不匹配")
	errValidation = fmt.Errorf("事件校验失败")
)

// ValidateEnvelope 严格解码请求体：信封与每个事件负载都拒绝未知字段。
func ValidateEnvelope(body []byte) (*protocol.Envelope, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var env protocol.Envelope
	if err := dec.Decode(&env); err != nil {
		return nil, fmt.Errorf("%w: %v", errValidation, err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("%w: 请求体只能包含一个 JSON 对象", errValidation)
	}
	if env.V != protocol.ProtocolVersion {
		return nil, fmt.Errorf("%w: 不支持的协议版本 %d", errValidation, env.V)
	}
	if !deviceIDPattern.MatchString(env.DeviceID) || (env.DeviceKind != protocol.KindDesktop && env.DeviceKind != protocol.KindAndroid) {
		return nil, fmt.Errorf("%w: device_id 或 device_kind 非法", errValidation)
	}
	if len(env.Events) < 1 || len(env.Events) > 100 {
		return nil, fmt.Errorf("%w: events 数量须在 1～100", errValidation)
	}
	if _, err := parseTime(env.SentAt); err != nil {
		return nil, fmt.Errorf("%w: sent_at 非法", errValidation)
	}
	seen := make(map[string]bool, len(env.Events))
	for i := range env.Events {
		ev := &env.Events[i]
		if err := validateEvent(ev); err != nil {
			return nil, fmt.Errorf("events[%d]: %w", i, err)
		}
		if err := canonicalEventData(ev); err != nil {
			return nil, err
		}
		if seen[ev.ID] {
			return nil, fmt.Errorf("%w: 重复事件 id %s", errValidation, ev.ID)
		}
		seen[ev.ID] = true
	}
	return &env, nil
}

// 只保留校验后的结构，避免重复 JSON 键的旧值进入历史。
func canonicalEventData(ev *protocol.Event) error {
	var data any
	switch ev.Type {
	case protocol.TypeHeartbeat:
		data = &protocol.HeartbeatData{}
	case protocol.TypePresence:
		data = &protocol.PresenceData{}
	case protocol.TypeActivity:
		data = &protocol.ActivityData{}
	case protocol.TypeLocation:
		data = &protocol.LocationData{}
	case protocol.TypeManual:
		data = &protocol.ManualData{}
	case protocol.TypeDevice:
		data = &protocol.DeviceData{}
	default:
		return errValidation
	}
	if err := json.Unmarshal(ev.Data, data); err != nil {
		return err
	}
	raw, err := json.Marshal(data)
	ev.Data = raw
	return err
}

func validateEvent(ev *protocol.Event) error {
	if ev.Seq > math.MaxInt64 {
		return fmt.Errorf("%w: seq 超出 SQLite 整数范围", errValidation)
	}
	if len(ev.ID) != 34 || !strings.HasPrefix(ev.ID, "e_") {
		return fmt.Errorf("%w: 事件 id 格式非法", errValidation)
	}
	for _, c := range ev.ID[2:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return fmt.Errorf("%w: 事件 id 必须小写十六进制", errValidation)
		}
	}
	ts, err := parseTime(ev.TS)
	if err != nil {
		return fmt.Errorf("%w: ts 非 RFC3339: %q", errValidation, ev.TS)
	}
	_ = ts
	switch ev.Type {
	case protocol.TypeHeartbeat:
		var d protocol.HeartbeatData
		return decodeStrict(ev.Data, &d)
	case protocol.TypePresence:
		var d protocol.PresenceData
		if err := decodeStrict(ev.Data, &d); err != nil {
			return err
		}
		if d.State != protocol.StateActive && d.State != protocol.StateAway {
			return fmt.Errorf("%w: presence.state 非法", errValidation)
		}
		if d.IdleMinutes < 0 || d.IdleMinutes > 7*24*60 {
			return fmt.Errorf("%w: idle_minutes 超界", errValidation)
		}
		return nil
	case protocol.TypeActivity:
		var d protocol.ActivityData
		if err := decodeStrict(ev.Data, &d); err != nil {
			return err
		}
		switch d.Kind {
		case "ide", "browser", "app", "private", "idle", "none":
		default:
			return fmt.Errorf("%w: activity.kind 非法", errValidation)
		}
		if len(d.App) > 64 {
			return fmt.Errorf("%w: app 过长", errValidation)
		}
		if err := checkDisplayString("app", d.App, 64); err != nil {
			return err
		}
		switch d.Category {
		case "coding", "writing", "browsing", "design", "meeting", "media", "music", "private", "other", "":
		default:
			return fmt.Errorf("%w: activity.category 非法", errValidation)
		}
		if d.FileType != "" {
			ok := len(d.FileType) >= 2 && len(d.FileType) <= 9 && d.FileType[0] == '.'
			if ok {
				for _, c := range d.FileType[1:] {
					if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
						ok = false
						break
					}
				}
			}
			if !ok {
				return fmt.Errorf("%w: file_type 非法", errValidation)
			}
		}
		return nil
	case protocol.TypeLocation:
		var d protocol.LocationData
		if err := decodeStrict(ev.Data, &d); err != nil {
			return err
		}
		if d.Source != "ip" && d.Source != "gps_coarse" {
			return fmt.Errorf("%w: location.source 非法", errValidation)
		}
		if d.Zone != "" && d.Zone != protocol.ZoneHome {
			return fmt.Errorf("%w: location.zone 非法", errValidation)
		}
		// precision 必须与实际非空层级一致（设计案 5.2 节）。
		switch d.Precision {
		case protocol.PrecisionDistrict:
			if d.District == "" {
				return fmt.Errorf("%w: precision=district 时 district 必填", errValidation)
			}
			if d.City == "" {
				return fmt.Errorf("%w: precision=district 时 city 必填", errValidation)
			}
		case protocol.PrecisionCity:
			if d.City == "" || d.District != "" {
				return fmt.Errorf("%w: precision=city 要求 city 非空且 district 为空", errValidation)
			}
		case protocol.PrecisionProvince:
			if d.Province == "" || d.City != "" || d.District != "" {
				return fmt.Errorf("%w: precision=province 要求 province 非空且市/区为空", errValidation)
			}
		case protocol.PrecisionCountry:
			if d.Country == "" || d.Province != "" || d.City != "" || d.District != "" {
				return fmt.Errorf("%w: precision=country 只允许 country", errValidation)
			}
		default:
			return fmt.Errorf("%w: location.precision 非法", errValidation)
		}
		for _, f := range []struct{ name, v string }{{"country", d.Country}, {"province", d.Province}, {"city", d.City}, {"district", d.District}} {
			if len(f.v) > 64 {
				return fmt.Errorf("%w: %s 过长", errValidation, f.name)
			}
			if err := checkDisplayString(f.name, f.v, 64); err != nil {
				return err
			}
		}
		return nil
	case protocol.TypeManual:
		var d protocol.ManualData
		if err := decodeStrict(ev.Data, &d); err != nil {
			return err
		}
		if d.Mood != "" && !validMoods[d.Mood] {
			return fmt.Errorf("%w: mood 非法", errValidation)
		}
		// 手动文本：码点 ≤60、字节 ≤240 兜底；坐标与 URL 模式拒收（设计案 11 节）。
		if len([]rune(d.Text)) > 60 || len(d.Text) > 240 {
			return fmt.Errorf("%w: text 超长", errValidation)
		}
		if err := checkDisplayString("text", d.Text, 60); err != nil {
			return err
		}
		if d.Until != "" {
			if _, err := parseTime(d.Until); err != nil {
				return fmt.Errorf("%w: until 非 RFC3339", errValidation)
			}
		}
		return nil
	case protocol.TypeDevice:
		var d protocol.DeviceData
		if err := decodeStrict(ev.Data, &d); err != nil {
			return err
		}
		if d.BatteryLevel < 0 || d.BatteryLevel > 100 {
			return fmt.Errorf("%w: battery_level 超界", errValidation)
		}
		return nil
	default:
		return fmt.Errorf("%w: 未知事件类型 %q", errValidation, ev.Type)
	}
}

func decodeStrict(raw []byte, out any) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("%w: data 必须为对象", errValidation)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%w: %v", errValidation, err)
	}
	return nil
}

// checkDisplayString 校验可展示字符串（project 与 manual.text 共用的底线规则）：
// 长度上限、禁止 URL 与坐标模式——合法字段里塞路径/坐标是真实风险（设计案 11 节）。
func checkDisplayString(field, v string, maxRunes int) error {
	if v == "" {
		return nil
	}
	if len([]rune(v)) > maxRunes {
		return fmt.Errorf("%w: %s 过长", errValidation, field)
	}
	if strings.Contains(v, "://") {
		return fmt.Errorf("%w: %s 禁止包含 URL", errValidation, field)
	}
	if isCoordPair(v) {
		return fmt.Errorf("%w: %s 疑似坐标", errValidation, field)
	}
	if strings.ContainsAny(v, `/\`) || ipv4Pattern.MatchString(v) {
		return fmt.Errorf("%w: %s 疑似路径或 IP", errValidation, field)
	}
	for _, token := range ipv6Pattern.FindAllString(v, -1) {
		if net.ParseIP(token) != nil {
			return fmt.Errorf("%w: %s 疑似 IP", errValidation, field)
		}
	}
	for _, c := range v {
		if unicode.IsControl(c) {
			return fmt.Errorf("%w: %s 含控制字符", errValidation, field)
		}
	}
	return nil
}

// isCoordPair 识别「31.19, 121.43」形态的经纬度对。
var ipv4Pattern = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
var ipv6Pattern = regexp.MustCompile(`(?:[0-9a-fA-F]{0,4}:){2,}[0-9a-fA-F:.]*`)
var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var coordinatePattern = regexp.MustCompile(`([-+]?\d{1,3}(?:\.\d+)?)\s*[,，]\s*([-+]?\d{1,3}(?:\.\d+)?)`)

func isCoordPair(s string) bool {
	for _, pair := range coordinatePattern.FindAllStringSubmatch(s, -1) {
		lat, _ := strconv.ParseFloat(pair[1], 64)
		lon, _ := strconv.ParseFloat(pair[2], 64)
		if lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 {
			return true
		}
	}
	return false
}

// --- ingest 规则应用 ---

// IngestEnvelope 处理一批已通过校验的事件。整包一个事务：部分失败全部回滚。
func (e *Engine) IngestEnvelope(device Device, clientIP net.IP, env *protocol.Envelope) (int, error) {
	if env.DeviceID != device.ID || env.DeviceKind != device.Kind {
		return 0, errForbidden
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	tx, err := e.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	receivedAt := e.now()

	// 定位策略在事务内重新读取：SetDevicePolicy 可能刚刚变更过，
	// 请求鉴权时拿到的设备对象可能已经过期（设计案 6.3 节）。
	policy, err := devicePolicyTx(tx, device.ID)
	if err != nil {
		return 0, err
	}

	// visibility 内部自愈「定时隐身到期」：到期即在当前事务内建立 resume_at。
	hidden, _, err := e.visibility(tx)
	if err != nil {
		return 0, err
	}
	if hidden {
		// 隐身期间（设计案 6.4 节）：
		//   discard：整包丢弃，不入库；
		//   keep：仅历史入库 + 心跳续活，任何事件都不得刷新实时态——
		//         否则 Show 后隐身期间的数据会直接公开。
		if e.rt.Cfg.OnHide == "discard" {
			return 0, tx.Commit()
		}
		for i := range env.Events {
			ev := &env.Events[i]
			res, err := tx.Exec(`INSERT OR IGNORE INTO events (id, device_id, type, ts, data, received_at, clock_skew)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				ev.ID, device.ID, ev.Type, ev.TS, string(ev.Data), formatTime(receivedAt), clockSkewFlag(ev.TS, receivedAt, e.rt))
			if err != nil {
				return 0, err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			if ev.Type == protocol.TypeHeartbeat {
				if err := applyHeartbeat(tx, device, receivedAt); err != nil {
					return 0, err
				}
			}
		}
		return len(env.Events), tx.Commit()
	}

	freshEvent := false
	for i := range env.Events {
		ev := &env.Events[i]
		res, err := tx.Exec(`INSERT OR IGNORE INTO events (id, device_id, type, ts, data, received_at, clock_skew)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			ev.ID, device.ID, ev.Type, ev.TS, string(ev.Data), formatTime(receivedAt), clockSkewFlag(ev.TS, receivedAt, e.rt))
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// 幂等：重复事件零副作用——重放的心跳不得复活在线窗口，
			// 重放的观测/手动状态不得覆盖实时态（设计案 11 节重放行）。
			continue
		}
		ts, _ := parseTime(ev.TS)
		resumeAt, err := e.resumeAt(tx)
		if err != nil {
			return 0, err
		}
		if ts.After(resumeAt) && receivedAt.Sub(ts) <= e.rt.FreshnessLag && ts.Sub(receivedAt) <= e.rt.FutureTolerance {
			freshEvent = true
		}
		switch ev.Type {
		case protocol.TypeHeartbeat:
			if err := applyHeartbeat(tx, device, receivedAt); err != nil {
				return 0, err
			}
		case protocol.TypeManual:
			if err := e.applyManual(tx, device, ev, receivedAt); err != nil {
				return 0, err
			}
		default: // presence / activity / device
			if ev.Type == protocol.TypeLocation && policy == "off" {
				// off 设备：位置事件仅入库，禁止刷新实时态（设计案 6.3 节，
				// 「关闭定位」由服务端强制执行，不依赖客户端自觉）。
				continue
			}
			if err := e.applyObservation(tx, device, ev, receivedAt); err != nil {
				return 0, err
			}
		}
	}

	// 方案 A：策略为 server 且设备未关闭位置时，由服务端解析连接 IP 并立即丢弃 IP。
	if freshEvent && policy == "server" && e.resolver != nil && clientIP != nil {
		if loc, ok := e.resolver.Resolve(clientIP.String()); ok {
			if err := upsertState(tx, device.ID, protocol.TypeLocation, loc, receivedAt, 0, receivedAt, receivedAt.Add(locationTTL(device))); err != nil {
				return 0, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(env.Events), nil
}

// devicePolicyTx 在事务内读取设备的最新定位策略。
func devicePolicyTx(tx *sql.Tx, deviceID string) (string, error) {
	var p string
	err := tx.QueryRow(`SELECT location_policy FROM devices WHERE id = ?`, deviceID).Scan(&p)
	if err == sql.ErrNoRows {
		return "server", nil
	}
	return p, err
}

func clockSkewFlag(ts string, receivedAt time.Time, rt *Runtime) int {
	t, err := parseTime(ts)
	if err != nil {
		return 1
	}
	if receivedAt.Sub(t) > rt.ClockSkewWarn || t.Sub(receivedAt) > rt.FutureTolerance {
		return 1
	}
	return 0
}

func applyHeartbeat(tx *sql.Tx, device Device, receivedAt time.Time) error {
	// 设备在线窗口 = 心跳间隔 × 3，基准是接收时间；事件自带的 ts 不参与。
	expires := receivedAt.Add(3 * device.Interval)
	return upsertState(tx, device.ID, protocol.TypeHeartbeat, protocol.HeartbeatData{}, receivedAt, 0, receivedAt, expires)
}

// applyObservation 实现设计案 5.3 节第 2 条：新鲜度窗口 + resume_at 边界 + 乱序保护。
func (e *Engine) applyObservation(tx *sql.Tx, device Device, ev *protocol.Event, receivedAt time.Time) error {
	ts, err := parseTime(ev.TS)
	if err != nil {
		return err
	}
	// 手动状态以外的观测事件才受新鲜度窗口约束。
	lag := receivedAt.Sub(ts)
	future := ts.Sub(receivedAt)
	if lag > e.rt.FreshnessLag || future > e.rt.FutureTolerance {
		e.log.Debug("历史补报只入库", "device", device.ID, "type", ev.Type, "lag", lag)
		return nil
	}

	resumeAt, err := e.resumeAt(tx)
	if err != nil {
		return err
	}
	if !ts.After(resumeAt) {
		// 隐身恢复边界：边界之前的观测一律不得刷新实时态（设计案 6.4 节）。
		e.log.Debug("恢复边界之前的观测只入库", "device", device.ID, "type", ev.Type, "ts", ev.TS)
		return nil
	}

	current, err := currentState(tx, device.ID, ev.Type)
	if err != nil {
		return err
	}
	if current != nil && !newer(ts, ev.Seq, current.ts, current.seq) {
		return nil
	}

	ttl := observationTTL(device, ev.Type)
	return upsertState(tx, device.ID, ev.Type, json.RawMessage(ev.Data), ts, ev.Seq, receivedAt, ts.Add(ttl))
}

// applyManual 实现设计案 5.3 节第 4 条：手动状态是用户操作，不受新鲜度窗口
// 限制，但受 resume_at 边界约束；全局 (ts, seq) 排序，同秒清除优先，清除写墓碑。
func (e *Engine) applyManual(tx *sql.Tx, device Device, ev *protocol.Event, receivedAt time.Time) error {
	ts, err := parseTime(ev.TS)
	if err != nil {
		return err
	}
	if ts.Sub(receivedAt) > e.rt.FutureTolerance {
		return nil
	}
	resumeAt, err := e.resumeAt(tx)
	if err != nil {
		return err
	}
	if !ts.After(resumeAt) {
		return nil
	}

	var data protocol.ManualData
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return err
	}

	var updatedTS string
	var cleared bool
	var clearedTS sql.NullString
	err = tx.QueryRow(`SELECT updated_ts, cleared, cleared_ts FROM manual_state WHERE id = 1`).
		Scan(&updatedTS, &cleared, &clearedTS)
	if err != nil {
		return err
	}
	curTS, _ := parseTime(updatedTS)

	if data.Clear {
		// 同秒时清除优先：>=。
		if ts.After(curTS) || ts.Equal(curTS) {
			_, err = tx.Exec(`UPDATE manual_state SET updated_ts = ?, cleared = 1, cleared_ts = ?, data = NULL, until = NULL WHERE id = 1`,
				ev.TS, ev.TS)
		}
		return err
	}

	// 墓碑：ts ≤ cleared_ts 的设置不生效。
	if cleared && clearedTS.Valid {
		if tomb, err2 := parseTime(clearedTS.String); err2 == nil && !ts.After(tomb) {
			return nil
		}
	}
	if !ts.After(curTS) {
		return nil // 同秒或更旧：设置让位于已有状态
	}

	until := ts.Add(4 * time.Hour)
	if data.Until != "" {
		if u, err := parseTime(data.Until); err == nil {
			until = u
		}
	}
	_, err = tx.Exec(`UPDATE manual_state SET updated_ts = ?, cleared = 0, cleared_ts = NULL, data = ?, until = ? WHERE id = 1`,
		ev.TS, string(ev.Data), formatTime(until))
	return err
}

// --- 控制操作 ---

// Hide 置隐身（可带期限）：同一事务内更新可见性、清空 location/activity 实时态、
// 手动状态写墓碑（设计案 6.4 节）。
func (e *Engine) Hide(until *time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := e.now()
	if err := stateSet(tx, "visibility", "hidden"); err != nil {
		return err
	}
	hu := ""
	if until != nil {
		hu = formatTime(*until)
	}
	if err := stateSet(tx, "hidden_until", hu); err != nil {
		return err
	}
	if err := stateSet(tx, "hide_at", formatTime(now)); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM current_state WHERE type IN (?, ?, ?)`,
		protocol.TypeLocation, protocol.TypeActivity, protocol.TypeDevice); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE manual_state SET cleared = 1, cleared_ts = ?, data = NULL, until = NULL WHERE id = 1`,
		formatTime(now)); err != nil {
		return err
	}
	return tx.Commit()
}

// Show 解除隐身并记录恢复边界 resume_at：此后刷新实时态要求 ts > resume_at。
func (e *Engine) Show() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := stateSet(tx, "visibility", "visible"); err != nil {
		return err
	}
	if err := stateSet(tx, "hidden_until", ""); err != nil {
		return err
	}
	if err := stateSet(tx, "resume_at", formatTime(e.now())); err != nil {
		return err
	}
	return tx.Commit()
}

// SetDevicePolicy 变更设备定位策略；off 时同步清除该设备的旧位置（设计案 6.3 节）。
func (e *Engine) SetDevicePolicy(deviceID, policy string) error {
	switch policy {
	case "server", "local", "off":
	default:
		return fmt.Errorf("policy 只能是 server、local 或 off")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE devices SET location_policy = ? WHERE id = ?`, policy, deviceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("设备不存在: %s", deviceID)
	}
	if policy == "off" {
		if _, err := tx.Exec(`DELETE FROM current_state WHERE device_id = ? AND type = ?`, deviceID, protocol.TypeLocation); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetDeviceCapability 记录设备能力及接收时间。unsupported 的降级展示
// 资格必须定期续报；空串表示未上报，按常规规则处理。
func (e *Engine) SetDeviceCapability(deviceID, capability string) error {
	switch capability {
	case "ok", "degraded", "unsupported", "":
	default:
		return fmt.Errorf("capability 只能是 ok、degraded、unsupported 或空")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	res, err := e.db.Exec(`UPDATE devices SET presence_capability = ?, presence_capability_at = ? WHERE id = ? AND revoked_at IS NULL`, capability, formatTime(e.now()), deviceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("设备不存在: %s", deviceID)
	}
	return nil
}

// --- 内部辅助 ---

type stateRow struct {
	data       string
	ts         time.Time
	seq        uint64
	receivedAt time.Time
}

func currentState(tx *sql.Tx, deviceID, typ string) (*stateRow, error) {
	var r stateRow
	var tsS, recvS string
	err := tx.QueryRow(`SELECT data, ts, seq, received_at FROM current_state WHERE device_id = ? AND type = ?`, deviceID, typ).
		Scan(&r.data, &tsS, &r.seq, &recvS)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if r.ts, err = parseTime(tsS); err != nil {
		return nil, err
	}
	if r.receivedAt, err = parseTime(recvS); err != nil {
		return nil, err
	}
	return &r, nil
}

func upsertState(tx *sql.Tx, deviceID, typ string, data any, ts time.Time, seq uint64, receivedAt time.Time, expires time.Time) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO current_state (device_id, type, data, ts, seq, received_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id, type) DO UPDATE SET
			data = excluded.data, ts = excluded.ts, seq = excluded.seq,
			received_at = excluded.received_at, expires_at = excluded.expires_at`,
		deviceID, typ, string(raw), formatTime(ts), seq, formatTime(receivedAt), formatTime(expires))
	return err
}

func newer(newTS time.Time, newSeq uint64, oldTS time.Time, oldSeq uint64) bool {
	if newTS.Equal(oldTS) {
		return newSeq > oldSeq
	}
	return newTS.After(oldTS)
}

func observationTTL(device Device, typ string) time.Duration {
	switch typ {
	case protocol.TypePresence, protocol.TypeActivity, protocol.TypeDevice:
		// activity/device 随低频心跳上报的设备（Android 默认 15 分钟）也要能活到
		// 下一拍；presence 的 active 判定会在设备真正空闲时先行隐藏活动展示。
		ttl := 2 * device.Interval
		if ttl < 10*time.Minute {
			ttl = 10 * time.Minute
		}
		return ttl
	case protocol.TypeLocation:
		return locationTTL(device)
	}
	return 10 * time.Minute
}

func locationTTL(device Device) time.Duration { return 2 * time.Hour }

// resumeAt 读取恢复边界；从未隐身过时为零值（ts > 零值恒真）。
// stateQ 抽象事务与直连两种访问来源（读 + 写）。
type stateQ interface {
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

func stateGetRow(q stateQ, key string) (string, bool, error) {
	var v string
	err := q.QueryRow(`SELECT value FROM server_state WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (e *Engine) resumeAt(q stateQ) (time.Time, error) {
	v, ok, err := stateGetRow(q, "resume_at")
	if err != nil || !ok {
		return time.Time{}, err
	}
	return parseTime(v)
}

func (e *Engine) visibility(q stateQ) (hidden bool, hiddenUntil string, err error) {
	v, ok, err := stateGetRow(q, "visibility")
	if err != nil || !ok {
		return false, "", err
	}
	if v != "hidden" {
		return false, "", nil
	}
	hu, _, err := stateGetRow(q, "hidden_until")
	if err != nil {
		return false, "", err
	}
	if hu != "" {
		if until, err2 := parseTime(hu); err2 == nil && !e.now().Before(until) {
			// 定时隐身到期：在当前事务内原子恢复并建立恢复边界
			// （resume_at = 到期时刻）。否则到期后的 visible 只是一个读时
			// 判断，隐身期间/边界前的新鲜度窗口内补报会借机回填（P1-4）。
			if err3 := stateSet(q, "visibility", "visible"); err3 != nil {
				return false, "", err3
			}
			if err3 := stateSet(q, "hidden_until", ""); err3 != nil {
				return false, "", err3
			}
			if err3 := stateSet(q, "resume_at", hu); err3 != nil {
				return false, "", err3
			}
			return false, "", nil
		}
	}
	return true, hu, nil
}

// OwnerAuth 校验 owner 令牌（常量时间比较哈希）。
func (e *Engine) OwnerAuth(token string) bool {
	var stored string
	err := e.db.QueryRow(`SELECT value FROM server_state WHERE key = 'owner_token_hash'`).Scan(&stored)
	if err != nil {
		return false
	}
	sum := hashToken(token)
	return subtle.ConstantTimeCompare([]byte(sum), []byte(stored)) == 1
}

// OwnerTokenGenerate 签发 owner 令牌；已存在时须显式 rotate。
func (e *Engine) OwnerTokenGenerate(rotate bool) (string, error) {
	var existing string
	err := e.db.QueryRow(`SELECT value FROM server_state WHERE key = 'owner_token_hash'`).Scan(&existing)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if existing != "" && !rotate {
		return "", fmt.Errorf("owner 令牌已存在；轮换请加 --rotate")
	}
	token, err := randToken()
	if err != nil {
		return "", err
	}
	_, err = e.db.Exec(`INSERT INTO server_state (key, value) VALUES ('owner_token_hash', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, hashToken(token))
	if err != nil {
		return "", err
	}
	return token, nil
}

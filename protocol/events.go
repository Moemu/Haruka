// Package protocol 定义三端共享的上报协议：事件信封、事件负载与构造辅助。
// schema/envelope.schema.json 是对外契约，改字段须同步改 schema 与 fixtures。
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

const (
	// ProtocolVersion 是当前信封版本，服务端只接受自己认识的版本。
	ProtocolVersion = 1

	KindDesktop = "desktop"
	KindAndroid = "android"

	TypeHeartbeat = "heartbeat"
	TypePresence  = "presence"
	TypeActivity  = "activity"
	TypeLocation  = "location"
	TypeManual    = "manual"
	TypeDevice    = "device"

	StateActive = "active"
	StateAway   = "away"

	PrecisionCountry  = "country"
	PrecisionProvince = "province"
	PrecisionCity     = "city"
	PrecisionDistrict = "district"

	ZoneHome   = "home"
	ZoneSchool = "school"
)

// Envelope 是一次上报的载体，携带同设备的一批事件。
type Envelope struct {
	V          int     `json:"v"`
	DeviceID   string  `json:"device_id"`
	DeviceKind string  `json:"device_kind"`
	SentAt     string  `json:"sent_at"`
	Snapshot   bool    `json:"snapshot,omitempty"`
	Events     []Event `json:"events"`
}

// Event 的 TS 是观测时间（状态发生时刻），新鲜度判定只用它；
// Seq 为设备内单调递增序号，用于同秒事件排序。
type Event struct {
	ID   string          `json:"id"`
	Seq  uint64          `json:"seq"`
	TS   string          `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type HeartbeatData struct{}

type PresenceData struct {
	State       string `json:"state"`
	IdleMinutes int    `json:"idle_minutes"`
}

type ActivityData struct {
	Kind     string `json:"kind"` // ide | browser | app | private | idle | none
	App      string `json:"app,omitempty"`
	Category string `json:"category,omitempty"`
	FileType string `json:"file_type,omitempty"`
}

// DeviceData 是设备自述状态（电量等）。battery_level 为 0～100；无电池的设备
// （台式机）不发 device 事件，charging 只在为 true 时携带。
// steps 为当日累计步数（MIUI 计步 provider，5 分钟粒度），仅 Android 上报；
// 0 与缺席等价，来源与判死记录见 docs/design.md §5.2。
type DeviceData struct {
	BatteryLevel int  `json:"battery_level"`
	Charging     bool `json:"charging,omitempty"`
	Steps        int  `json:"steps,omitempty"`
}

type LocationData struct {
	Source    string `json:"source"` // ip | gps_coarse | manual（采集端手动覆盖）
	Precision string `json:"precision"`
	Country   string `json:"country,omitempty"`
	Province  string `json:"province,omitempty"`
	City      string `json:"city,omitempty"`
	District  string `json:"district,omitempty"`
	Zone      string `json:"zone,omitempty"`
}

type ManualData struct {
	Mood  string `json:"mood,omitempty"`
	Text  string `json:"text,omitempty"`
	Until string `json:"until,omitempty"`
	Clear bool   `json:"clear,omitempty"`
}

// NewEvent 以 UTC RFC3339 生成观测事件。data 必须是本包定义的负载类型。
func NewEvent(typ string, seq uint64, at time.Time, data any) (Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	id, err := newEventID()
	if err != nil {
		return Event{}, err
	}
	return Event{
		ID:   id,
		Seq:  seq,
		TS:   at.UTC().Format(time.RFC3339),
		Type: typ,
		Data: raw,
	}, nil
}

func BuildEnvelope(deviceID, deviceKind string, snapshot bool, events []Event) Envelope {
	return Envelope{
		V:          ProtocolVersion,
		DeviceID:   deviceID,
		DeviceKind: deviceKind,
		SentAt:     time.Now().UTC().Format(time.RFC3339),
		Snapshot:   snapshot,
		Events:     events,
	}
}

// newEventID 生成 UUIDv4 形态的事件 ID，服务端以此做幂等去重。
func newEventID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return "e_" + hex.EncodeToString(b[:]), nil
}

package server

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"haruka/protocol"
)

// Device 是登记设备（devices 表行）。
type Device struct {
	ID                   string
	Name                 string
	Kind                 string
	Interval             time.Duration
	LocationPolicy       string // server | local | off
	PresenceCapability   string // ok | degraded | unsupported；空 = 未上报
	PresenceCapabilityAt time.Time
	CreatedAt            time.Time
	Revoked              bool
}

// LocationResolver 由部署层提供（如 ip2region.xdb），把设备 IP 解析为位置；
// 返回 false 表示解析失败，此时不产生位置状态。
type LocationResolver interface {
	Resolve(ip string) (protocol.LocationData, bool)
}

// Engine 是服务端核心：写入规则（ingest）、控制操作与聚合快照共用一个实例。
type Engine struct {
	rt       *Runtime
	db       *sql.DB
	resolver LocationResolver

	mu  sync.Mutex // 串行化写事务，规避 SQLITE_BUSY
	now func() time.Time

	log *slog.Logger
}

func NewEngine(rt *Runtime, db *sql.DB, resolver LocationResolver, log *slog.Logger) *Engine {
	return &Engine{rt: rt, db: db, resolver: resolver, now: time.Now, log: log}
}

// SetClock 供测试注入时钟。
func (e *Engine) SetClock(now func() time.Time) { e.now = now }

// --- 设备注册（仅 CLI 直连本机数据库，无 HTTP 管理接口） ---

func scanDevice(row interface{ Scan(...any) error }) (Device, error) {
	var d Device
	var intervalS int64
	var createdAt string
	var capabilityAt string
	var revokedAt sql.NullString
	if err := row.Scan(&d.ID, &d.Name, &d.Kind, &intervalS, &d.LocationPolicy, &d.PresenceCapability, &capabilityAt, &createdAt, &revokedAt); err != nil {
		return d, err
	}
	d.Interval = time.Duration(intervalS) * time.Second
	d.CreatedAt, _ = parseTime(createdAt)
	d.PresenceCapabilityAt, _ = parseTime(capabilityAt)
	d.Revoked = revokedAt.Valid
	return d, nil
}

const deviceColumns = `id, name, kind, heartbeat_interval_s, location_policy, presence_capability, presence_capability_at, created_at, revoked_at`

func (e *Engine) DeviceByToken(token string) (Device, error) {
	row := e.db.QueryRow(`SELECT `+deviceColumns+` FROM devices WHERE token_hash = ? AND revoked_at IS NULL`, hashToken(token))
	d, err := scanDevice(row)
	if err == sql.ErrNoRows {
		return d, fmt.Errorf("令牌无效")
	}
	return d, err
}

func (e *Engine) DeviceByID(id string) (Device, error) {
	row := e.db.QueryRow(`SELECT `+deviceColumns+` FROM devices WHERE id = ?`, id)
	d, err := scanDevice(row)
	if err == sql.ErrNoRows {
		return d, fmt.Errorf("设备不存在: %s", id)
	}
	return d, err
}

// DeviceAdd 签发新设备，返回明文令牌（仅此一次）。
func (e *Engine) DeviceAdd(name, kind string, interval time.Duration, policy string) (Device, string, error) {
	if kind != protocol.KindDesktop && kind != protocol.KindAndroid {
		return Device{}, "", fmt.Errorf("kind 只能是 desktop 或 android")
	}
	if interval <= 0 {
		interval = e.rt.HeartbeatDefault
	}
	switch policy {
	case "server", "local", "off":
	default:
		return Device{}, "", fmt.Errorf("location_policy 只能是 server、local 或 off")
	}
	token, err := randToken()
	if err != nil {
		return Device{}, "", err
	}
	id := "d_" + randHex(4)
	_, err = e.db.Exec(`INSERT INTO devices (id, name, kind, token_hash, heartbeat_interval_s, location_policy, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, name, kind, hashToken(token), int64(interval/time.Second), policy, formatTime(e.now()))
	if err != nil {
		return Device{}, "", err
	}
	d, err := e.DeviceByID(id)
	return d, token, err
}

func (e *Engine) DeviceList() ([]Device, error) {
	rows, err := e.db.Query(`SELECT ` + deviceColumns + ` FROM devices ORDER BY created_at`)
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
		out = append(out, d)
	}
	return out, rows.Err()
}

func (e *Engine) DeviceRevoke(id string) error {
	_, err := e.db.Exec(`UPDATE devices SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, formatTime(e.now()), id)
	return err
}

// --- 保留策略 ---

// RunRetention 周期清理过期原始事件；ctx 结束返回。
func (e *Engine) RunRetention(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := formatTime(e.now().Add(-e.rt.EventsRetention))
			if res, err := e.db.Exec(`DELETE FROM events WHERE received_at < ?`, cutoff); err != nil {
				e.log.Warn("事件保留策略执行失败", "error", err)
			} else if n, _ := res.RowsAffected(); n > 0 {
				e.log.Info("已清理过期事件", "count", n)
			}
		}
	}
}

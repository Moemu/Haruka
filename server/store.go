package server

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// OpenDB 打开（并迁移）服务端数据库。表结构见设计案 7.3 节。
func OpenDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		db.Close()
		return nil, err
	}
	for _, stmt := range migrations {
		if _, err := db.Exec(stmt); err != nil {
			// 幂等迁移（如为早期库补列）在列已存在时视为成功。
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			db.Close()
			return nil, fmt.Errorf("迁移失败: %w", err)
		}
	}
	return db, nil
}

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS devices (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		kind TEXT NOT NULL,
		token_hash TEXT NOT NULL,
		heartbeat_interval_s INTEGER NOT NULL,
		location_policy TEXT NOT NULL DEFAULT 'server',
		presence_capability TEXT NOT NULL DEFAULT '',
		presence_capability_at TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		revoked_at TEXT
	)`,
	// 早期库没有 presence_capability 列：已存在时忽略本条迁移。
	`ALTER TABLE devices ADD COLUMN presence_capability TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE devices ADD COLUMN presence_capability_at TEXT NOT NULL DEFAULT ''`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_devices_token ON devices(token_hash)`,
	`CREATE TABLE IF NOT EXISTS events (
		id TEXT PRIMARY KEY,
		device_id TEXT NOT NULL,
		type TEXT NOT NULL,
		ts TEXT NOT NULL,
		data TEXT NOT NULL,
		received_at TEXT NOT NULL,
		clock_skew INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS idx_events_received ON events(received_at)`,
	`CREATE TABLE IF NOT EXISTS current_state (
		device_id TEXT NOT NULL,
		type TEXT NOT NULL,
		data TEXT NOT NULL,
		ts TEXT NOT NULL,
		seq INTEGER NOT NULL,
		received_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		PRIMARY KEY (device_id, type)
	)`,
	`CREATE TABLE IF NOT EXISTS manual_state (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		updated_ts TEXT NOT NULL,
		cleared INTEGER NOT NULL DEFAULT 1,
		cleared_ts TEXT,
		data TEXT,
		until TEXT
	)`,
	`INSERT OR IGNORE INTO manual_state (id, updated_ts, cleared) VALUES (1, '1970-01-01T00:00:00Z', 1)`,
	`CREATE TABLE IF NOT EXISTS server_state (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
}

// randToken 生成 32 字节令牌（base64url，43 字符），明文只在签发时显示一次。
func randToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randHex(n int) string {
	var b [16]byte
	if _, err := rand.Read(b[:n]); err != nil {
		panic(err) // crypto/rand 失败属于不可恢复错误
	}
	return hex.EncodeToString(b[:n])
}

// --- server_state 键值访问 ---

func stateGet(tx *sql.Tx, key string) (string, bool, error) {
	var v string
	err := tx.QueryRow(`SELECT value FROM server_state WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return v, err == nil, err
}

func stateSet(tx stateQ, key, value string) error {
	_, err := tx.Exec(`INSERT INTO server_state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// --- 时间与格式辅助 ---

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

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

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

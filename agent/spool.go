package agent

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"haruka/protocol"
)

// Spool 是断线续传的落盘缓冲：只存观测事件与 manual 操作，
// heartbeat 永不入 spool（见设计案 5.3、8.3 节）。
type Spool struct {
	db   *sql.DB
	path string
}

func OpenSpool(path string) (*Spool, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS spool (
		id      INTEGER PRIMARY KEY AUTOINCREMENT,
		ts      TEXT NOT NULL,
		type    TEXT NOT NULL,
		payload TEXT NOT NULL
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Spool{db: db, path: path}, nil
}

func (s *Spool) Add(events []protocol.Event) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO spool (ts, type, payload) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, ev := range events {
		if ev.Type == protocol.TypeHeartbeat {
			continue
		}
		raw, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(ev.TS, ev.Type, string(raw)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// List 按入队顺序（即 ts 升序）取最早的一批。
func (s *Spool) List(limit int) ([]protocol.Event, []int64, error) {
	rows, err := s.db.Query(`SELECT id, payload FROM spool ORDER BY id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var events []protocol.Event
	var ids []int64
	for rows.Next() {
		var id int64
		var payload string
		if err := rows.Scan(&id, &payload); err != nil {
			return nil, nil, err
		}
		var ev protocol.Event
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return nil, nil, fmt.Errorf("spool 记录损坏 id=%d: %w", id, err)
		}
		events = append(events, ev)
		ids = append(ids, id)
	}
	return events, ids, rows.Err()
}

func (s *Spool) Delete(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM spool WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Spool) Count() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM spool`).Scan(&n)
	return n, err
}

// Purge 清空全部缓冲（设备暂停时调用）。
func (s *Spool) Purge() error {
	_, err := s.db.Exec(`DELETE FROM spool`)
	return err
}

// Cap 限制保留时间与事件负载字节数。SQLite 文件可复用空闲页，
// 删除行不会缩小文件；不能按文件大小反复删到整队列清空。
func (s *Spool) Cap(maxAge time.Duration, maxBytes int64) error {
	cutoff := time.Now().UTC().Add(-maxAge).Format(time.RFC3339)
	if _, err := s.db.Exec(`DELETE FROM spool WHERE ts < ?`, cutoff); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM spool WHERE id IN (
		SELECT id FROM (
			SELECT id, SUM(length(CAST(payload AS BLOB))) OVER (ORDER BY id DESC) AS retained_bytes
			FROM spool
		) WHERE retained_bytes > ?
	)`, maxBytes)
	return err
}

func (s *Spool) Close() error { return s.db.Close() }

package agent

import (
	"path/filepath"
	"testing"
	"time"

	"haruka/protocol"
)

func openTestSpool(t *testing.T) *Spool {
	t.Helper()
	s, err := OpenSpool(filepath.Join(t.TempDir(), "spool.db"))
	if err != nil {
		t.Fatalf("OpenSpool: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSpoolRoundTrip(t *testing.T) {
	s := openTestSpool(t)
	var events []protocol.Event
	for i := 0; i < 3; i++ {
		ev, err := protocol.NewEvent(protocol.TypeActivity, uint64(i), time.Now(),
			protocol.ActivityData{Kind: "app", App: "demo.exe", Category: "other"})
		if err != nil {
			t.Fatalf("NewEvent: %v", err)
		}
		events = append(events, ev)
	}
	if err := s.Add(events); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if n, _ := s.Count(); n != 3 {
		t.Fatalf("Count = %d, want 3", n)
	}
	got, ids, err := s.List(2)
	if err != nil || len(got) != 2 || len(ids) != 2 {
		t.Fatalf("List(2): %d events, err=%v", len(got), err)
	}
	if got[0].TS != events[0].TS || got[0].Type != protocol.TypeActivity {
		t.Fatalf("事件内容不一致: %+v vs %+v", got[0], events[0])
	}
	if err := s.Delete(ids); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n, _ := s.Count(); n != 1 {
		t.Fatalf("删除后 Count = %d, want 1", n)
	}
	// 剩余 1 条是最早未取的那批之后的：List 保证按入队顺序。
	rest, _, _ := s.List(10)
	if rest[0].TS != events[2].TS {
		t.Fatalf("删除后剩余应为最后一条: %+v", rest[0])
	}
}

func TestSpoolPurgeAndCap(t *testing.T) {
	s := openTestSpool(t)
	for i := 0; i < 4; i++ {
		ev, _ := protocol.NewEvent(protocol.TypeHeartbeat, uint64(i), time.Now(), protocol.HeartbeatData{})
		_ = s.Add([]protocol.Event{ev})
	}
	if err := s.Purge(); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if n, _ := s.Count(); n != 0 {
		t.Fatalf("Purge 后应清空，实际 %d", n)
	}

	// Cap 按时间淘汰：写入一条「8 天前」的记录，保留期 7 天。
	old, _ := protocol.NewEvent(protocol.TypeActivity, 9, time.Now().Add(-8*24*time.Hour), protocol.ActivityData{Kind: "app", App: "demo"})
	// 直接改写 ts 模拟旧记录。
	old.TS = time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	if err := s.Add([]protocol.Event{old}); err != nil {
		t.Fatalf("Add old: %v", err)
	}
	if err := s.Cap(7*24*time.Hour, 1<<20); err != nil {
		t.Fatalf("Cap: %v", err)
	}
	if n, _ := s.Count(); n != 0 {
		t.Fatalf("过期记录应被 Cap 清除，实际 %d", n)
	}
}

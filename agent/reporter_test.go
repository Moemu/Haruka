package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"haruka/protocol"
)

type recordingServer struct {
	mu        sync.Mutex
	failFirst int
	served    int
	bodies    []protocol.Envelope
}

func (s *recordingServer) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.served++
	if s.served <= s.failFirst {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var env protocol.Envelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.bodies = append(s.bodies, env)
	w.WriteHeader(http.StatusOK)
}

func (s *recordingServer) envelopes() []protocol.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.Envelope(nil), s.bodies...)
}

func newTestReporter(t *testing.T, srv *httptest.Server, stateDir string) *Reporter {
	t.Helper()
	rt := testRuntime(t, nil)
	rt.ServerURL = srv.URL
	spool, err := OpenSpool(filepath.Join(stateDir, "spool.db"))
	if err != nil {
		t.Fatalf("OpenSpool: %v", err)
	}
	t.Cleanup(func() { spool.Close() })
	seq, err := OpenSeq(stateDir)
	if err != nil {
		t.Fatalf("OpenSeq: %v", err)
	}
	deps := Deps{DeviceID: "d_test01", DeviceKind: protocol.KindDesktop, Seq: seq}
	rep := NewReporter(deps, rt, spool, func() (time.Time, bool) { return time.Time{}, false }, discardLogger())
	rep.backoffInit = time.Millisecond
	rep.backoffMax = 4 * time.Millisecond
	return rep
}

func makeEvents(t *testing.T, seq *Seq, n int) []protocol.Event {
	t.Helper()
	var out []protocol.Event
	for i := 0; i < n; i++ {
		ev, err := protocol.NewEvent(protocol.TypePresence, seq.Next(), time.Now(),
			protocol.PresenceData{State: protocol.StateActive, IdleMinutes: i})
		if err != nil {
			t.Fatalf("NewEvent: %v", err)
		}
		out = append(out, ev)
	}
	return out
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestReporterSpoolAndSnapshotFirst(t *testing.T) {
	srv := &recordingServer{failFirst: 1}
	ts := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer ts.Close()

	dir := t.TempDir()
	rep := newTestReporter(t, ts, dir)
	seq, _ := OpenSeq(dir)
	ctx := context.Background()

	rep.Enqueue(makeEvents(t, seq, 3)...)
	if err := rep.FlushOnce(ctx); err == nil {
		t.Fatal("服务故障时应返回错误")
	}
	rep.mu.Lock()
	rep.nextTry = time.Time{} // 测试跳过真实时钟退避
	rep.mu.Unlock()
	if _, spooled := rep.Stats(); spooled != 3 {
		t.Fatalf("失败后应转入 spool 3 条，实际 %d", spooled)
	}

	// 恢复连接：队列里已有新事件 → 第一包必须是快照。
	rep.Enqueue(makeEvents(t, seq, 1)...)
	if err := rep.FlushOnce(ctx); err != nil {
		t.Fatalf("恢复后首次上报: %v", err)
	}
	bodies := srv.envelopes()
	if len(bodies) != 1 || !bodies[0].Snapshot || len(bodies[0].Events) != 1 {
		t.Fatalf("第一包应为只含新事件的快照: %+v", bodies)
	}

	// 第二轮清空 spool 欠账（非快照）。
	if err := rep.FlushOnce(ctx); err != nil {
		t.Fatalf("补报积压: %v", err)
	}
	bodies = srv.envelopes()
	if len(bodies) != 2 || bodies[1].Snapshot {
		t.Fatalf("第二包应为非快照积压: %+v", bodies)
	}
	if len(bodies[1].Events) != 3 {
		t.Fatalf("积压应为 3 条: %+v", bodies[1].Events)
	}
	if _, spooled := rep.Stats(); spooled != 0 {
		t.Fatalf("spool 应已清空，实际 %d", spooled)
	}
}

func TestReporterPauseClearsQueueAndSpool(t *testing.T) {
	srv := &recordingServer{failFirst: 1}
	ts := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer ts.Close()

	dir := t.TempDir()
	paused := false
	rep := newTestReporter(t, ts, dir)
	rep.pauseFn = func() (time.Time, bool) { return time.Now().Add(time.Hour), paused }
	seq, _ := OpenSeq(dir)
	ctx := context.Background()

	rep.Enqueue(makeEvents(t, seq, 2)...)
	_ = rep.FlushOnce(ctx) // 失败 → spool 2 条

	paused = true
	if err := rep.FlushOnce(ctx); err != nil {
		t.Fatalf("暂停清理: %v", err)
	}
	if _, spooled := rep.Stats(); spooled != 0 {
		t.Fatalf("暂停应清空 spool，实际 %d", spooled)
	}

	paused = false
	if err := rep.FlushOnce(ctx); err != nil {
		t.Fatalf("恢复: %v", err)
	}
	for _, env := range srv.envelopes() {
		for _, ev := range env.Events {
			if ev.TS == "" {
				t.Fatalf("不应有丢失字段的事件: %+v", ev)
			}
		}
	}
	// 暂停前的积压已被清空：恢复后不应有 3 条以上的包。
	if bodies := srv.envelopes(); len(bodies) > 1 {
		t.Fatalf("暂停应丢弃积压，不应补报: %d 包", len(bodies))
	}
}

func TestReporterHeartbeatNotSpooled(t *testing.T) {
	srv := &recordingServer{failFirst: 100}
	ts := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer ts.Close()

	dir := t.TempDir()
	rep := newTestReporter(t, ts, dir)
	ctx := context.Background()

	if err := rep.SendHeartbeat(ctx); err == nil {
		t.Fatal("服务故障时心跳应失败")
	}
	if _, spooled := rep.Stats(); spooled != 0 {
		t.Fatalf("heartbeat 失败不得入 spool，实际 %d", spooled)
	}
}

// 断言（审查 P1-5）：任何一包不超过 BatchSize；spool 与新观测交错补报，
// 长积压不得饿死实时态。
func TestReporterBatchCapAndInterleave(t *testing.T) {
	srv := &recordingServer{failFirst: 3}
	ts := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer ts.Close()

	dir := t.TempDir()
	rep := newTestReporter(t, ts, dir)
	seq, _ := OpenSeq(dir)
	ctx := context.Background()

	// 45 条事件在服务故障期间全部进 spool（每轮最多取 20）。
	rep.Enqueue(makeEvents(t, seq, 45)...)
	for i := 0; i < 3; i++ {
		_ = rep.FlushOnce(ctx)
		rep.mu.Lock()
		rep.nextTry = time.Time{} // 测试跳过真实时钟退避
		rep.mu.Unlock()
	}
	if _, spooled := rep.Stats(); spooled != 45 {
		t.Fatalf("故障期应全部入 spool：实际 %d", spooled)
	}

	// 服务恢复 + 队列里已有 10 条新观测。
	rep.Enqueue(makeEvents(t, seq, 10)...)
	bodies := 0
	check := func(wantSnapshot bool, wantLen int) {
		t.Helper()
		if err := rep.FlushOnce(ctx); err != nil {
			t.Fatalf("flush: %v", err)
		}
		bodies++
		envs := srv.envelopes()
		got := envs[len(envs)-1]
		if got.Snapshot != wantSnapshot || len(got.Events) != wantLen {
			t.Fatalf("第 %d 包: snapshot=%v len=%d, want %v/%d",
				bodies, got.Snapshot, len(got.Events), wantSnapshot, wantLen)
		}
	}
	check(true, 10)  // 快照：新观测优先，且整包 ≤ BatchSize
	check(false, 20) // 交错：先补最老的 spool 旧账
	rep.Enqueue(makeEvents(t, seq, 10)...)
	check(false, 10) // 轮到新观测
	check(false, 20) // 再补历史
	check(false, 5)  // spool 清零
	rep.Enqueue(makeEvents(t, seq, 10)...)
	check(false, 10) // 剩余队列
	if _, spooled := rep.Stats(); spooled != 0 {
		t.Fatalf("spool 应清空: %d", spooled)
	}
}

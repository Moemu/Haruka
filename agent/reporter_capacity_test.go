package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"haruka/protocol"
)

func TestReporterBoundsMemoryAndSnapshotsNewest(t *testing.T) {
	srv := &recordingServer{}
	ts := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer ts.Close()
	rep := newTestReporter(t, ts, t.TempDir())
	events := makeEvents(t, rep.deps.Seq, 150)
	rep.Enqueue(events...)
	queued, spooled := rep.Stats()
	if queued != 100 || spooled != 50 {
		t.Fatalf("queued=%d spooled=%d", queued, spooled)
	}
	if err := rep.FlushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	env := srv.envelopes()[0]
	if !env.Snapshot || env.Events[len(env.Events)-1].ID != events[149].ID {
		t.Fatal("first recovery batch did not contain newest observation")
	}
	queued, _ = rep.Stats()
	if queued != 100-int64(rep.rt.BatchSize) {
		t.Fatalf("remaining queued=%d", queued)
	}
	// Subsequent queue operations cannot change the already captured batch.
	rep.Enqueue(events[0])
	if env.Events[len(env.Events)-1].ID != events[149].ID {
		t.Fatal("batch aliased queue")
	}
}

func TestSpoolCapacityKeepsNewestAfterSQLiteHighWatermark(t *testing.T) {
	s := openTestSpool(t)
	var events []protocol.Event
	for i := 0; i < 20; i++ {
		ev, _ := protocol.NewEvent(protocol.TypeActivity, uint64(i), time.Now(), protocol.ActivityData{Kind: "app", App: "demo"})
		events = append(events, ev)
	}
	if err := s.Add(events); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(events[19])
	if err := s.Cap(time.Hour, int64(len(raw)*3)); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.List(100)
	if err != nil || len(got) != 3 || got[2].ID != events[19].ID {
		t.Fatalf("retained=%d err=%v", len(got), err)
	}
	if err := s.Cap(time.Hour, int64(len(raw)*3)); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Count(); n != 3 {
		t.Fatalf("repeated cap erased retained data: %d", n)
	}
}

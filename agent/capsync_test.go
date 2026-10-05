package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCapSyncRefreshesChangedCapabilityAfterFailure(t *testing.T) {
	values := make(chan Capability, 4)
	attempt := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/device-capability" || r.Header.Get("Authorization") != "Bearer test" {
			t.Error("wrong endpoint or auth")
		}
		var body struct {
			Presence Capability `json:"presence"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		values <- body.Presence
		attempt++
		if attempt == 1 {
			w.WriteHeader(503)
		}
	}))
	defer ts.Close()
	probes := 0
	rt := Runtime{ServerURL: ts.URL, Heartbeat: 10 * time.Millisecond, Cfg: Config{Token: "test"}}
	c := NewCapSync(&rt, func() Capability {
		probes++
		if probes < 3 {
			return CapUnsupported
		}
		return CapDegraded
	}, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	for _, want := range []Capability{CapUnsupported, CapUnsupported, CapDegraded} {
		select {
		case got := <-values:
			if got != want {
				t.Fatalf("want %s got %s", want, got)
			}
		case <-time.After(time.Second):
			t.Fatal("sync did not retry or refresh")
		}
	}
	cancel()
	<-done
}

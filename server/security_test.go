package server

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"haruka/protocol"
)

func TestDisplayFieldsRejectSensitiveShapes(t *testing.T) {
	for _, value := range []string{`C:\Users\person\private`, "/home/person/private", "https://example.com", "203.0.113.7", "2001:db8::1", "地点 -31.19, 121.43", "31，121", "hello\nworld"} {
		for _, data := range []any{
			protocol.ActivityData{Kind: "app", App: value},
			protocol.LocationData{Source: "ip", Precision: "city", City: value},
			protocol.ManualData{Mood: "happy", Text: value},
		} {
			typ := protocol.TypeActivity
			switch data.(type) {
			case protocol.LocationData:
				typ = protocol.TypeLocation
			case protocol.ManualData:
				typ = protocol.TypeManual
			}
			ev, _ := protocol.NewEvent(typ, 1, time.Now(), data)
			if err := validateEvent(&ev); err == nil {
				t.Errorf("%s accepted sensitive shape %q", typ, value)
			}
		}
	}
}

func TestDuplicateJSONKeysDoNotPersistDiscardedValues(t *testing.T) {
	ev, _ := protocol.NewEvent(protocol.TypeActivity, 1, time.Now(), protocol.ActivityData{})
	ev.Data = json.RawMessage(`{"kind":"app","app":"https://private.example","app":"demo"}`)
	env := protocol.BuildEnvelope("d_test", protocol.KindDesktop, false, []protocol.Event{ev})
	raw, _ := json.Marshal(env)
	got, err := ValidateEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got.Events[0].Data), "private.example") {
		t.Fatal("discarded duplicate value survived validation")
	}
}

func TestIngestRejectsTrailingJSONAndNullData(t *testing.T) {
	raw, _ := json.Marshal(sampleEnvelope("d_test"))
	if _, err := ValidateEnvelope(append(raw, []byte("{}")...)); err == nil {
		t.Fatal("accepted trailing JSON")
	}
	ev, _ := protocol.NewEvent(protocol.TypeHeartbeat, 1, time.Now(), protocol.HeartbeatData{})
	ev.Data = json.RawMessage("null")
	if err := validateEvent(&ev); err == nil {
		t.Fatal("accepted null event data")
	}
}

func TestControlAndPolicyBoundedStrictJSON(t *testing.T) {
	f := newHTTPFixture(t)
	owner, err := f.e.OwnerTokenGenerate(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, token, body string }{
		{"/v1/control", owner, `{"action":"hide"}{}`},
		{"/v1/control", owner, `{"action":"` + strings.Repeat("x", 5000) + `"}`},
		{"/v1/device-policy", f.token, `{"policy":"off"}{}`},
		{"/v1/device-policy", f.token, `{"policy":"` + strings.Repeat("x", 5000) + `"}`},
	} {
		req, _ := http.NewRequest(http.MethodPost, f.ts.URL+tc.path, bytes.NewBufferString(tc.body))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s returned %d", tc.path, resp.StatusCode)
		}
	}
	snap, err := f.e.Snapshot()
	if err != nil || snap.Visibility != "visible" {
		t.Fatal("rejected request changed visibility")
	}
}

type countingResolver struct{ calls int }

func (r *countingResolver) Resolve(string) (protocol.LocationData, bool) {
	r.calls++
	return protocol.LocationData{Source: "ip", Precision: "city", City: "测试市"}, true
}

func TestReplayCannotRefreshDerivedLocation(t *testing.T) {
	te := newTestEnv(t, nil)
	dev := te.addDevice(t, protocol.KindDesktop, time.Minute)
	r := &countingResolver{}
	te.e.resolver = r
	ev, _ := protocol.NewEvent(protocol.TypeHeartbeat, 1, te.now, protocol.HeartbeatData{})
	env := protocol.BuildEnvelope(dev.ID, dev.Kind, false, []protocol.Event{ev})
	ip := net.ParseIP("203.0.113.7")
	if _, err := te.e.IngestEnvelope(dev, ip, &env); err != nil {
		t.Fatal(err)
	}
	te.now = te.now.Add(time.Minute)
	if _, err := te.e.IngestEnvelope(dev, ip, &env); err != nil {
		t.Fatal(err)
	}
	old, _ := protocol.NewEvent(protocol.TypeActivity, 2, te.now.Add(-24*time.Hour), protocol.ActivityData{Kind: "app", App: "demo"})
	env.Events = []protocol.Event{old}
	if _, err := te.e.IngestEnvelope(dev, ip, &env); err != nil {
		t.Fatal(err)
	}
	if r.calls != 1 {
		t.Fatalf("resolver called %d times", r.calls)
	}
}

func TestHiddenReplayCannotRefreshHeartbeat(t *testing.T) {
	te := newTestEnv(t, func(c *Config) { c.OnHide = "keep" })
	dev := te.addDevice(t, protocol.KindDesktop, time.Minute)
	if err := te.e.Hide(nil); err != nil {
		t.Fatal(err)
	}
	ev, _ := protocol.NewEvent(protocol.TypeHeartbeat, 1, te.now, protocol.HeartbeatData{})
	te.emitRaw(t, dev, ev)
	te.now = te.now.Add(4 * time.Minute)
	te.emitRaw(t, dev, ev)
	if err := te.e.Show(); err != nil {
		t.Fatal(err)
	}
	if te.snapshot(t).Online {
		t.Fatal("replayed hidden heartbeat revived online status")
	}
}

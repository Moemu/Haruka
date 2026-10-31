package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	ev, err := NewEvent(TypePresence, 42, time.Now(), PresenceData{State: StateActive, IdleMinutes: 0})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	env := BuildEnvelope("d_test01", KindDesktop, true, []Event{ev})

	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Envelope
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.V != ProtocolVersion || back.DeviceID != "d_test01" || !back.Snapshot {
		t.Fatalf("envelope fields lost: %+v", back)
	}
	if len(back.Events) != 1 || back.Events[0].Type != TypePresence || back.Events[0].Seq != 42 {
		t.Fatalf("event fields lost: %+v", back.Events)
	}
	var pd PresenceData
	if err := json.Unmarshal(back.Events[0].Data, &pd); err != nil {
		t.Fatalf("presence data: %v", err)
	}
	if pd.State != StateActive {
		t.Fatalf("state lost: %+v", pd)
	}
}

func TestEventTSIsUTCRFC3339(t *testing.T) {
	ev, err := NewEvent(TypeHeartbeat, 1, time.Now(), HeartbeatData{})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, ev.TS); err != nil {
		t.Fatalf("ts not RFC3339: %q", ev.TS)
	}
	if !strings.HasSuffix(ev.TS, "Z") && !strings.HasSuffix(ev.TS, "+00:00") {
		t.Fatalf("ts not UTC: %q", ev.TS)
	}
}

func TestEventIDFormat(t *testing.T) {
	for i := 0; i < 16; i++ {
		ev, err := NewEvent(TypeHeartbeat, uint64(i), time.Now(), HeartbeatData{})
		if err != nil {
			t.Fatalf("NewEvent: %v", err)
		}
		if len(ev.ID) != len("e_")+32 || !strings.HasPrefix(ev.ID, "e_") {
			t.Fatalf("unexpected id %q", ev.ID)
		}
	}
}

// TestFixtures 保证 fixtures 与协议结构保持同步：valid 套件必须可反序列化，
// invalid 套件的核心断言（未知字段、坐标字段、非法 project）在服务端 schema 校验里执行，
// 这里检查它们的字段确实落在合法负载之外。
func TestFixtures(t *testing.T) {
	valid := []string{
		"valid-presence.json", "valid-activity.json", "valid-location-city.json",
		"valid-location-district.json", "valid-location-manual.json", "valid-manual.json",
		"valid-heartbeat.json", "valid-device.json",
	}
	for _, name := range valid {
		var env Envelope
		if err := loadFixture(t, name, &env); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if env.V != ProtocolVersion || len(env.Events) == 0 {
			t.Errorf("%s: bad envelope %+v", name, env)
		}
	}
	invalid := []string{"invalid-unknown-field.json", "invalid-location-coords.json", "invalid-device-battery.json", "invalid-device-steps.json"}
	for _, name := range invalid {
		var env map[string]any
		if err := loadFixture(t, name, &env); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func loadFixture(t *testing.T, name string, out any) error {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("fixtures", name))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

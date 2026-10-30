package server

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"haruka/protocol"
)

func TestActivityEligibilityBySourceCapability(t *testing.T) {
	for _, tc := range []struct {
		name, capability, presence string
		elapsed                    time.Duration
		want                       string
	}{
		{"active", "ok", "active", 0, "正在使用 IDE"},
		{"unsupported", "unsupported", "", 0, "前台应用：IDE"},
		{"away", "unsupported", "away", 0, ""},
		{"unreported", "", "", 0, ""},
		{"failed", "degraded", "", 0, ""},
		{"expired-presence", "unsupported", "active", 11 * time.Minute, ""},
		{"expired-capability", "unsupported", "", 4 * time.Minute, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te := newTestEnv(t, nil)
			dev := te.addDevice(t, protocol.KindDesktop, time.Minute)
			if err := te.e.SetDeviceCapability(dev.ID, tc.capability); err != nil {
				t.Fatal(err)
			}
			if tc.presence != "" {
				te.emit(t, dev, protocol.TypePresence, te.now, protocol.PresenceData{State: tc.presence})
			}
			te.now = te.now.Add(tc.elapsed)
			te.heartbeat(t, dev)
			te.emit(t, dev, protocol.TypeActivity, te.now, protocol.ActivityData{Kind: "ide", App: "IDE"})
			if tc.name == "expired-presence" {
				if err := te.e.SetDeviceCapability(dev.ID, tc.capability); err != nil {
					t.Fatal(err)
				}
			}
			snap := te.snapshot(t)
			if tc.want == "" {
				if snap.Activity != nil {
					t.Fatalf("must hide: %+v", snap.Activity)
				}
				return
			}
			if snap.Activity == nil || snap.Activity.Display != tc.want {
				t.Fatalf("want %q, got %+v", tc.want, snap.Activity)
			}
			te.now = te.now.Add(11 * time.Minute)
			te.heartbeat(t, dev)
			if snap = te.snapshot(t); snap.Activity != nil {
				t.Fatal("expired activity must hide")
			}
		})
	}
}

func TestActivityDoesNotBorrowAnotherDevicePresence(t *testing.T) {
	te := newTestEnv(t, nil)
	active := te.addDevice(t, protocol.KindAndroid, time.Minute)
	source := te.addDevice(t, protocol.KindDesktop, time.Minute)
	te.heartbeat(t, active)
	te.heartbeat(t, source)
	te.emit(t, active, protocol.TypePresence, te.now, protocol.PresenceData{State: "active"})
	te.emit(t, source, protocol.TypeActivity, te.now, protocol.ActivityData{Kind: "app", App: "Editor"})
	if snap := te.snapshot(t); snap.Presence.State != "active" || snap.Activity != nil {
		t.Fatalf("unqualified source must hide: %+v", snap)
	}
	if err := te.e.SetDeviceCapability(source.ID, "unsupported"); err != nil {
		t.Fatal(err)
	}
	if snap := te.snapshot(t); snap.Activity == nil || snap.Activity.Display != "前台应用：Editor" {
		t.Fatalf("unsupported source should show neutral description: %+v", snap)
	}
	if err := te.e.Hide(nil); err != nil {
		t.Fatal(err)
	}
	if snap := te.snapshot(t); snap.Activity != nil {
		t.Fatal("hidden status must not expose activity")
	}
}

func TestDeviceCapabilityEndpoint(t *testing.T) {
	f := newHTTPFixture(t)
	for _, tc := range []struct {
		body, token string
		status      int
	}{
		{`{"presence":"unsupported"}`, f.token, 200},
		{`{"presence":"degraded"}`, f.token, 200},
		{`{"presence":"ok"}`, "invalid", 401},
		{`{}`, f.token, 400},
		{`{"presence":"bogus"}`, f.token, 400},
		{`{"presence":"unsupported","device_id":"other"}`, f.token, 400},
		{`{"presence":"unsupported"}{}`, f.token, 400},
	} {
		req, err := http.NewRequest("POST", f.ts.URL+"/v1/device-capability", strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tc.token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatalf("body %s: want %d got %d", tc.body, tc.status, res.StatusCode)
		}
	}
	d, err := f.e.DeviceByID(f.device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.PresenceCapability != "degraded" || d.PresenceCapabilityAt.IsZero() {
		t.Fatalf("invalid requests must leave last valid capability intact: %+v", d)
	}
}

func TestCapabilityMigrationPreservesExistingDevice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO devices (id,name,kind,token_hash,heartbeat_interval_s,location_policy,created_at) VALUES ('d_old','old','desktop',?,60,'off','2026-10-05T00:00:00Z')`, hashToken("test"))
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"presence_capability_at", "presence_capability"} {
		if _, err := db.Exec("ALTER TABLE devices DROP COLUMN " + column); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	for i := 0; i < 2; i++ {
		db, err = OpenDB(path)
		if err != nil {
			t.Fatal(err)
		}
		e := NewEngine(&Runtime{}, db, nil, discardLoggerS())
		d, err := e.DeviceByToken("test")
		if err != nil {
			t.Fatal(err)
		}
		if d.ID != "d_old" || d.PresenceCapability != "" || !d.PresenceCapabilityAt.IsZero() {
			t.Fatalf("migration must preserve device and start conservatively: %+v", d)
		}
		db.Close()
	}
}

package agent

import (
	"os"
	"testing"
	"time"
)

func TestBrokenPauseMarkerKeepsCollectionPaused(t *testing.T) {
	dir := t.TempDir()
	for _, body := range []string{"broken", `{"until":"bad"}`} {
		if err := os.WriteFile(PauseMarkerPath(dir), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, paused, err := ReadPauseMarker(dir); !paused || err == nil {
			t.Fatalf("paused=%v err=%v", paused, err)
		}
	}
	if err := ClearPauseMarker(dir); err != nil {
		t.Fatal(err)
	}
	if _, paused, err := ReadPauseMarker(dir); paused || err != nil {
		t.Fatalf("clear: paused=%v err=%v", paused, err)
	}
	if err := WritePauseMarker(dir, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, paused, err := ReadPauseMarker(dir); paused || err != nil {
		t.Fatalf("expired: paused=%v err=%v", paused, err)
	}
}

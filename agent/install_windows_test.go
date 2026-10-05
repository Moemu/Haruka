//go:build windows

package agent

import (
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsArgumentRoundTrip(t *testing.T) {
	args := []string{`install`, `C:\folder with spaces\`, `a"b`, "", `--state-dir`, `C:\cache\`}
	got, err := windows.DecomposeCommandLine(quoteWindowsArgs(args))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, args) {
		t.Fatalf("roundtrip = %q, want %q", got, args)
	}
}

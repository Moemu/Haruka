//go:build linux

package agent

import (
	"net"
	"testing"
	"time"
)

func TestWaylandOldConnectionCannotChangeNewState(t *testing.T) {
	old, peer := net.Pipe()
	defer old.Close()
	defer peer.Close()
	oldConn := &waylandConn{conn: old, notifyID: 5}
	src := newWaylandIdleSource(nil)
	src.conn = &waylandConn{}
	done := make(chan struct{})
	go func() { src.pump(oldConn); close(done) }()
	if err := (&waylandConn{conn: peer}).send(wlMessage{obj: 5, opcode: 0}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old event stream did not exit")
	}
	if src.idled || !src.idleStart.IsZero() {
		t.Fatal("old connection changed new connection state")
	}
}

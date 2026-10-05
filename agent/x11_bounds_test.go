//go:build linux

package agent

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
)

func TestX11OrdinaryEventDoesNotTreatTimestampAsLength(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	event := make([]byte, 32)
	event[0] = 2
	binary.LittleEndian.PutUint32(event[4:8], 0xffffffff)
	reply := make([]byte, 32)
	reply[0] = 1
	go func() { _, _ = peer.Write(append(event, reply...)) }()
	got, err := (&x11Conn{conn: client}).readReply()
	if err != nil || len(got) != 32 || got[0] != 1 {
		t.Fatalf("reply=%v err=%v", got, err)
	}
}

func TestX11OversizedReplyRejectedBeforeAllocation(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	reply := make([]byte, 32)
	reply[0] = 1
	binary.LittleEndian.PutUint32(reply[4:8], 0xffffffff)
	go func() { _, _ = peer.Write(reply) }()
	if _, err := (&x11Conn{conn: client}).readReply(); err == nil {
		t.Fatal("accepted oversized reply")
	}
}

func TestX11MalformedSetupRejectionReturnsError(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	go func() {
		_, _ = io.ReadFull(peer, make([]byte, 12))
		prefix := make([]byte, 8)
		prefix[1] = 31 // rejection reason exceeds the zero-length body
		_, _ = peer.Write(prefix)
	}()
	if err := (&x11Conn{conn: client}).handshake("", nil); err == nil {
		t.Fatal("accepted malformed setup")
	}
}

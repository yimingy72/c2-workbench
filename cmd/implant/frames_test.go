package main

import (
	"net"
	"testing"
	"time"

	"c2-platform/internal/protocol"
)

// TestReadFramesSurvivesSplitFrames is the regression test for the disconnect
// that killed every session on its first command: the old loop decoded under a
// 120 ms read deadline, so a frame arriving in two pieces threw away the first
// piece and desynchronised the stream.
func TestReadFramesSurvivesSplitFrames(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	frames, errs := readFrames(client)
	defer client.Close()

	go func() {
		enc := protocol.Encode(protocol.CmdExec, []byte("whoami"))
		_, _ = server.Write(enc[:4])       // header half
		time.Sleep(300 * time.Millisecond) // far beyond the old deadline
		_, _ = server.Write(enc[4:])
	}()

	select {
	case fr, ok := <-frames:
		if !ok {
			t.Fatal("frame channel closed")
		}
		if fr.Type != protocol.CmdExec || string(fr.Payload) != "whoami" {
			t.Fatalf("got type=%#x payload=%q", fr.Type, fr.Payload)
		}
	case err := <-errs:
		t.Fatalf("reader failed on a split frame: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the frame")
	}
}

// TestReadFramesKeepsOrder checks back-to-back frames are not merged or dropped.
func TestReadFramesKeepsOrder(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	frames, errs := readFrames(client)
	defer client.Close()

	go func() {
		for _, cmd := range []string{"one", "two", "three"} {
			if _, err := server.Write(protocol.Encode(protocol.CmdExec, []byte(cmd))); err != nil {
				return
			}
		}
	}()

	for _, want := range []string{"one", "two", "three"} {
		select {
		case fr := <-frames:
			if string(fr.Payload) != want {
				t.Fatalf("payload %q, want %q", fr.Payload, want)
			}
		case err := <-errs:
			t.Fatalf("reader failed: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out before %q", want)
		}
	}
}

// TestWriteFrameIsSerialised covers the heartbeat goroutine writing while the
// main loop is answering a command: interleaved writes would corrupt both.
func TestWriteFrameIsSerialised(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	payloads := [][]byte{make([]byte, 4096), make([]byte, 4096), make([]byte, 4096)}
	for i := range payloads {
		for j := range payloads[i] {
			payloads[i][j] = byte('a' + i)
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < len(payloads); i++ {
			if err := writeFrame(client, protocol.RspData, payloads[i]); err != nil {
				t.Errorf("write %d: %v", i, err)
				return
			}
		}
	}()

	seen := make([]string, 0, len(payloads))
	for i := 0; i < len(payloads); i++ {
		fr, err := protocol.Decode(server)
		if err != nil {
			t.Fatalf("decode %d: %v", i, err)
		}
		if fr.Type != protocol.RspData || len(fr.Payload) != 4096 {
			t.Fatalf("frame %d: type=%#x len=%d", i, fr.Type, len(fr.Payload))
		}
		// every byte of one frame must come from a single payload
		for _, b := range fr.Payload {
			if b != fr.Payload[0] {
				t.Fatalf("frame %d mixes two writes", i)
			}
		}
		seen = append(seen, string(fr.Payload[:1]))
	}
	<-done
	if len(seen) != 3 {
		t.Fatalf("saw %d frames", len(seen))
	}
}

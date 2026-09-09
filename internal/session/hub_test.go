package session

import (
	"sync"
	"testing"
	"time"

	"c2-platform/internal/protocol"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestAttachIDPrefix(t *testing.T) {
	h := NewHub(nil)
	s := h.Attach("L1", "1.2.3.4:9", protocol.Register{Hostname: "h"}, func(protocol.Type, []byte) error { return nil })
	if s.ID == "" || s.ID[:2] != "s-" {
		t.Fatalf("id %q", s.ID)
	}
	if s.Status != StatusOnline {
		t.Fatalf("status %s", s.Status)
	}
}

func TestTickStaleThenDead(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	h := NewHub(clk.Now)
	s := h.Attach("L1", "1.2.3.4:9", protocol.Register{}, func(protocol.Type, []byte) error { return nil })
	clk.Add(31 * time.Second)
	h.Tick()
	got := h.Get(s.ID)
	if got == nil || got.Status != StatusStale {
		t.Fatalf("want stale got %+v", got)
	}
	clk.Add(60 * time.Second)
	h.Tick()
	if h.Get(s.ID) != nil {
		t.Fatal("expected removed after dead")
	}
}

func TestSerialExecQueue(t *testing.T) {
	var sent []string
	h := NewHub(nil)
	s := h.Attach("L1", "1.2.3.4:9", protocol.Register{}, func(typ protocol.Type, p []byte) error {
		sent = append(sent, string(p))
		return nil
	})
	t1, err := h.EnqueueExec(s.ID, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	t2, err := h.EnqueueExec(s.ID, "hostname")
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0] != "whoami" {
		t.Fatalf("first send %v", sent)
	}
	h.OnFrame(s.ID, protocol.Frame{Type: protocol.RspData, Payload: []byte("alice\n")})
	h.OnFrame(s.ID, protocol.Frame{Type: protocol.RspEnd})
	if len(sent) != 2 || sent[1] != "hostname" {
		t.Fatalf("second send %v", sent)
	}
	task := h.GetTask(t1)
	if !task.Done || string(task.Output) != "alice\n" {
		t.Fatalf("task1 %+v", task)
	}
	if h.GetTask(t2).Done {
		t.Fatal("task2 should still be in flight")
	}
}

func TestEnqueueMissing(t *testing.T) {
	h := NewHub(nil)
	if _, err := h.EnqueueExec("s-nope", "x"); err != ErrNoSession {
		t.Fatalf("err %v", err)
	}
}

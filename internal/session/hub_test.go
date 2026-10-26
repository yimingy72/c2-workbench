package session

import (
	"strings"
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
	dead := h.Get(s.ID)
	if dead == nil || dead.Status != StatusDead {
		t.Fatalf("want a dead-but-kept session, got %+v", dead)
	}
	// A frame on the same connection must revive it: the implant is alive, it
	// only went quiet (long command, stalled host, network hiccup).
	h.OnFrame(s.ID, protocol.Frame{Type: protocol.CmdHeartbeat})
	if got := h.Get(s.ID); got == nil || got.Status != StatusOnline {
		t.Fatalf("frame after dead did not revive: %+v", got)
	}
	clk.Add(purgeAfter + time.Second)
	h.Tick()
	if h.Get(s.ID) != nil {
		t.Fatal("expected removal after the purge window")
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

func TestEnqueueFsAndScreenTypes(t *testing.T) {
	var types []protocol.Type
	h := NewHub(nil)
	s := h.Attach("L1", "1.2.3.4:9", protocol.Register{}, func(typ protocol.Type, p []byte) error {
		types = append(types, typ)
		return nil
	})
	if _, err := h.Enqueue(s.ID, TaskFsLs, "C:\\"); err != nil {
		t.Fatal(err)
	}
	if len(types) != 1 || types[0] != protocol.CmdFsLs {
		t.Fatalf("types %v", types)
	}
	h.OnFrame(s.ID, protocol.Frame{Type: protocol.RspData, Payload: []byte("D\tWindows\n")})
	h.OnFrame(s.ID, protocol.Frame{Type: protocol.RspEnd})
	if _, err := h.Enqueue(s.ID, TaskScreen, ""); err != nil {
		t.Fatal(err)
	}
	if types[1] != protocol.CmdScreen {
		t.Fatalf("screen %v", types)
	}
}

// TestStalledTaskFreesTheQueue covers the wedged-session case: an old payload
// silently ignores an unknown command, so the task never ends and every later
// command queues behind it forever. Silence must fail the task, not the session.
func TestStalledTaskFreesTheQueue(t *testing.T) {
	clk := &fakeClock{t: time.Unix(2000, 0)}
	h := NewHub(clk.Now)
	var mu sync.Mutex
	var sent []string
	s := h.Attach("L1", "1.2.3.4:9", protocol.Register{}, func(typ protocol.Type, p []byte) error {
		mu.Lock()
		sent = append(sent, string(p))
		mu.Unlock()
		return nil
	})

	// the implant never answers this one, but it keeps heartbeating: the
	// session looks healthy while the task is stuck
	if _, err := h.Enqueue(s.ID, TaskBrowserDeep, ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		clk.Add(10 * time.Second)
		h.OnFrame(s.ID, protocol.Frame{Type: protocol.CmdHeartbeat})
	}
	h.Tick()

	tasks := h.allTasksFor(s.ID)
	if len(tasks) != 1 || !tasks[0].Done || tasks[0].Err == "" {
		t.Fatalf("stalled task not failed: %+v", tasks)
	}
	if tasks[0].Err == "" || !strings.Contains(tasks[0].Err, "无响应") {
		t.Fatalf("error text should explain the stall: %q", tasks[0].Err)
	}
	if got := h.Get(s.ID); got == nil || got.Busy {
		t.Fatalf("session still busy: %+v", got)
	}

	// the queue must move again: a new task is dispatched immediately
	if _, err := h.Enqueue(s.ID, TaskExec, "whoami"); err != nil {
		t.Fatalf("enqueue after stall: %v", err)
	}
	mu.Lock()
	last := sent[len(sent)-1]
	mu.Unlock()
	if last != "whoami" {
		t.Fatalf("next task not dispatched, last frame %q", last)
	}

	// an exec that is genuinely silent for a while is not cut early: it gets the
	// long budget, and only then does the queue move on
	if _, err := h.Enqueue(s.ID, TaskExec, "slow-but-running"); err != nil {
		t.Fatal(err)
	}
	clk.Add(stallBatch + time.Second) // past the batch budget, under the exec one
	h.Tick()
	if got := h.Get(s.ID); got == nil || !got.Busy {
		t.Fatalf("exec was cut too early: %+v", got)
	}
	clk.Add(stallTask)
	h.Tick()
	var who *Task
	for _, x := range h.allTasksFor(s.ID) {
		if x.Cmd == "whoami" {
			who = x
		}
	}
	if who == nil || !who.Done || who.Err == "" {
		t.Fatalf("exec never timed out: %+v", who)
	}
	mu.Lock()
	last = sent[len(sent)-1]
	mu.Unlock()
	if last != "slow-but-running" {
		t.Fatalf("queued task not dispatched after the stall: %q", last)
	}
}

// allTasksFor returns the tasks of one session (map order is random; callers
// select by Cmd or Kind).
func (h *Hub) allTasksFor(sid string) []*Task {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*Task
	for _, t := range h.tasks {
		if t.SessionID == sid {
			out = append(out, t)
		}
	}
	return out
}

// TestProgressingTaskIsNotCut is the other half of the guard: a task that keeps
// streaming rows must survive well past its silence budget.
func TestProgressingTaskIsNotCut(t *testing.T) {
	clk := &fakeClock{t: time.Unix(3000, 0)}
	h := NewHub(clk.Now)
	s := h.Attach("L1", "1.2.3.4:9", protocol.Register{}, func(protocol.Type, []byte) error { return nil })
	tid, err := h.Enqueue(s.ID, TaskCreds, "all")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ { // 5 x 30 s = 150 s, well past the 60 s budget
		clk.Add(30 * time.Second)
		h.OnFrame(s.ID, protocol.Frame{Type: protocol.RspData, Payload: []byte("row\n")})
		h.Tick()
	}
	task := h.GetTask(tid)
	if task == nil || task.Done {
		t.Fatalf("streaming task was cut: %+v", task)
	}
	if got := h.Get(s.ID); got == nil || !got.Busy {
		t.Fatalf("session should still be working: %+v", got)
	}
	// it ends normally once the implant says so
	h.OnFrame(s.ID, protocol.Frame{Type: protocol.RspEnd})
	if task := h.GetTask(tid); task == nil || !task.Done || task.Err != "" {
		t.Fatalf("clean finish mishandled: %+v", task)
	}
}

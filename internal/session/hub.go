package session

import (
	"errors"
	"sync"
	"time"

	"c2-platform/internal/protocol"
)

type Status string

const (
	StatusOnline Status = "online"
	StatusStale  Status = "stale"
	StatusDead   Status = "dead"
)

type Session struct {
	ID          string
	ListenerID  string
	RemoteAddr  string
	Reg         protocol.Register
	LastSeen    time.Time
	Status      Status
	Busy        bool
	currentID   string
	send        func(protocol.Type, []byte) error
	queue       []queued}

type queued struct {
	task *Task
}

type Task struct {
	ID        string
	SessionID string
	Cmd       string
	Output    []byte
	Done      bool
	Err       string
}

type EventKind string

const (
	EventSessionUp    EventKind = "session.up"
	EventSessionStale EventKind = "session.stale"
	EventSessionDown  EventKind = "session.down"
	EventTaskOutput   EventKind = "task.output"
	EventTaskEnd      EventKind = "task.end"
	EventTaskError    EventKind = "task.error"
	EventListenerUp   EventKind = "listener.up"
	EventListenerDown EventKind = "listener.down"
)

type Event struct {
	Kind      EventKind `json:"kind"`
	SessionID string    `json:"session_id,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	Data      string    `json:"data,omitempty"`
	Error     string    `json:"error,omitempty"`
	Listener  string    `json:"listener_id,omitempty"`
}

type Hub struct {
	mu       sync.Mutex
	now      func() time.Time
	n        uint64
	sessions map[string]*Session
	tasks    map[string]*Task
	subs     []chan Event
}

func NewHub(now func() time.Time) *Hub {
	if now == nil {
		now = time.Now
	}
	return &Hub{
		now:      now,
		sessions: map[string]*Session{},
		tasks:    map[string]*Task{},
	}
}

func (h *Hub) nextID(prefix string) string {
	h.n++
	return prefix + itoa36(h.n)
}

func itoa36(n uint64) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = digits[n%36]
		n /= 36
	}
	return string(b[i:])
}

func (h *Hub) Attach(listenerID, remote string, reg protocol.Register, send func(protocol.Type, []byte) error) *Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := &Session{
		ID:         h.nextID("s-"),
		ListenerID: listenerID,
		RemoteAddr: remote,
		Reg:        reg,
		LastSeen:   h.now(),
		Status:     StatusOnline,
		send:       send,
	}
	h.sessions[s.ID] = s
	h.emitLocked(Event{Kind: EventSessionUp, SessionID: s.ID})
	return s
}

func (h *Hub) Detach(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[id]
	if !ok {
		return
	}
	if s.Busy && len(s.queue) > 0 {
		// drop queued
	}
	if s.Busy {
		for _, t := range h.tasks {
			if t.SessionID == id && !t.Done {
				t.Done = true
				t.Err = "session gone"
				h.emitLocked(Event{Kind: EventTaskError, SessionID: id, TaskID: t.ID, Error: t.Err})
			}
		}
	}
	delete(h.sessions, id)
	h.emitLocked(Event{Kind: EventSessionDown, SessionID: id})
}

func (h *Hub) Touch(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[id]
	if !ok {
		return
	}
	s.LastSeen = h.now()
	if s.Status != StatusOnline {
		s.Status = StatusOnline
		h.emitLocked(Event{Kind: EventSessionUp, SessionID: id})
	}
}

func (h *Hub) Tick() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	for id, s := range h.sessions {
		age := now.Sub(s.LastSeen)
		timeout := 30 * time.Second
		deadAfter := 90 * time.Second
		if s.Busy {
			timeout = 90 * time.Second
			deadAfter = 90 * time.Second
		}
		switch {
		case age >= deadAfter:
			s.Status = StatusDead
			delete(h.sessions, id)
			h.emitLocked(Event{Kind: EventSessionDown, SessionID: id})
		case age >= timeout && s.Status == StatusOnline:
			s.Status = StatusStale
			h.emitLocked(Event{Kind: EventSessionStale, SessionID: id})
		}
	}
}

var (
	ErrNoSession = errors.New("session not found")
	ErrOffline   = errors.New("session not online")
)

func (h *Hub) EnqueueExec(sessionID, cmd string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	if !ok {
		return "", ErrNoSession
	}
	if s.Status != StatusOnline {
		return "", ErrOffline
	}
	t := &Task{
		ID:        h.nextID("t-"),
		SessionID: sessionID,
		Cmd:       cmd,
	}
	h.tasks[t.ID] = t
	s.queue = append(s.queue, queued{task: t})
	h.kickLocked(s)
	return t.ID, nil
}

func (h *Hub) kickLocked(s *Session) {
	if s.Busy || len(s.queue) == 0 || s.send == nil {
		return
	}
	q := s.queue[0]
	s.queue = s.queue[1:]
	s.Busy = true
	s.currentID = q.task.ID
	_ = s.send(protocol.CmdExec, []byte(q.task.Cmd))
}

func (h *Hub) OnFrame(sessionID string, f protocol.Frame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	if !ok {
		return
	}
	s.LastSeen = h.now()
	switch f.Type {
	case protocol.CmdHeartbeat, protocol.CmdPing:
		if s.Status != StatusOnline {
			s.Status = StatusOnline
			h.emitLocked(Event{Kind: EventSessionUp, SessionID: sessionID})
		}
	case protocol.RspData:
		t := h.inFlightLocked(s)
		if t == nil {
			return
		}
		t.Output = append(t.Output, f.Payload...)
		h.emitLocked(Event{Kind: EventTaskOutput, SessionID: sessionID, TaskID: t.ID, Data: string(f.Payload)})
	case protocol.RspErr:
		t := h.inFlightLocked(s)
		if t == nil {
			return
		}
		t.Err = string(f.Payload)
		t.Done = true
		s.Busy = false
		s.currentID = ""
		h.emitLocked(Event{Kind: EventTaskError, SessionID: sessionID, TaskID: t.ID, Error: t.Err})
		h.kickLocked(s)
	case protocol.RspEnd:
		t := h.inFlightLocked(s)
		if t == nil {
			return
		}
		t.Done = true
		s.Busy = false
		s.currentID = ""
		h.emitLocked(Event{Kind: EventTaskEnd, SessionID: sessionID, TaskID: t.ID, Data: string(t.Output)})
		h.kickLocked(s)
	}
}

func (h *Hub) inFlightLocked(s *Session) *Task {
	if s.currentID == "" {
		return nil
	}
	return h.tasks[s.currentID]
}

func (h *Hub) GetTask(id string) *Task {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.tasks[id]
	if t == nil {
		return nil
	}
	cp := *t
	cp.Output = append([]byte(nil), t.Output...)
	return &cp
}

func (h *Hub) Get(id string) *Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[id]
	if s == nil {
		return nil
	}
	cp := *s
	cp.queue = nil
	cp.send = nil
	return &cp
}

func (h *Hub) List() []Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Session, 0, len(h.sessions))
	for _, s := range h.sessions {
		cp := *s
		cp.queue = nil
		cp.send = nil
		out = append(out, cp)
	}
	return out
}

func (h *Hub) Subscribe() <-chan Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan Event, 32)
	h.subs = append(h.subs, ch)
	return ch
}

func (h *Hub) emitLocked(e Event) {
	for _, ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

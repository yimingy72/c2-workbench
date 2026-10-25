package listener

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"sync"

	"c2-platform/internal/protocol"
	"c2-platform/internal/session"
)

type Spec struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Bind string `json:"bind"`
	Port int    `json:"port"`
}

type Manager struct {
	mu      sync.Mutex
	n       uint64
	hub     *session.Hub
	list    map[string]*running
	persist string
	loading bool
}

type running struct {
	spec Spec
	ln   net.Listener
	stop chan struct{}
}

func NewManager(h *session.Hub) *Manager {
	return &Manager{hub: h, list: map[string]*running{}}
}

var (
	ErrNotFound = errors.New("listener not found")
	ErrInUse    = errors.New("port in use")
)

func (m *Manager) Start(spec Spec) (Spec, error) {
	if spec.Bind == "" {
		spec.Bind = "0.0.0.0"
	}
	if spec.Port <= 0 || spec.Port > 65535 {
		return Spec{}, fmt.Errorf("invalid port")
	}
	addr := fmt.Sprintf("%s:%d", spec.Bind, spec.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return Spec{}, ErrInUse
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.n++
	spec.ID = fmt.Sprintf("l-%d", m.n)
	if spec.Name == "" {
		spec.Name = fmt.Sprintf("tcp-%d", spec.Port)
	}
	r := &running{spec: spec, ln: ln, stop: make(chan struct{})}
	m.list[spec.ID] = r
	go m.serve(r)
	m.saveLocked()
	return spec, nil
}

func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	r, ok := m.list[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	delete(m.list, id)
	m.saveLocked()
	m.mu.Unlock()
	close(r.stop)
	_ = r.ln.Close()
	return nil
}

func (m *Manager) List() []Spec {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Spec, 0, len(m.list))
	for _, r := range m.list {
		out = append(out, r.spec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (m *Manager) SetPersist(path string) {
	m.mu.Lock()
	m.persist = path
	m.mu.Unlock()
}

func (m *Manager) Restore() {
	m.mu.Lock()
	path := m.persist
	m.mu.Unlock()
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return
	}
	var specs []Spec
	if json.Unmarshal(b, &specs) != nil {
		return
	}
	m.mu.Lock()
	m.loading = true
	m.mu.Unlock()
	for _, s := range specs {
		if _, err := m.Start(Spec{Name: s.Name, Bind: s.Bind, Port: s.Port}); err != nil {
			log.Printf("restore listener %s:%d: %v", s.Bind, s.Port, err)
		}
	}
	m.mu.Lock()
	m.loading = false
	m.saveLocked()
	m.mu.Unlock()
}

func (m *Manager) saveLocked() {
	if m.persist == "" || m.loading {
		return
	}
	out := make([]Spec, 0, len(m.list))
	for _, r := range m.list {
		out = append(out, r.spec)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(m.persist), 0o755)
	_ = os.WriteFile(m.persist, b, 0o600)
}

func (m *Manager) serve(r *running) {
	for {
		c, err := r.ln.Accept()
		if err != nil {
			select {
			case <-r.stop:
				return
			default:
				return
			}
		}
		go m.handle(r.spec.ID, c)
	}
}

func (m *Manager) handle(listenerID string, c net.Conn) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("session panic: %v\n%s", rec, debug.Stack())
		}
		c.Close()
	}()
	f, err := protocol.Decode(c)
	if err != nil {
		return
	}
	if f.Type != protocol.CmdRegister {
		return
	}
	reg, err := protocol.ParseRegister(f.Payload)
	if err != nil {
		return
	}
	var mu sync.Mutex
	send := func(typ protocol.Type, payload []byte) error {
		mu.Lock()
		defer mu.Unlock()
		_, err := c.Write(protocol.Encode(typ, payload))
		return err
	}
	s := m.hub.Attach(listenerID, c.RemoteAddr().String(), reg, send)
	sid := s.ID
	defer m.hub.Detach(sid)
	if err := send(protocol.RspOK, []byte(sid)); err != nil {
		return
	}
	for {
		f, err := protocol.Decode(c)
		if err != nil {
			return
		}
		switch f.Type {
		case protocol.CmdHeartbeat, protocol.CmdPing, protocol.RspData, protocol.RspErr, protocol.RspEnd, protocol.RspShell, protocol.RspFrame:
			m.hub.OnFrame(sid, f)
		default:
		}
	}
}

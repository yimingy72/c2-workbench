package listener

import (
	"errors"
	"fmt"
	"net"
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
	mu   sync.Mutex
	n    uint64
	hub  *session.Hub
	list map[string]*running
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
		spec.Name = spec.ID
	}
	r := &running{spec: spec, ln: ln, stop: make(chan struct{})}
	m.list[spec.ID] = r
	go m.serve(r)
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
	return out
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
	defer c.Close()
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
		case protocol.CmdHeartbeat, protocol.CmdPing, protocol.RspData, protocol.RspErr, protocol.RspEnd:
			m.hub.OnFrame(sid, f)
		default:
		}
	}
}

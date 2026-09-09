package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"c2-platform/internal/auth"
	"c2-platform/internal/config"
	"c2-platform/internal/listener"
	"c2-platform/internal/session"

	"github.com/gorilla/websocket"
)

type Server struct {
	cfg *config.Config
	hub *session.Hub
	lm  *listener.Manager
	au  *auth.Service
	ui  http.Handler
}

func New(cfg *config.Config, hub *session.Hub, lm *listener.Manager, ui http.Handler) http.Handler {
	s := &Server{
		cfg: cfg,
		hub: hub,
		lm:  lm,
		au:  auth.New(cfg.Username, cfg.Password, cfg.JWTSecret),
		ui:  ui,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("GET /api/me", s.auth(s.me))
	mux.HandleFunc("GET /api/listeners", s.auth(s.listListeners))
	mux.HandleFunc("POST /api/listeners", s.auth(s.createListener))
	mux.HandleFunc("DELETE /api/listeners/{id}", s.auth(s.deleteListener))
	mux.HandleFunc("GET /api/sessions", s.auth(s.listSessions))
	mux.HandleFunc("GET /api/sessions/{id}", s.auth(s.getSession))
	mux.HandleFunc("POST /api/sessions/{id}/exec", s.auth(s.exec))
	mux.HandleFunc("GET /api/sessions/{id}/tasks/{tid}", s.auth(s.getTask))
	mux.HandleFunc("GET /api/ws", s.ws)
	if ui != nil {
		mux.Handle("/", ui)
	}
	return mux
}

type apiErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiErr{Code: status, Message: msg})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 422, "invalid json")
		return
	}
	tok, err := s.au.Login(body.Username, body.Password)
	if err != nil {
		writeErr(w, 401, "unauthorized")
		return
	}
	writeJSON(w, 200, map[string]string{"token": tok})
}

func (s *Server) tokenFrom(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.au.Parse(s.tokenFrom(r)); err != nil {
			writeErr(w, 401, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u, _ := s.au.Parse(s.tokenFrom(r))
	writeJSON(w, 200, map[string]string{"username": u, "title": s.cfg.WebTitle})
}

func (s *Server) listListeners(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.lm.List())
}

func (s *Server) createListener(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Bind string `json:"bind"`
		Port int    `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 422, "invalid json")
		return
	}
	spec, err := s.lm.Start(listener.Spec{Name: body.Name, Bind: body.Bind, Port: body.Port})
	if err != nil {
		if errors.Is(err, listener.ErrInUse) {
			writeErr(w, 409, "port in use")
			return
		}
		writeErr(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, spec)
}

func (s *Server) deleteListener(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.lm.Stop(id); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

type sessionDTO struct {
	ID         string `json:"id"`
	ListenerID string `json:"listener_id"`
	RemoteAddr string `json:"remote_addr"`
	Hostname   string `json:"hostname"`
	Username   string `json:"username"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	PID        uint32 `json:"pid"`
	IP         string `json:"ip"`
	LastSeen   int64  `json:"last_seen"`
	Status     string `json:"status"`
}

func toDTO(s session.Session) sessionDTO {
	return sessionDTO{
		ID:         s.ID,
		ListenerID: s.ListenerID,
		RemoteAddr: s.RemoteAddr,
		Hostname:   s.Reg.Hostname,
		Username:   s.Reg.Username,
		OS:         s.Reg.OS,
		Arch:       s.Reg.Arch,
		PID:        s.Reg.PID,
		IP:         s.Reg.IP,
		LastSeen:   s.LastSeen.Unix(),
		Status:     string(s.Status),
	}
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	list := s.hub.List()
	out := make([]sessionDTO, 0, len(list))
	for _, x := range list {
		out = append(out, toDTO(x))
	}
	writeJSON(w, 200, out)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	got := s.hub.Get(id)
	if got == nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, toDTO(*got))
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Cmd string `json:"cmd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Cmd) == "" {
		writeErr(w, 422, "cmd required")
		return
	}
	tid, err := s.hub.EnqueueExec(id, body.Cmd)
	if err != nil {
		if errors.Is(err, session.ErrNoSession) {
			writeErr(w, 404, "not found")
			return
		}
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"task_id": tid})
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	tid := r.PathValue("tid")
	t := s.hub.GetTask(tid)
	if t == nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]any{
		"id":         t.ID,
		"session_id": t.SessionID,
		"cmd":        t.Cmd,
		"output":     string(t.Output),
		"done":       t.Done,
		"error":      t.Err,
	})
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	if _, err := s.au.Parse(s.tokenFrom(r)); err != nil {
		writeErr(w, 401, "unauthorized")
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	ch := s.hub.Subscribe()
	var mu sync.Mutex
	for ev := range ch {
		mu.Lock()
		err := conn.WriteJSON(ev)
		mu.Unlock()
		if err != nil {
			return
		}
	}
}

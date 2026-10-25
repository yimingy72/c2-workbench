package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"c2-platform/internal/xfer"
)

func (s *Server) xferList(w http.ResponseWriter, r *http.Request) {
	if s.xf == nil {
		writeJSON(w, 200, []xfer.Job{})
		return
	}
	writeJSON(w, 200, s.xf.List(r.PathValue("id")))
}

func (s *Server) xferStart(w http.ResponseWriter, r *http.Request) {
	if s.xf == nil {
		writeErr(w, 500, "xfer unavailable")
		return
	}
	id := r.PathValue("id")
	var body struct {
		Dir  string `json:"dir"`
		Path string `json:"path"`
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Path == "" {
		writeErr(w, 422, "path required")
		return
	}
	var (
		job xfer.Job
		err error
	)
	switch body.Dir {
	case "upload":
		job, err = s.xf.StartUpload(id, body.Path, body.Name, body.Size)
	default:
		job, err = s.xf.StartDownload(id, body.Path, body.Name, body.Size)
	}
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, job)
}

func (s *Server) xferClear(w http.ResponseWriter, r *http.Request) {
	if s.xf != nil {
		s.xf.ClearDone(r.PathValue("id"))
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) xferPause(w http.ResponseWriter, r *http.Request) {
	s.xferAct(w, r, func(id string) (xfer.Job, error) { return s.xf.Pause(id) })
}

func (s *Server) xferResume(w http.ResponseWriter, r *http.Request) {
	s.xferAct(w, r, func(id string) (xfer.Job, error) { return s.xf.Resume(id) })
}

func (s *Server) xferCancel(w http.ResponseWriter, r *http.Request) {
	if s.xf == nil {
		writeErr(w, 500, "xfer unavailable")
		return
	}
	if err := s.xf.Cancel(r.PathValue("jid")); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) xferAct(w http.ResponseWriter, r *http.Request, fn func(string) (xfer.Job, error)) {
	if s.xf == nil {
		writeErr(w, 500, "xfer unavailable")
		return
	}
	job, err := fn(r.PathValue("jid"))
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, job)
}

func (s *Server) xferChunk(w http.ResponseWriter, r *http.Request) {
	if s.xf == nil {
		writeErr(w, 500, "xfer unavailable")
		return
	}
	var body struct {
		Offset int64  `json:"offset"`
		Data   string `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 422, "invalid json")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil {
		writeErr(w, 422, "invalid data")
		return
	}
	job, err := s.xf.Push(r.PathValue("jid"), body.Offset, raw)
	if err != nil {
		if errors.Is(err, xfer.ErrNotFound) {
			writeErr(w, 404, "not found")
			return
		}
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, job)
}

func (s *Server) xferFile(w http.ResponseWriter, r *http.Request) {
	if s.xf == nil {
		writeErr(w, 404, "not found")
		return
	}
	job, f, err := s.xf.File(r.PathValue("jid"))
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", contentDisposition(job.Name))
	if st != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	}
	w.WriteHeader(200)
	_, _ = io.Copy(w, f)
}

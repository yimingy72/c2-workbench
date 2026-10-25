package xfer

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"c2-platform/internal/session"
	"c2-platform/internal/stash"
)

const (
	Chunk        = 256 * 1024
	StatusRun    = "running"
	StatusPause  = "paused"
	StatusDone   = "done"
	StatusErr    = "error"
	StatusCancel = "canceled"
)

var (
	ErrNotFound = errors.New("xfer not found")
	ErrBadOff   = errors.New("offset mismatch")
)

type Job struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Dir       string `json:"dir"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Offset    int64  `json:"offset"`
	Total     int64  `json:"total"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	Speed     int64  `json:"speed"`
	At        int64  `json:"at"`
}

type Manager struct {
	hub   *session.Hub
	stash *stash.Store
	dir   string
	mu    sync.Mutex
	n     uint64
	jobs  map[string]*live
}

type live struct {
	Job
	pause bool
	stop  bool
	part  string
	mark  time.Time
	got   int64
}

func New(hub *session.Hub, st *stash.Store, dir string) *Manager {
	_ = os.MkdirAll(dir, 0o755)
	return &Manager{hub: hub, stash: st, dir: dir, jobs: map[string]*live{}}
}

func (m *Manager) List(sessionID string) []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0)
	for _, j := range m.jobs {
		if sessionID == "" || j.SessionID == sessionID {
			out = append(out, j.Job)
		}
	}
	return out
}

func (m *Manager) Get(id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	return j.Job, nil
}

func (m *Manager) StartDownload(sessionID, path, name string, total int64) (Job, error) {
	if path == "" {
		return Job{}, errors.New("path required")
	}
	if name == "" {
		name = filepath.Base(path)
	}
	j := m.newJob(sessionID, "download", path, name, total)
	go m.runDownload(j.ID)
	return j.Job, nil
}

func (m *Manager) StartUpload(sessionID, path, name string, total int64) (Job, error) {
	if path == "" {
		return Job{}, errors.New("path required")
	}
	if name == "" {
		name = filepath.Base(path)
	}
	j := m.newJob(sessionID, "upload", path, name, total)
	return j.Job, nil
}

func (m *Manager) Pause(id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	if j.Status == StatusRun {
		j.pause = true
		j.Status = StatusPause
	}
	m.emitLocked(j)
	return j.Job, nil
}

func (m *Manager) Resume(id string) (Job, error) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return Job{}, ErrNotFound
	}
	if j.Status != StatusPause && j.Status != StatusErr {
		out := j.Job
		m.mu.Unlock()
		return out, nil
	}
	j.pause = false
	j.stop = false
	j.Status = StatusRun
	j.Error = ""
	dir := j.Dir
	m.emitLocked(j)
	m.mu.Unlock()
	if dir == "download" {
		go m.runDownload(id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id].Job, nil
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return ErrNotFound
	}
	j.stop = true
	j.pause = true
	j.Status = StatusCancel
	if j.part != "" {
		_ = os.Remove(j.part)
	}
	m.emitLocked(j)
	return nil
}

func (m *Manager) ClearDone(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, j := range m.jobs {
		if sessionID != "" && j.SessionID != sessionID {
			continue
		}
		if j.Status == StatusDone || j.Status == StatusCancel || j.Status == StatusErr {
			if j.part != "" && j.Status != StatusDone {
				_ = os.Remove(j.part)
			}
			delete(m.jobs, id)
		}
	}
}

func (m *Manager) Push(id string, offset int64, data []byte) (Job, error) {
	if len(data) == 0 {
		return Job{}, errors.New("empty chunk")
	}
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return Job{}, ErrNotFound
	}
	if j.Dir != "upload" {
		m.mu.Unlock()
		return Job{}, errors.New("not an upload")
	}
	if j.Status == StatusCancel {
		m.mu.Unlock()
		return Job{}, errors.New("canceled")
	}
	if offset < j.Offset {
		out := j.Job
		m.mu.Unlock()
		return out, nil
	}
	if offset != j.Offset {
		m.mu.Unlock()
		return Job{}, ErrBadOff
	}
	if j.pause {
		j.Status = StatusPause
		out := j.Job
		m.mu.Unlock()
		return out, nil
	}
	j.Status = StatusRun
	sid, path := j.SessionID, j.Path
	m.mu.Unlock()

	tid, err := m.hub.PutFileAt(sid, path, offset, data)
	if err != nil {
		return m.fail(id, err)
	}
	if err := waitTask(m.hub, tid, 90*time.Second); err != nil {
		return m.fail(id, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok = m.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	now := time.Now()
	dt := now.Sub(j.mark).Seconds()
	j.Offset = offset + int64(len(data))
	if dt > 0 {
		j.Speed = int64(float64(len(data)) / dt)
	}
	j.mark = now
	if j.Total > 0 && j.Offset >= j.Total {
		j.Status = StatusDone
		j.Speed = 0
	}
	m.emitLocked(j)
	return j.Job, nil
}

func (m *Manager) File(id string) (Job, *os.File, error) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return Job{}, nil, ErrNotFound
	}
	part := j.part
	out := j.Job
	m.mu.Unlock()
	if part == "" {
		return out, nil, os.ErrNotExist
	}
	f, err := os.Open(part)
	return out, f, err
}

func (m *Manager) newJob(sessionID, dir, path, name string, total int64) *live {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.n++
	id := fmt.Sprintf("x-%d", m.n)
	sum := sha1.Sum([]byte(sessionID + "\n" + dir + "\n" + path))
	part := filepath.Join(m.dir, sessionID, hex.EncodeToString(sum[:8])+".part")
	_ = os.MkdirAll(filepath.Dir(part), 0o755)
	var offset int64
	if dir == "download" {
		if st, err := os.Stat(part); err == nil {
			offset = st.Size()
		}
	}
	j := &live{
		Job: Job{
			ID: id, SessionID: sessionID, Dir: dir, Name: name, Path: path,
			Offset: offset, Total: total, Status: StatusRun, At: time.Now().Unix(),
		},
		part: part,
		mark: time.Now(),
	}
	if total > 0 && offset >= total && dir == "download" {
		j.Status = StatusDone
	}
	m.jobs[id] = j
	m.emitLocked(j)
	return j
}

func (m *Manager) runDownload(id string) {
	for {
		m.mu.Lock()
		j, ok := m.jobs[id]
		if !ok || j.stop || j.pause || j.Status != StatusRun {
			m.mu.Unlock()
			return
		}
		if j.Total > 0 && j.Offset >= j.Total {
			j.Status = StatusDone
			m.emitLocked(j)
			part, sid, name, remote := j.part, j.SessionID, j.Name, j.Path
			m.mu.Unlock()
			m.finishDownload(sid, name, remote, part)
			return
		}
		off, total, path, part, sid := j.Offset, j.Total, j.Path, j.part, j.SessionID
		n := int64(Chunk)
		if total > 0 && off+n > total {
			n = total - off
		}
		m.mu.Unlock()
		if n <= 0 {
			m.mu.Lock()
			if jj, ok := m.jobs[id]; ok {
				jj.Status = StatusDone
				m.emitLocked(jj)
			}
			m.mu.Unlock()
			m.finishDownload(sid, filepath.Base(path), path, part)
			return
		}
		tid, err := m.hub.EnqueueGet(sid, path, off, n)
		if err != nil {
			_, _ = m.fail(id, err)
			return
		}
		if err := waitTask(m.hub, tid, 90*time.Second); err != nil {
			_, _ = m.fail(id, err)
			return
		}
		t := m.hub.GetTask(tid)
		if t == nil {
			_, _ = m.fail(id, errors.New("task lost"))
			return
		}
		if len(t.Output) > 0 {
			if err := appendFile(part, t.Output); err != nil {
				_, _ = m.fail(id, err)
				return
			}
		}
		m.mu.Lock()
		j, ok = m.jobs[id]
		if !ok {
			m.mu.Unlock()
			return
		}
		now := time.Now()
		dt := now.Sub(j.mark).Seconds()
		got := int64(len(t.Output))
		j.Offset += got
		if dt > 0 {
			j.Speed = int64(float64(got) / dt)
		}
		j.mark = now
		if got == 0 || (j.Total > 0 && j.Offset >= j.Total) {
			j.Status = StatusDone
			j.Speed = 0
			m.emitLocked(j)
			part, sid, name, remote := j.part, j.SessionID, j.Name, j.Path
			m.mu.Unlock()
			m.finishDownload(sid, name, remote, part)
			return
		}
		m.emitLocked(j)
		m.mu.Unlock()
	}
}

func (m *Manager) finishDownload(sid, name, remote, part string) {
	if m.stash == nil || part == "" {
		return
	}
	_, _ = m.stash.PutFile(sid, name, remote, part)
}

func (m *Manager) fail(id string, err error) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	j.Status = StatusErr
	j.Error = err.Error()
	m.emitLocked(j)
	return j.Job, err
}

func (m *Manager) emitLocked(j *live) {
	if m.hub == nil {
		return
	}
	b, _ := json.Marshal(j.Job)
	m.hub.Emit(session.Event{Kind: session.EventXfer, SessionID: j.SessionID, Data: string(b)})
}

func appendFile(path string, data []byte) error {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

func waitTask(h *session.Hub, tid string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		t := h.GetTask(tid)
		if t != nil && t.Done {
			if t.Err != "" {
				return errors.New(t.Err)
			}
			return nil
		}
		time.Sleep(40 * time.Millisecond)
	}
	return errors.New("超时")
}

func CopyFile(dst io.Writer, srcPath string) (int64, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(dst, f)
}

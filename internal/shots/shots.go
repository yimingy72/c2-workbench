package shots

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Item struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Remark    string `json:"remark"`
	W         int    `json:"w"`
	H         int    `json:"h"`
	Size      int    `json:"size"`
	At        int64  `json:"at"`
}

type Store struct {
	mu   sync.Mutex
	dir  string
	list []Item
}

func New(dir string) *Store {
	s := &Store{dir: dir}
	_ = os.MkdirAll(dir, 0o755)
	b, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err == nil {
		_ = json.Unmarshal(b, &s.list)
	}
	return s
}

func (s *Store) List(sessionID string) []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Item, 0)
	for _, it := range s.list {
		if sessionID == "" || it.SessionID == sessionID {
			out = append(out, it)
		}
	}
	return out
}

func (s *Store) Put(sessionID, remark string, w, h int, data []byte) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := Item{
		ID: time.Now().Format("20060102150405.000000"), SessionID: sessionID,
		Remark: remark, W: w, H: h, Size: len(data), At: time.Now().Unix(),
	}
	if err := os.WriteFile(filepath.Join(s.dir, it.ID+".jpg"), data, 0o600); err != nil {
		return Item{}, err
	}
	s.list = append([]Item{it}, s.list...)
	if len(s.list) > 80 {
		old := s.list[80:]
		s.list = s.list[:80]
		for _, o := range old {
			_ = os.Remove(filepath.Join(s.dir, o.ID+".jpg"))
		}
	}
	return it, s.flushLocked()
}

func (s *Store) File(id string) (Item, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.list {
		if it.ID == id {
			b, err := os.ReadFile(filepath.Join(s.dir, id+".jpg"))
			return it, b, err
		}
	}
	return Item{}, nil, os.ErrNotExist
}

func (s *Store) SetRemark(id, remark string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.list {
		if s.list[i].ID == id {
			s.list[i].Remark = remark
			return s.flushLocked()
		}
	}
	return os.ErrNotExist
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.list[:0]
	found := false
	for _, it := range s.list {
		if it.ID == id {
			found = true
			_ = os.Remove(filepath.Join(s.dir, id+".jpg"))
			continue
		}
		out = append(out, it)
	}
	if !found {
		return os.ErrNotExist
	}
	s.list = out
	return s.flushLocked()
}

func (s *Store) DeleteSession(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.list[:0]
	for _, it := range s.list {
		if it.SessionID == sessionID {
			_ = os.Remove(filepath.Join(s.dir, it.ID+".jpg"))
			continue
		}
		out = append(out, it)
	}
	s.list = out
	return s.flushLocked()
}

func (s *Store) flushLocked() error {
	b, err := json.MarshalIndent(s.list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "index.json"), b, 0o600)
}

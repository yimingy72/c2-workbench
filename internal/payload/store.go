package payload

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Record struct {
	ID         string   `json:"id"`
	Name       string   `json:"name,omitempty"`
	Host       string   `json:"host"`
	Port       int      `json:"port"`
	Filename   string   `json:"filename"`
	Target     string   `json:"target"`
	OS         string   `json:"os,omitempty"`
	Arch       string   `json:"arch,omitempty"`
	Format     string   `json:"format,omitempty"`
	Language   string   `json:"language,omitempty"`
	SHA256     string   `json:"sha256,omitempty"`
	Token      string   `json:"token,omitempty"`
	Techniques []string `json:"techniques,omitempty"`
	Size       int      `json:"size"`
	At         int64    `json:"at"`
}

type Store struct {
	mu   sync.Mutex
	dir  string
	list []Record
}

func NewStore(dir string) *Store {
	s := &Store{dir: dir}
	_ = os.MkdirAll(dir, 0o755)
	b, err := os.ReadFile(filepath.Join(dir, "history.json"))
	if err == nil {
		_ = json.Unmarshal(b, &s.list)
	}
	return s
}

func (s *Store) List() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, len(s.list))
	copy(out, s.list)
	return out
}

func (s *Store) Put(rec Record, data []byte) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.ID == "" {
		rec.ID = time.Now().Format("20060102150405")
	}
	if rec.At == 0 {
		rec.At = time.Now().Unix()
	}
	if rec.Token == "" {
		var btok [16]byte
		_, _ = rand.Read(btok[:])
		rec.Token = hex.EncodeToString(btok[:])
	}
	if rec.Format == "" {
		rec.Format = FormatOf(rec.Target)
	}
	if rec.Name == "" {
		rec.Name = rec.Filename
	}
	sum := sha256.Sum256(data)
	rec.SHA256 = hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(s.dir, rec.ID+".bin"), data, 0o600); err != nil {
		return Record{}, err
	}
	rec.Size = len(data)
	s.list = append([]Record{rec}, s.list...)
	if len(s.list) > 50 {
		old := s.list[50:]
		s.list = s.list[:50]
		for _, o := range old {
			_ = os.Remove(filepath.Join(s.dir, o.ID+".bin"))
		}
	}
	b, _ := json.MarshalIndent(s.list, "", "  ")
	_ = os.WriteFile(filepath.Join(s.dir, "history.json"), b, 0o600)
	return rec, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.list[:0]
	found := false
	for _, r := range s.list {
		if r.ID == id {
			found = true
			_ = os.Remove(filepath.Join(s.dir, id+".bin"))
			continue
		}
		out = append(out, r)
	}
	if !found {
		return os.ErrNotExist
	}
	s.list = out
	b, _ := json.MarshalIndent(s.list, "", "  ")
	return os.WriteFile(filepath.Join(s.dir, "history.json"), b, 0o600)
}

func (s *Store) File(id string) (Record, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.list {
		if r.ID == id {
			b, err := os.ReadFile(filepath.Join(s.dir, id+".bin"))
			return r, b, err
		}
	}
	return Record{}, nil, os.ErrNotExist
}

func (s *Store) FileByToken(token string) (Record, []byte, error) {
	if token == "" {
		return Record{}, nil, os.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.list {
		if r.Token == token {
			b, err := os.ReadFile(filepath.Join(s.dir, r.ID+".bin"))
			return r, b, err
		}
	}
	return Record{}, nil, os.ErrNotExist
}

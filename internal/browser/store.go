// Package browser keeps per-host snapshots of the browser inventory report so
// the console can show what changed between scans.
package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const maxSnapshots = 20

type Snapshot struct {
	At   int64    `json:"at"`
	Rows []string `json:"rows"`
}

type Store struct {
	mu  sync.Mutex
	dir string
}

func New(dir string) *Store {
	_ = os.MkdirAll(dir, 0o755)
	return &Store{dir: dir}
}

func (s *Store) path(host string) string {
	host = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(host))
	if host == "" {
		host = "unknown"
	}
	return filepath.Join(s.dir, host+".json")
}

// Put appends one snapshot (newest first) and trims to maxSnapshots.
func (s *Store) Put(host, rows string, at int64) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{At: at}
	for _, line := range strings.Split(rows, "\n") {
		if strings.TrimSpace(line) != "" {
			snap.Rows = append(snap.Rows, line)
		}
	}
	list := s.load(host)
	list = append([]Snapshot{snap}, list...)
	if len(list) > maxSnapshots {
		list = list[:maxSnapshots]
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return snap, err
	}
	return snap, os.WriteFile(s.path(host), b, 0o600)
}

func (s *Store) load(host string) []Snapshot {
	var list []Snapshot
	b, err := os.ReadFile(s.path(host))
	if err == nil {
		_ = json.Unmarshal(b, &list)
	}
	return list
}

func (s *Store) List(host string) []Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.load(host)
	sort.Slice(list, func(i, j int) bool { return list[i].At > list[j].At })
	return list
}

// rowKey ignores volatile fields (timestamp, size) so a touched History file is
// not reported as removed+added on every scan.
func rowKey(row string) string {
	f := strings.Split(row, "\t")
	if len(f) < 4 {
		return row
	}
	// bookmarks carry their identity in the url column
	if f[0] == "B" && len(f) > 4 {
		return strings.Join(f[:5], "\t")
	}
	return strings.Join(f[:4], "\t")
}

// Diff reports rows that appeared (+) or disappeared (-) between two snapshots.
func Diff(older, newer []string) (added, removed []string) {
	old := map[string]bool{}
	for _, r := range older {
		old[rowKey(r)] = true
	}
	cur := map[string]bool{}
	for _, r := range newer {
		cur[rowKey(r)] = true
		if !old[rowKey(r)] {
			added = append(added, r)
		}
	}
	for _, r := range older {
		if !cur[rowKey(r)] {
			removed = append(removed, r)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

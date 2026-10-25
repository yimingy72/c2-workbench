package browser

import (
	"os/exec"
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

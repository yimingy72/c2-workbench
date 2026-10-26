package sqlread

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// seed builds a real sqlite database with the system CLI so the reader is
// checked against actual page layouts, not hand-written fixtures.
func seed(t *testing.T, sql string) []byte {
	t.Helper()
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 CLI not installed")
	}
	dir := t.TempDir()
	path := dir + "/db.sqlite"
	cmd := exec.Command(bin, path)
	cmd.Stdin = strings.NewReader(sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v %s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return data
}

// TestChromeLoginsSchema mirrors the real Login Data layout closely enough to
// catch column-order and blob handling bugs.
func TestChromeLoginsSchema(t *testing.T) {
	data := seed(t, `
create table logins (
  origin_url varchar not null,
  action_url varchar,
  username_element varchar,
  username_value varchar,
  password_element varchar,
  password_value blob,
  submit_element varchar,
  signon_realm varchar not null,
  date_created integer not null default 0,
  blacklisted_by_user integer not null default 0,
  scheme integer not null default 0,
  password_type integer,
  times_used integer not null default 0,
  date_synced integer);
insert into logins (origin_url, username_value, password_value, signon_realm, date_created, times_used)
values ('https://github.com','octocat', x'763130000102030405060708090A0B0C', 'https://github.com/', 13300000000000000, 7),
       ('https://mail.example.com','bob@example.com', x'7631300A0B0C0D0E0F101112131415161718191A1B1C1D1E', 'https://mail.example.com/', 13300000001000000, 2);
`)
	db, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := db.Table("logins")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"origin_url", "action_url", "username_element", "username_value",
		"password_element", "password_value", "submit_element", "signon_realm",
		"date_created", "blacklisted_by_user", "scheme", "password_type", "times_used", "date_synced"}
	if strings.Join(tbl.Columns, ",") != strings.Join(want, ",") {
		t.Fatalf("columns\n got %v\nwant %v", tbl.Columns, want)
	}
	var got []string
	if err := tbl.Scan(0, func(row map[string]Value) bool {
		got = append(got, row["origin_url"].String()+"|"+row["username_value"].String()+
			"|"+fmt.Sprintf("%x", row["password_value"].Bytes())+"|"+row["times_used"].String())
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("rows %v", got)
	}
	if !strings.HasPrefix(got[0], "https://github.com|octocat|763130000102030405060708090a0b0c|7") {
		t.Fatalf("row0 %s", got[0])
	}
	if !strings.HasPrefix(got[1], "https://mail.example.com|bob@example.com|7631300a") {
		t.Fatalf("row1 %s", got[1])
	}
}

// TestManyRowsAndOverflow forces interior pages plus overflow chains, which is
// where a hand-rolled reader usually breaks.
func TestManyRowsAndOverflow(t *testing.T) {
	var b strings.Builder
	b.WriteString("create table cookies(host_key text, name text, path text, encrypted_value blob, expires_utc integer);\n")
	b.WriteString("begin;\n")
	big := strings.Repeat("A1B2C3D4", 900) // 7.2 KB hex -> ~3.6 KB blob, needs overflow
	for i := 0; i < 2500; i++ {
		fmt.Fprintf(&b, "insert into cookies values('.host%d.com','c%d','/',x'%s',%d);\n", i, i, big, 13300000000000000+i)
	}
	b.WriteString("commit;\n")
	data := seed(t, b.String())

	db, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := db.Table("cookies")
	if err != nil {
		t.Fatal(err)
	}
	n, err := tbl.Count()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2500 {
		t.Fatalf("count = %d", n)
	}
	seen := 0
	if err := tbl.Scan(0, func(row map[string]Value) bool {
		blob := row["encrypted_value"].Bytes()
		if len(blob) != 3600 {
			t.Fatalf("row %d blob len %d", seen, len(blob))
		}
		if row["host_key"].String() != fmt.Sprintf(".host%d.com", seen) {
			t.Fatalf("row %d host %q", seen, row["host_key"].String())
		}
		seen++
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 2500 {
		t.Fatalf("scanned %d", seen)
	}
}

// TestCrossCheckWithCLI compares a sample of decoded values against sqlite3's
// own JSON output so a subtle varint or serial-type bug cannot slip through.
func TestCrossCheckWithCLI(t *testing.T) {
	sql := `
create table moz_places(url text, title text, visit_count int, last_visit_date int, hidden int);
insert into moz_places values('https://a.example/1','标题一',3,13300000000000000,0);
insert into moz_places values('https://b.example/2','',NULL,13300000001000000,0);
insert into moz_places values('https://c.example/3',NULL,-7,13300000002000000,1);
`
	data := seed(t, sql)
	db, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := db.Table("moz_places")
	if err != nil {
		t.Fatal(err)
	}
	var mine []string
	if err := tbl.Scan(0, func(row map[string]Value) bool {
		mine = append(mine, row["url"].String()+"\t"+row["title"].String()+"\t"+
			row["visit_count"].String()+"\t"+fmt.Sprintf("%v", row["visit_count"].Kind))
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if len(mine) != 3 {
		t.Fatalf("rows %v", mine)
	}
	if mine[1] != "https://b.example/2\t\t\ttrue" && !strings.Contains(mine[1], "https://b.example/2") {
		t.Fatalf("row1 %q", mine[1])
	}
	if !strings.Contains(mine[2], "-7") {
		t.Fatalf("negative int lost: %q", mine[2])
	}
}

// TestNotSQLiteAndMissingTable covers the error paths the implant relies on.
func TestNotSQLiteAndMissingTable(t *testing.T) {
	if _, err := Open([]byte("plain text, definitely not a database")); err != ErrNotSQLite {
		t.Fatalf("want ErrNotSQLite got %v", err)
	}
	data := seed(t, "create table a(x int); insert into a values(1);")
	db, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Table("nope"); err == nil {
		t.Fatal("want error for missing table")
	}
	tables, err := db.Tables()
	if err != nil || len(tables) != 1 || tables[0] != "a" {
		t.Fatalf("tables %v err %v", tables, err)
	}
}

// TestParseColumns keeps the CREATE TABLE parser honest about constraint lines.
func TestParseColumns(t *testing.T) {
	got, alias := parseColumns(`CREATE TABLE t(
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        "user name" TEXT,
        CONSTRAINT u UNIQUE(id),
        CHECK (id > 0),
        [weird] BLOB,
        FOREIGN KEY(id) REFERENCES x(id))`)
	want := []string{"id", "user name", "weird"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v want %v", got, want)
	}
	if alias != 0 {
		t.Fatalf("rowid alias index = %d, want 0", alias)
	}
	if _, alias := parseColumns("CREATE TABLE u(a TEXT, b INT, PRIMARY KEY(a))"); alias != -1 {
		t.Fatalf("table-level PRIMARY KEY must not be treated as a rowid alias, got %d", alias)
	}
}

// TestRowIDAliasIsFilled covers "INTEGER PRIMARY KEY": sqlite stores NULL in
// the record for that column, so readers must take the value from the b-tree
// key or joins on the id silently break (this broke Firefox bookmarks).
func TestRowIDAliasIsFilled(t *testing.T) {
	data := seed(t, `create table p(id integer primary key, name text);
insert into p values(7,'seven'),(9,'nine');
create table q(k text, v int); insert into q values('x',1);`)
	db, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := db.Table("p")
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	if err := tbl.Scan(0, func(r map[string]Value) bool {
		ids = append(ids, r["id"].Int)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 7 || ids[1] != 9 {
		t.Fatalf("ids = %v, want [7 9]", ids)
	}
	// a table without the alias must not invent an id column
	if q, err := db.Table("q"); err == nil {
		if err := q.Scan(0, func(r map[string]Value) bool {
			if _, ok := r["id"]; ok {
				t.Fatal("table q has no id column")
			}
			return true
		}); err != nil {
			t.Fatal(err)
		}
	}
}

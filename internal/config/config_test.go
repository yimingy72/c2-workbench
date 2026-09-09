package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadExample(t *testing.T) {
	cfg, err := Load("../../conf/setting.conf.example")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebPort != 8082 {
		t.Fatalf("port %d", cfg.WebPort)
	}
	if cfg.Username != "admin" {
		t.Fatalf("username %q", cfg.Username)
	}
	if cfg.Password != "changeme" {
		t.Fatalf("password %q", cfg.Password)
	}
	if cfg.WebAddr() != "0.0.0.0:8082" {
		t.Fatalf("addr %q", cfg.WebAddr())
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("no-such-file.conf"); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadInvalidPort(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.conf")
	if err := os.WriteFile(p, []byte("web_port=99999\nweb_username=a\nweb_password=b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("expected invalid port")
	}
}

func TestLoadEmptyJWTAllowed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ok.conf")
	body := "web_ip=127.0.0.1\nweb_port=8082\nweb_username=u\nweb_password=p\nweb_jwt_secret=\n"
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JWTSecret != "" {
		t.Fatalf("secret %q", cfg.JWTSecret)
	}
}

func TestLoadMissingPassword(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nopw.conf")
	if err := os.WriteFile(p, []byte("web_username=admin\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("expected missing password")
	}
}

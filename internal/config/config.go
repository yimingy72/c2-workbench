package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	WebTitle  string
	WebIP     string
	WebPort   int
	Username  string
	Password  string
	JWTSecret string
	LogLevel  int
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := &Config{
		WebTitle: "管理平台",
		WebIP:    "0.0.0.0",
		WebPort:  8082,
		Username: "admin",
		LogLevel: 6,
	}

	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected key=value", path, lineNo)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "web_title":
			cfg.WebTitle = v
		case "web_ip":
			cfg.WebIP = v
		case "web_port":
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 || n > 65535 {
				return nil, fmt.Errorf("%s:%d: invalid web_port", path, lineNo)
			}
			cfg.WebPort = n
		case "web_username":
			cfg.Username = v
		case "web_password":
			cfg.Password = v
		case "web_jwt_secret":
			cfg.JWTSecret = v
		case "log_level":
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: invalid log_level", path, lineNo)
			}
			cfg.LogLevel = n
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if cfg.Username == "" {
		return nil, fmt.Errorf("web_username is required")
	}
	if cfg.Password == "" {
		return nil, fmt.Errorf("web_password is required")
	}
	if cfg.WebIP == "" {
		return nil, fmt.Errorf("web_ip is required")
	}
	return cfg, nil
}

func (c *Config) WebAddr() string {
	return fmt.Sprintf("%s:%d", c.WebIP, c.WebPort)
}

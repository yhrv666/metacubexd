package main

import (
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestPanelSessionAndAllowlist(t *testing.T) {
	const password = "synthetic-admin-test-only"
	const secret = "synthetic-kernel-test-only"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret || r.URL.Query().Has("token") {
			t.Error("controller authentication or query forwarding")
		}
		switch r.URL.Path {
		case "/version":
			io.WriteString(w, `{"version":"v1.19.34","diagnostic":"synthetic-kernel-test-only"}`)
		case "/logs":
			upgrader := websocket.Upgrader{}
			ws, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer ws.Close()
			_ = ws.WriteJSON(map[string]string{"type": "info", "payload": "password=example connected"})
		default:
			t.Errorf("unexpected controller request %s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	origin, _ := url.Parse(upstream.URL)
	app := httptest.NewServer(newPanel(sha256.Sum256([]byte(password)), newKernel(origin, secret), http.NotFoundHandler()))
	defer app.Close()
	attempt := func(method, path, origin, body string, cookie *http.Cookie) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, app.URL+path, strings.NewReader(body))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		res, err := app.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	expect := func(res *http.Response, status int) string {
		t.Helper()
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != status {
			t.Fatalf("HTTP %d, want %d: %s", res.StatusCode, status, body)
		}
		if strings.Contains(string(body), secret) {
			t.Fatal("controller credential exposed")
		}
		return string(body)
	}
	expect(attempt("GET", "/api/monitor/version", "", "", nil), 401)
	expect(attempt("POST", "/api/session", "http://example.test", `{"password":"synthetic-admin-test-only"}`, nil), 403)
	login := attempt("POST", "/api/session", app.URL, `{"password":"synthetic-admin-test-only"}`, nil)
	expect(login, 204)
	cookie := login.Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("session cookie protection missing")
	}
	expect(attempt("GET", "/api/monitor/version?token=browser", "", "", cookie), 200)
	expect(attempt("GET", "/api/monitor/configs", "", "", cookie), 404)
	expect(attempt("PUT", "/api/monitor/configs", app.URL, "{}", cookie), 404)
	socket := strings.Replace(app.URL, "http:", "ws:", 1) + "/api/monitor/logs?token=browser"
	header := http.Header{"Origin": {app.URL}, "Cookie": {cookie.String()}}
	ws, _, err := websocket.DefaultDialer.Dial(socket, header)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_, message, err := ws.ReadMessage()
	if err != nil || strings.Contains(string(message), "example") || !strings.Contains(string(message), "connected") {
		t.Fatalf("log forwarding: %s, %v", message, err)
	}
	expect(attempt("DELETE", "/api/session", app.URL, "", cookie), 204)
	expect(attempt("GET", "/api/monitor/version", "", "", cookie), 401)
}

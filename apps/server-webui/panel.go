package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const sessionCookie = "mcxd_session"

type session struct {
	ctx    context.Context
	cancel context.CancelFunc
}

type panel struct {
	password          [32]byte
	kernel            *kernel
	static            http.Handler
	releaseDir        string
	baseConfig        string
	manager           *manager
	drafts            map[string]managedDraft
	fetchSubscription func(context.Context, string) ([]byte, error)
	mu                sync.Mutex
	sessions          map[string]session
	failures          int
	retryAt           time.Time
	sockets           chan struct{}
}

func newPanel(password [32]byte, kernel *kernel, static http.Handler) *panel {
	return &panel{password: password, kernel: kernel, static: static, sessions: make(map[string]session), drafts: make(map[string]managedDraft), sockets: make(chan struct{}, 32), fetchSubscription: downloadSubscription}
}

func respond(w http.ResponseWriter, status int, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}

func problem(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}

func readJSON(w http.ResponseWriter, r *http.Request, max int64, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, max))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		problem(w, http.StatusBadRequest, "Invalid request")
		return false
	}
	return true
}

func expectedOrigin(r *http.Request) string {
	if r.TLS != nil {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}

func (p *panel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	if host != "localhost" && !loopback(strings.Trim(host, "[]")) {
		problem(w, http.StatusForbidden, "Use a loopback SSH tunnel")
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self' ws://"+r.Host+"; frame-ancestors 'none'; form-action 'self'; base-uri 'none'; object-src 'none'")
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		p.static.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	origin := r.Header.Get("Origin")
	if (origin != "" && origin != expectedOrigin(r)) || (r.Method != http.MethodGet && origin != expectedOrigin(r)) {
		problem(w, http.StatusForbidden, "Same-origin request required")
		return
	}
	if r.URL.Path == "/api/session" && r.Method == http.MethodPost {
		p.login(w, r)
		return
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		problem(w, http.StatusUnauthorized, "Sign in required")
		return
	}
	p.mu.Lock()
	session, ok := p.sessions[cookie.Value]
	p.mu.Unlock()
	if !ok || session.ctx.Err() != nil {
		problem(w, http.StatusUnauthorized, "Sign in required")
		return
	}
	if r.URL.Path == "/api/session" {
		switch r.Method {
		case http.MethodGet:
			respond(w, http.StatusOK, struct {
				Authenticated bool   `json:"authenticated"`
				ReleaseTag    string `json:"releaseTag,omitempty"`
			}{true, deploymentTag(p.releaseDir)})
		case http.MethodDelete:
			p.mu.Lock()
			delete(p.sessions, cookie.Value)
			delete(p.drafts, cookie.Value)
			session.cancel()
			p.mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
			w.WriteHeader(http.StatusNoContent)
		default:
			problem(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}
	if route, ok := strings.CutPrefix(r.URL.Path, "/api/monitor/"); ok && r.Method == http.MethodGet {
		p.monitor(w, r, session.ctx, route)
		return
	}
	if route, ok := strings.CutPrefix(r.URL.Path, "/api/managed/"); ok {
		p.manage(w, r, cookie.Value, route)
		return
	}
	problem(w, http.StatusNotFound, "Capability unavailable")
}

func (p *panel) login(w http.ResponseWriter, r *http.Request) {
	var request struct{ Password string }
	if !readJSON(w, r, 8192, &request) {
		return
	}
	password := sha256.Sum256([]byte(request.Password))
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if now.Before(p.retryAt) {
		problem(w, http.StatusTooManyRequests, "Try again shortly")
		return
	}
	if subtle.ConstantTimeCompare(password[:], p.password[:]) != 1 {
		p.failures++
		if p.failures >= 5 {
			p.retryAt = now.Add(30 * time.Second)
			p.failures = 0
		}
		problem(w, http.StatusUnauthorized, "Incorrect password")
		return
	}
	p.failures = 0
	for key, session := range p.sessions {
		if session.ctx.Err() != nil {
			delete(p.sessions, key)
			delete(p.drafts, key)
		}
	}
	if old, err := r.Cookie(sessionCookie); err == nil {
		if session, ok := p.sessions[old.Value]; ok {
			session.cancel()
			delete(p.sessions, old.Value)
			delete(p.drafts, old.Value)
		}
	}
	if len(p.sessions) >= 16 {
		problem(w, http.StatusTooManyRequests, "Too many active sessions")
		return
	}
	const lifetime = 12 * time.Hour
	var data [32]byte
	_, _ = rand.Read(data[:])
	key := base64.RawURLEncoding.EncodeToString(data[:])
	ctx, cancel := context.WithTimeout(context.Background(), lifetime)
	p.sessions[key] = session{ctx, cancel}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: key, Path: "/", MaxAge: int(lifetime.Seconds()), HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

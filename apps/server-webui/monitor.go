package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/gorilla/websocket"
)

type kernel struct {
	origin     *url.URL
	secret     string
	client     *http.Client
	redact     func([]byte) []byte
	secretJSON []byte
}

func newKernel(origin *url.URL, secret string) *kernel {
	escaped, _ := json.Marshal(secret)
	return &kernel{origin: origin, secret: secret, secretJSON: escaped[1 : len(escaped)-1], client: &http.Client{
		Timeout:       5 * time.Second,
		Transport:     &http.Transport{DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext, MaxIdleConnsPerHost: 4},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type kernelError string

func (e kernelError) Error() string { return string(e) }

func (k *kernel) call(ctx context.Context, method, path string, body io.Reader) ([]byte, error) {
	u := *k.origin
	u.Path = "/" + path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, kernelError("Controller request failed")
	}
	req.Header.Set("Authorization", "Bearer "+k.secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := k.client.Do(req)
	if err != nil {
		return nil, kernelError("Mihomo controller is unreachable or timed out")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, kernelError("Mihomo rejected the controller credential")
	}
	if res.StatusCode/100 != 2 {
		return nil, kernelError("Mihomo controller rejected the request")
	}
	const max = 8 << 20
	data, err := io.ReadAll(io.LimitReader(res.Body, max+1))
	if err != nil || len(data) > max {
		return nil, kernelError("Mihomo response is unavailable or too large")
	}
	return data, nil
}

var credentialText = regexp.MustCompile("(?i)([a-z][a-z0-9+.-]*://[^\\s\"<>]+|(?:bearer|authorization|password|passwd|secret|token)\\s*[:= ]\\s*[^\\s\",;]+)")

func (k *kernel) safeJSON(data []byte) []byte {
	if len(k.secretJSON) != 0 && bytes.Contains(data, k.secretJSON) {
		data = bytes.ReplaceAll(data, k.secretJSON, []byte("[redacted]"))
	}
	if k.redact != nil {
		data = k.redact(data)
	}
	if !json.Valid(data) {
		return nil
	}
	return data
}

func (p *panel) monitor(w http.ResponseWriter, r *http.Request, session context.Context, route string) {
	if websocket.IsWebSocketUpgrade(r) {
		p.stream(w, r, session, route)
		return
	}
	switch route {
	case "version", "rules", "providers/rules":
	default:
		problem(w, http.StatusNotFound, "Capability unavailable")
		return
	}
	data, err := p.kernel.call(r.Context(), http.MethodGet, route, nil)
	if err != nil {
		problem(w, http.StatusBadGateway, err.Error())
		return
	}
	data = p.kernel.safeJSON(data)
	if data == nil {
		problem(w, http.StatusBadGateway, "Invalid Mihomo response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (p *panel) stream(w http.ResponseWriter, r *http.Request, session context.Context, route string) {
	u := *p.kernel.origin
	u.Path = "/" + route
	switch route {
	case "traffic", "memory":
	case "connections":
		u.RawQuery = "interval=1000"
	case "logs":
		level := r.URL.Query().Get("level")
		switch level {
		case "debug", "info", "warning", "error", "silent":
		default:
			level = "info"
		}
		u.RawQuery = url.Values{"level": {level}}.Encode()
	default:
		problem(w, http.StatusNotFound, "Capability unavailable")
		return
	}
	if r.Header.Get("Origin") != expectedOrigin(r) {
		problem(w, http.StatusForbidden, "Same-origin socket required")
		return
	}
	select {
	case p.sockets <- struct{}{}:
		defer func() { <-p.sockets }()
	default:
		problem(w, http.StatusTooManyRequests, "Too many monitor streams")
		return
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	dialer := websocket.Dialer{HandshakeTimeout: 4 * time.Second}
	up, res, err := dialer.DialContext(r.Context(), u.String(), http.Header{"Authorization": {"Bearer " + p.kernel.secret}})
	if err != nil {
		if res != nil {
			res.Body.Close()
		}
		problem(w, http.StatusBadGateway, "Mihomo stream unavailable")
		return
	}
	defer up.Close()
	up.SetReadLimit(8 << 20)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	down, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer down.Close()
	down.SetReadLimit(1024)
	stop := context.AfterFunc(session, func() { up.Close(); down.Close() })
	defer stop()
	go func() {
		defer up.Close()
		_, _, _ = down.ReadMessage()
	}()
	var frame bytes.Buffer
	for {
		if route != "logs" {
			_ = up.SetReadDeadline(time.Now().Add(10 * time.Second))
		}
		kind, reader, err := up.NextReader()
		if err != nil || kind != websocket.TextMessage {
			return
		}
		frame.Reset()
		if _, err := frame.ReadFrom(reader); err != nil {
			return
		}
		data := frame.Bytes()
		if route == "logs" {
			data = credentialText.ReplaceAll(data, []byte("[redacted]"))
		}
		data = p.kernel.safeJSON(data)
		if data == nil {
			continue
		}
		_ = down.SetWriteDeadline(time.Now().Add(3 * time.Second))
		if down.WriteMessage(websocket.TextMessage, data) != nil {
			return
		}
	}
}

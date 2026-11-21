package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBrowserFixture(t *testing.T) {
	if listen := os.Getenv("MCXD_BROWSER_PANEL"); listen != "" {
		serveBrowserPanel(t, listen)
		return
	}
	addr := os.Getenv("MCXD_BROWSER_FIXTURE")
	if addr == "" {
		t.Skip("browser fixture runs as an external process")
	}
	count, _ := strconv.Atoi(os.Getenv("MCXD_FIXTURE_CONNECTIONS"))
	if count == 0 {
		count = 3
	}
	type entry struct {
		ID          string            `json:"id"`
		Download    int               `json:"download"`
		Upload      int               `json:"upload"`
		Start       time.Time         `json:"start"`
		Chains      []string          `json:"chains"`
		Rule        string            `json:"rule"`
		RulePayload string            `json:"rulePayload"`
		Metadata    map[string]string `json:"metadata"`
	}
	connections := make([]entry, count)
	for i := range connections {
		connections[i] = entry{ID: strconv.Itoa(i), Download: 12345, Upload: 678, Start: time.Now(), Chains: []string{"Fixture"}, Rule: "DomainSuffix", RulePayload: "example.test", Metadata: map[string]string{"network": "tcp", "type": "HTTP", "sourceIP": "192.0.2.1", "destinationIP": "203.0.113.20", "sourcePort": "20480", "destinationPort": "443", "host": fmt.Sprintf("load-%04d.example.test", i), "dnsMode": "normal", "process": "", "processPath": "", "inboundName": "fixture", "remoteDestination": "203.0.113.20"}}
	}
	data, _ := json.Marshal(connections)
	var interrupted atomic.Int64
	var messages atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("POST /interrupt", func(w http.ResponseWriter, _ *http.Request) {
		interrupted.Store(time.Now().Add(5 * time.Second).UnixNano())
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int64{"frames": messages.Load()})
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"version":"v1.19.34","meta":true}`)
	})
	mux.HandleFunc("GET /rules", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"rules":[{"type":"Match","payload":"","proxy":"Fixture","size":-1}]}`)
	})
	mux.HandleFunc("GET /providers/rules", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"providers":{}}`) })
	for _, route := range []string{"connections", "traffic", "memory", "logs"} {
		mux.HandleFunc("GET /"+route, func(w http.ResponseWriter, r *http.Request) {
			if interrupted.Load() > time.Now().UnixNano() {
				w.WriteHeader(503)
				return
			}
			upgrader := websocket.Upgrader{}
			ws, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer ws.Close()
			done := make(chan struct{})
			go func() { defer close(done); _, _, _ = ws.ReadMessage() }()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				if interrupted.Load() > time.Now().UnixNano() {
					return
				}
				var frame []byte
				switch route {
				case "connections":
					n := messages.Add(1)
					frame = fmt.Appendf(make([]byte, 0, len(data)+100), `{"uploadTotal":%d,"downloadTotal":%d,"connections":`, n, n*10)
					frame = append(append(frame, data...), '}')
				case "traffic":
					frame = []byte(`{"up":1024,"down":2048}`)
				case "memory":
					frame = []byte(`{"inuse":4096,"oslimit":0}`)
				case "logs":
					frame = []byte(`{"type":"info","payload":"[TCP] 192.0.2.1:20480 --> example.test:443 match DomainSuffix(example.test) using Fixture"}`)
				}
				_ = ws.SetWriteDeadline(time.Now().Add(3 * time.Second))
				if ws.WriteMessage(websocket.TextMessage, frame) != nil {
					return
				}
				select {
				case <-done:
					return
				case <-ticker.C:
				}
			}
		})
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	server := http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
	_ = server.Shutdown(context.Background())
}

func serveBrowserPanel(t *testing.T, listen string) {
	m, _, _ := setupManager(t)
	web, err := fs.Sub(assets, "web")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := url.Parse("http://" + os.Getenv("MCXD_BROWSER_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	p := newPanel(sha256.Sum256([]byte("synthetic-admin-fixture")), newKernel(controller, "synthetic-controller-fixture"), http.FileServerFS(web))
	p.releaseDir = filepath.Dir(os.Args[0])
	p.manager, p.baseConfig = m, m.config
	p.fetchSubscription = func(context.Context, string) ([]byte, error) {
		return []byte("proxies:\n  - {name: relay, type: socks5, server: relay.example, port: 1080}\n"), nil
	}
	server := http.Server{Addr: listen, Handler: p, ReadHeaderTimeout: 3 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		t.Fatal(err)
	}
}

package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestProbeAddressRequiresAttachedPrivateNetwork(t *testing.T) {
	var addresses []net.Addr
	for _, value := range []string{"172.30.240.1/24", "fdfe:3281::1/64", "192.0.2.1/24"} {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			t.Fatal(err)
		}
		addresses = append(addresses, network)
	}
	for host, want := range map[string]bool{
		"172.30.240.12": true,
		"172.30.241.12": false,
		"fdfe:3281::2":  true,
		"192.0.2.12":    false,
		"proxy.local":   false,
	} {
		if got := attachedPrivateAddress(host, addresses); got != want {
			t.Errorf("attachedPrivateAddress(%q) = %t, want %t", host, got, want)
		}
	}
}

func TestRuntimeProbeIdentifiesItsSourceAndTarget(t *testing.T) {
	const host = "outside.example"
	peer := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer <- r.RemoteAddr
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var remote string
		select {
		case remote = <-peer:
		case <-r.Context().Done():
			return
		}
		source, port, _ := net.SplitHostPort(remote)
		entry := func(ip, target, policy string) map[string]any {
			return map[string]any{
				"metadata": map[string]string{"sourceIP": ip, "sourcePort": port, "host": target},
				"chains":   []string{policy},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"connections": []any{
			entry("192.0.2.12", host, "REJECT"),
			entry(source, "other.example", "REJECT"),
			entry(source, host, "DIRECT"),
		}})
	}))
	defer controller.Close()
	origin, err := url.Parse(controller.URL)
	if err != nil {
		t.Fatal(err)
	}
	instance := &fixedInstance{
		kernel: newKernel(origin, ""),
		probes: probeSpec{Proxy: strings.TrimPrefix(proxy.URL, "http://")},
	}
	if err := instance.testRoute(t.Context(), routeProbe{host + ":443", "DIRECT"}, nil, nil); err != nil {
		t.Fatal(err)
	}
}

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsolatedMihomoInstance(t *testing.T) {
	kind := os.Getenv("MCXD_INSTANCE_TEST")
	if kind == "" {
		t.Skip("set MCXD_INSTANCE_TEST to systemd or docker")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	const secret = "synthetic-controller-fixture"
	home := t.TempDir()
	config := filepath.Join(home, "config.yaml")
	controller := freeTCP(t)
	mixed := freeTCP(t)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer sink.Close()
	target := strings.TrimPrefix(sink.URL, "http://")
	_, sinkPort, _ := net.SplitHostPort(target)
	residential := startSyntheticSOCKS(t, func(host string) string {
		if host == "api.openai.com" || host == "cloudcode-pa.googleapis.com" || host == "127.0.0.1" {
			return target
		}
		return ""
	})
	defer residential.Close()
	relay := startSyntheticSOCKS(t, func(host string) string {
		if host == "192.0.2.22" {
			return residential.Addr().String()
		}
		return ""
	})
	defer relay.Close()
	_, relayPort, _ := net.SplitHostPort(relay.Addr().String())
	_, port, _ := net.SplitHostPort(mixed)
	base := fmt.Sprintf("mixed-port: %s\nexternal-controller: %s\nsecret: %s\nmode: rule\nlog-level: silent\nallow-lan: false\nprofile: {store-selected: false}\nhosts: {outside.example: 127.0.0.1, api.openai.com: 127.0.0.1, cloudcode-pa.googleapis.com: 127.0.0.1}\nproxies:\n  - {name: relay, type: socks5, server: 127.0.0.1, port: %s}\nrules:\n  - IP-CIDR6,2001:db8::/32,REJECT,no-resolve\n  - MATCH,DIRECT\n", port, controller, secret, relayPort)
	if err := os.WriteFile(config, []byte(base), 0600); err != nil {
		t.Fatal(err)
	}
	image, binaryPath, dockerPath := os.Getenv("MCXD_FIXTURE_IMAGE"), os.Getenv("MCXD_FIXTURE_MIHOMO"), ""
	name := "mcxd-fixture-" + revisionID()[:10]
	run := func(command string, args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(ctx, command, args...).CombinedOutput(); err != nil {
			t.Fatalf("fixture startup failed: %v\n%s", err, out)
		}
	}
	switch kind {
	case "systemd":
		run("sudo", "systemd-run", "--quiet", "--unit", name, "--property=Type=exec", "--property=StandardOutput=null", "--property=StandardError=null", binaryPath, "-d", home, "-f", config)
		t.Cleanup(func() { _ = exec.Command("sudo", "systemctl", "stop", name).Run() })
	case "docker":
		var err error
		dockerPath, err = exec.LookPath("docker")
		if err != nil {
			t.Fatal(err)
		}
		binaryPath = ""
		run(dockerPath, "run", "--detach", "--rm", "--name", name, "--network=host", "--volume", home+":/config", image, "-d", "/config", "-f", "/config/config.yaml")
		t.Cleanup(func() { _ = exec.Command(dockerPath, "rm", "-f", name).Run() })
	default:
		t.Fatal("unknown fixture adapter")
	}
	origin, _ := url.Parse("http://" + controller)
	kernel := newKernel(origin, secret)
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := kernel.call(ctx, "GET", "version", nil); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture Mihomo did not start (%s)", kind)
		}
		time.Sleep(100 * time.Millisecond)
	}
	check, err := fixedValidator(home, binaryPath, dockerPath, image)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "validate.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() { _ = startValidator(listener, check) }()
	runtime := &fixedInstance{kernel: kernel, validate: validatorClient(listener.Addr().String()), probes: probeSpec{Proxy: mixed, Probes: []routeProbe{
		{"api.openai.com:" + sinkPort, "DIRECT"}, {"cloudcode-pa.googleapis.com:" + sinkPort, "DIRECT"}, {"outside.example:" + sinkPort, "DIRECT"},
	}}}
	m := &manager{dir: filepath.Join(t.TempDir(), "private"), config: config, instance: runtime}
	if err := m.open(); err != nil {
		t.Fatal(err)
	}
	_, initial, err := m.current()
	if err != nil {
		t.Fatal(err)
	}
	managed := managedConfig{
		Chains: []managedChain{{ID: "home", Transit: "relay", Import: "socks5://192.0.2.22:1080"}},
		Rules:  []managedRule{{Chain: "home", Presets: []string{"openai", "antigravity"}, Sources: []string{"127.0.0.0/8"}}},
	}
	if err := m.apply(ctx, initial.ID, "publish", func(_ stateIndex, r revision) (revision, error) { return composeRevision(r, managed) }); err != nil {
		t.Fatal(err)
	}
	_, applied, err := m.current()
	if err != nil || applied.ID == initial.ID {
		t.Fatal("candidate not committed")
	}
	// Stop only the synthetic residence. A CONNECT tunnel must not deliver an
	// HTTP request to the sink through a DIRECT fallback.
	residential.Close()
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: mixed})}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
	if res, err := client.Get("http://api.openai.com:" + sinkPort + "/"); err == nil {
		res.Body.Close()
		if res.StatusCode == 204 {
			t.Fatal("residential failure fell back to DIRECT")
		}
	}
	// The previous policy uses the local synthetic sink for all three probes.
	if err := m.apply(ctx, applied.ID, "rollback", func(_ stateIndex, _ revision) (revision, error) { return initial, nil }); err != nil {
		t.Fatal(err)
	}
	if err := m.open(); err != nil || !m.diskMatches(initial.Config) {
		t.Fatal("rollback did not survive restart")
	}
	if err := runtime.Validate(ctx, []byte("broken: [")); err == nil {
		t.Fatal("Mihomo accepted invalid candidate")
	}
}

func freeTCP(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func startSyntheticSOCKS(t *testing.T, target func(string) string) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveSyntheticSOCKS(conn, target)
		}
	}()
	return listener
}

func serveSyntheticSOCKS(conn net.Conn, target func(string) string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:2]); err != nil || header[0] != 5 {
		return
	}
	if _, err := io.CopyN(io.Discard, conn, int64(header[1])); err != nil {
		return
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return
	}
	if _, err := io.ReadFull(conn, header[:]); err != nil || header[1] != 1 {
		return
	}
	var address []byte
	switch header[3] {
	case 1:
		address = make([]byte, 4)
	case 3:
		if _, err := io.ReadFull(conn, header[:1]); err != nil {
			return
		}
		address = make([]byte, int(header[0]))
	case 4:
		address = make([]byte, 16)
	default:
		return
	}
	if _, err := io.ReadFull(conn, address); err != nil {
		return
	}
	if _, err := io.ReadFull(conn, header[:2]); err != nil {
		return
	}
	host := string(address)
	if len(address) == net.IPv4len || len(address) == net.IPv6len {
		host = net.IP(address).String()
	}
	dst := target(host)
	if dst == "" {
		return
	}
	up, err := net.DialTimeout("tcp", dst, time.Second)
	if err != nil {
		return
	}
	defer up.Close()
	if _, err := conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	go func() { _, _ = io.Copy(up, conn); up.Close() }()
	_, _ = io.Copy(conn, up)
}

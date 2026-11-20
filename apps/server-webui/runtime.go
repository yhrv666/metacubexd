package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type fixedInstance struct {
	kernel   *kernel
	validate validator
	probes   probeSpec
}

type probeSpec struct {
	Proxy  string
	Probes []routeProbe
}

type routeProbe struct {
	Target         string
	BaselinePolicy string
}

func (p probeSpec) valid() bool {
	host, port, err := net.SplitHostPort(p.Proxy)
	n, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || portErr != nil || n == 0 || !loopback(host) || len(p.Probes) == 0 || len(p.Probes) > 12 {
		return false
	}
	for _, probe := range p.Probes {
		name, port, err := net.SplitHostPort(probe.Target)
		n, portErr := strconv.ParseUint(port, 10, 16)
		if err != nil || portErr != nil || n == 0 || !validDomain(name) || probe.BaselinePolicy == "" || len(probe.BaselinePolicy) > 256 {
			return false
		}
	}
	return true
}

func (f *fixedInstance) Validate(ctx context.Context, data []byte) error {
	return f.validate(ctx, data)
}

func (f *fixedInstance) Reload(ctx context.Context) error {
	_, err := f.kernel.call(ctx, http.MethodPut, "configs", strings.NewReader("{}"))
	return err
}

func (f *fixedInstance) get(ctx context.Context, path string, target any) bool {
	data, err := f.kernel.call(ctx, http.MethodGet, path, nil)
	return err == nil && json.Unmarshal(data, target) == nil
}

func (f *fixedInstance) Verify(ctx context.Context, data []byte) error {
	root, err := configTree(data)
	if err != nil {
		return err
	}
	var active map[string]json.RawMessage
	if !f.get(ctx, "configs", &active) {
		return errors.New("Runtime general settings are unavailable")
	}
	if value := yamlField(root, "mode"); value != nil && !strings.EqualFold(strings.Trim(string(active["mode"]), "\""), value.Value) {
		return errors.New("Runtime mode differs from startup configuration")
	}
	for _, key := range []string{"port", "mixed-port", "socks-port", "redir-port", "tproxy-port"} {
		expect := 0
		if value := yamlField(root, key); value != nil && value.Decode(&expect) != nil {
			return errValidation
		}
		var observed int
		if json.Unmarshal(active[key], &observed) != nil || expect != observed {
			return errors.New("Runtime listener ports differ from startup configuration")
		}
	}
	for key, route := range map[string]string{"proxy-providers": "providers/proxies", "rule-providers": "providers/rules"} {
		var providers struct{ Providers map[string]json.RawMessage }
		if !f.get(ctx, route, &providers) {
			return errors.New("Runtime providers are unavailable")
		}
		if node := yamlField(root, key); node != nil {
			var configured map[string]any
			if node.Decode(&configured) != nil {
				return errValidation
			}
			for name := range configured {
				if _, ok := providers.Providers[name]; !ok {
					return errors.New("A configured provider is missing from runtime")
				}
			}
		}
	}
	var rules []string
	if node := yamlField(root, "rules"); node != nil && node.Decode(&rules) != nil {
		return errValidation
	}
	observed, err := f.verifyRules(ctx, rules)
	if err != nil {
		return err
	}
	if err := f.verifyListeners(ctx, root); err != nil {
		return err
	}
	for _, probe := range f.probes.Probes {
		if err := f.testRoute(ctx, probe, rules, observed); err != nil {
			return err
		}
	}
	return nil
}

type liveRule struct {
	Type, Payload, Proxy string
	Extra                struct{ Disabled bool }
}

func (f *fixedInstance) verifyRules(ctx context.Context, rules []string) ([]liveRule, error) {
	var active struct{ Rules []liveRule }
	err := errors.New("Runtime rules differ from the startup rule targets")
	if !f.get(ctx, "rules", &active) || len(active.Rules) != len(rules) {
		return nil, err
	}
	for i, raw := range rules {
		rule := active.Rules[i]
		compact := strings.ReplaceAll(raw, " ", "")
		target := strings.ReplaceAll(rule.Proxy, " ", "")
		if rule.Extra.Disabled || (!strings.HasSuffix(compact, ","+target) && !strings.HasSuffix(compact, ","+target+",no-resolve") && !strings.HasSuffix(compact, ","+target+",src")) {
			return nil, err
		}
		matcher, _, _ := strings.Cut(compact, ",")
		if matcher == "IP-CIDR6" {
			matcher = "IP-CIDR"
		}
		normalize := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "-", "")) }
		if normalize(matcher) != normalize(rule.Type) && !strings.HasPrefix(normalize(rule.Type), normalize(matcher)) {
			return nil, err
		}
		if strings.HasPrefix(rule.Proxy, managedPrefix) && matcher != "AND" && !strings.Contains(compact, ","+rule.Payload+",") {
			return nil, err
		}
	}
	return active.Rules, nil
}

func (f *fixedInstance) verifyListeners(ctx context.Context, root *yaml.Node) error {
	host, _, _ := net.SplitHostPort(f.probes.Proxy)
	dialer := net.Dialer{Timeout: 2 * time.Second}
	for _, key := range []string{"port", "mixed-port", "socks-port"} {
		if node := yamlField(root, key); node != nil && node.Value != "0" {
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, node.Value))
			if err != nil {
				return errors.New("A configured TCP listener is unreachable")
			}
			conn.Close()
		}
	}
	return nil
}

func expectedPolicy(probe routeProbe, source netip.Addr, configured []string, observed []liveRule) string {
	host, _, _ := net.SplitHostPort(probe.Target)
	for i, rule := range observed {
		if !strings.HasPrefix(rule.Proxy, managedPrefix) {
			break
		}
		var domain, ip string
		for _, part := range strings.Split(configured[i], ",") {
			part = strings.Trim(part, "()")
			if domain == "next" {
				domain = part
			}
			if ip == "next" {
				ip = part
			}
			if part == "DOMAIN-SUFFIX" {
				domain = "next"
			}
			if part == "SRC-IP-CIDR" {
				ip = "next"
			}
		}
		if domain != "" && host != domain && !strings.HasSuffix(host, "."+domain) {
			continue
		}
		if ip != "" {
			if prefix, err := netip.ParsePrefix(ip); err != nil || !prefix.Contains(source) {
				continue
			}
		}
		return rule.Proxy
	}
	return probe.BaselinePolicy
}

func (f *fixedInstance) testRoute(ctx context.Context, probe routeProbe, configured []string, observed []liveRule) error {
	const mismatch = "A test connection did not reach its expected policy"
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", f.probes.Proxy)
	if err != nil {
		return errors.New("The local HTTP probe listener is unreachable")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", probe.Target, probe.Target); err != nil {
		return errors.New(mismatch)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != 200 {
		return errors.New(mismatch)
	}
	addr := conn.LocalAddr().(*net.TCPAddr).AddrPort()
	expected := expectedPolicy(probe, addr.Addr().Unmap(), configured, observed)
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		var snapshot struct {
			Connections []struct {
				Chains   []string
				Metadata struct {
					SourcePort string
					Host       string
				}
			}
		}
		if f.get(ctx, "connections", &snapshot) {
			for _, active := range snapshot.Connections {
				if active.Metadata.SourcePort == strconv.Itoa(int(addr.Port())) {
					if slices.Contains(active.Chains, expected) {
						return nil
					}
					return errors.New(mismatch)
				}
			}
		}
		select {
		case <-deadline.C:
			return errors.New("Test connection was absent from the runtime snapshot")
		case <-ctx.Done():
			return errors.New(mismatch)
		case <-tick.C:
		}
	}
}

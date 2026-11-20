package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const subscriptionPrefix = "MCXD-SUB-"

type subscription struct {
	ID        string
	URL       string
	Cached    []byte
	Refreshed time.Time
}

type subscriptionView struct {
	ID        string     `json:"id"`
	Refreshed *time.Time `json:"refreshed,omitempty"`
}

func summarizeSubscriptions(list []subscription) []subscriptionView {
	result := make([]subscriptionView, 0, len(list))
	for _, item := range list {
		view := subscriptionView{ID: item.ID}
		if !item.Refreshed.IsZero() {
			view.Refreshed = &item.Refreshed
		}
		result = append(result, view)
	}
	return result
}

func subscriptionURL(raw string) (*url.URL, error) {
	invalid := errors.New("Supply a valid HTTPS subscription URL")
	if len(raw) > 8192 {
		return nil, invalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Fragment != "" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return nil, invalid
	}
	return u, nil
}

var carrierNAT = netip.MustParsePrefix("100.64.0.0/10")

func downloadSubscription(ctx context.Context, raw string) ([]byte, error) {
	u, err := subscriptionURL(raw)
	if err != nil {
		return nil, err
	}
	failure := errors.New("Could not download HTTPS subscription from a public address")
	transport := &http.Transport{TLSHandshakeTimeout: 8 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, failure
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, failure
		}
		for _, ip := range addresses {
			ip = ip.Unmap()
			if !ip.IsGlobalUnicast() || ip.IsPrivate() || carrierNAT.Contains(ip) {
				return nil, failure
			}
		}
		for _, ip := range addresses {
			conn, err := (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, failure
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, failure
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, failure
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, failure
	}
	const limit = 2 << 20
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || len(data) > limit {
		return nil, errors.New("Subscription content exceeds limit")
	}
	return data, nil
}

func subscriptionNodes(data []byte) ([]*yaml.Node, error) {
	root, err := configTree(data)
	if err != nil {
		return nil, errors.New("Expected a Clash YAML subscription containing inline proxies")
	}
	nodes := yamlField(root, "proxies")
	if nodes == nil || nodes.Kind != yaml.SequenceNode || len(nodes.Content) == 0 || len(nodes.Content) > 1000 {
		return nil, errors.New("Subscription must contain 1 to 1000 inline proxies")
	}
	list := nodes.Content
	for _, node := range list {
		if node.Kind != yaml.MappingNode || !plainYAML(node) {
			return nil, errors.New("Subscription contains unsupported proxy data")
		}
		var proxy baseProxy
		if node.Decode(&proxy) != nil || proxy.Name == "" || len(proxy.Name) > 128 || strings.ContainsAny(proxy.Name, ",\r\n\x00") || proxy.Server == "" || proxy.Port == 0 || proxy.Type == "" || proxy.Dialer != "" || slices.Contains([]string{"direct", "reject", "pass", "compatible", "dns"}, strings.ToLower(proxy.Type)) {
			return nil, errors.New("Subscription contains an unsupported transit node")
		}
	}
	return list, nil
}

func plainYAML(node *yaml.Node) bool {
	if node.Kind == yaml.AliasNode || node.Anchor != "" || node.Tag == "!!merge" {
		return false
	}
	for _, child := range node.Content {
		if !plainYAML(child) {
			return false
		}
	}
	return true
}

func subscriptionLayer(base []byte, list []subscription) ([]byte, error) {
	if len(list) == 0 {
		return base, nil
	}
	root, err := configTree(base)
	if err != nil {
		return nil, err
	}
	baseNames, err := baseProxies(root)
	if err != nil {
		return nil, err
	}
	var added []*yaml.Node
	for _, item := range list {
		if len(item.Cached) == 0 {
			continue
		}
		proxies, err := subscriptionNodes(item.Cached)
		if err != nil {
			return nil, err
		}
		for _, proxy := range proxies {
			name := yamlField(proxy, "name")
			if name == nil {
				return nil, errors.New("Subscription proxy is missing its name")
			}
			name.Value = subscriptionPrefix + item.ID + "-" + name.Value
			if baseNames[name.Value].Name != "" {
				return nil, errors.New("Subscription proxy name conflicts with another node")
			}
			baseNames[name.Value] = baseProxy{Name: name.Value}
			added = append(added, proxy)
		}
	}
	if len(added) == 0 {
		return base, nil
	}
	field := yamlField(root, "proxies")
	if field == nil || field.Tag == "!!null" {
		setYAML(root, "proxies", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
		field = yamlField(root, "proxies")
	}
	if field.Kind != yaml.SequenceNode {
		return nil, errors.New("Unsupported base proxy list")
	}
	field.Content = append(field.Content, added...)
	merged, err := yaml.Marshal(root)
	if err != nil {
		return nil, errors.New("Could not compose subscription layer")
	}
	return merged, nil
}

func composeRevision(prior revision, managed managedConfig) (revision, error) {
	base, err := subscriptionLayer(prior.Base, prior.Subscriptions)
	if err != nil {
		return revision{}, err
	}
	candidate, normalized, err := makeManaged(base, managed, prior.Managed)
	if err != nil {
		return revision{}, err
	}
	return revision{Base: prior.Base, Config: candidate, Managed: normalized, Subscriptions: prior.Subscriptions}, nil
}

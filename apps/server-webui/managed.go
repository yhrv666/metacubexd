package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const managedPrefix = "MCXD-RESIDENTIAL-"

var identifier = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_-]{0,47}$")
var domainPattern = regexp.MustCompile("^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$")

type residential struct {
	Type     string `json:"type"`
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type managedChain struct {
	ID      string       `json:"id"`
	Transit string       `json:"transit"`
	Proxy   *residential `json:"proxy,omitempty"`
	Import  string       `json:"import,omitempty"`
}

type managedRule struct {
	Chain   string   `json:"chain"`
	Domains []string `json:"domains,omitempty"`
	Sources []string `json:"sources,omitempty"`
	Presets []string `json:"presets,omitempty"`
}

type managedConfig struct {
	Chains []managedChain `json:"chains"`
	Rules  []managedRule  `json:"rules"`
}

type chainPreview struct {
	ID            string `json:"id"`
	Transit       string `json:"transit"`
	Type          string `json:"type"`
	Authenticated bool   `json:"authenticated"`
}

func (m managedConfig) preview() []chainPreview {
	result := make([]chainPreview, 0, len(m.Chains))
	for _, chain := range m.Chains {
		result = append(result, chainPreview{chain.ID, chain.Transit, chain.Proxy.Type, chain.Proxy.Username != ""})
	}
	return result
}

var presets = map[string][]string{
	"openai":      {"openai.com", "chatgpt.com", "oaistatic.com", "oaiusercontent.com"},
	"antigravity": {"antigravity.google", "daily-cloudcode-pa.googleapis.com", "cloudcode-pa.googleapis.com", "autopush-cloudcode-pa.googleapis.com"},
}

// Accept URI form and the legacy host:port:user:password form. Split only
// the first three colons so literal colons in a legacy password survive.
func parseResidential(raw string) (*residential, error) {
	invalid := errors.New("Invalid residential proxy string")
	if !strings.Contains(raw, "://") {
		parts := strings.SplitN(strings.TrimSpace(raw), ":", 4)
		if len(parts) != 4 {
			return nil, invalid
		}
		port, err := strconv.ParseUint(parts[1], 10, 16)
		if err != nil {
			return nil, invalid
		}
		return &residential{"socks5", parts[0], uint16(port), parts[2], parts[3]}, nil
	}
	uri, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || uri.Path != "" || uri.RawQuery != "" || uri.Fragment != "" {
		return nil, invalid
	}
	port, err := strconv.ParseUint(uri.Port(), 10, 16)
	if err != nil {
		return nil, invalid
	}
	password, _ := uri.User.Password()
	return &residential{uri.Scheme, uri.Hostname(), uint16(port), uri.User.Username(), password}, nil
}

func validDomain(value string) bool {
	if len(value) > 253 || !strings.Contains(value, ".") {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if !domainPattern.MatchString(part) {
			return false
		}
	}
	return net.ParseIP(value) == nil
}

func validateResidential(p *residential) bool {
	if p == nil || !slices.Contains([]string{"socks5", "http", "https"}, p.Type) || p.Port == 0 {
		return false
	}
	if (p.Username == "") != (p.Password == "") || len(p.Username) > 1024 || len(p.Password) > 1024 || strings.ContainsAny(p.Username+p.Password, "\r\n\x00") {
		return false
	}
	if ip, err := netip.ParseAddr(p.Host); err == nil {
		ip = ip.Unmap()
		return ip.IsGlobalUnicast() && !ip.IsPrivate() && ip.Zone() == ""
	}
	host := strings.ToLower(p.Host)
	return validDomain(host) && !strings.HasSuffix(host, ".localhost") && !strings.HasSuffix(host, ".local")
}

func configTree(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&doc) != nil || decoder.Decode(new(yaml.Node)) != io.EOF || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("Expected one Mihomo YAML mapping")
	}
	root := doc.Content[0]
	seen := make(map[string]bool)
	for i := 0; i < len(root.Content); i += 2 {
		key := root.Content[i]
		if key.Tag != "!!str" || seen[key.Value] {
			return nil, errors.New("Unsupported config mapping key")
		}
		seen[key.Value] = true
	}
	return root, nil
}

func yamlField(root *yaml.Node, name string) *yaml.Node {
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == name {
			return root.Content[i+1]
		}
	}
	return nil
}

func setYAML(root *yaml.Node, name string, value *yaml.Node) {
	if current := yamlField(root, name); current != nil {
		*current = *value
		return
	}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, value)
}

type baseProxy struct {
	Name   string
	Type   string
	Server string
	Port   uint16
	Dialer string `yaml:"dialer-proxy"`
}

func baseProxies(root *yaml.Node) (map[string]baseProxy, error) {
	names := make(map[string]baseProxy)
	for _, field := range []string{"proxies", "proxy-groups"} {
		node := yamlField(root, field)
		if node == nil {
			continue
		}
		var list []baseProxy
		if node.Decode(&list) != nil {
			return nil, errors.New("Invalid base proxies or groups")
		}
		for _, proxy := range list {
			if proxy.Name == "" || names[proxy.Name].Name != "" || strings.HasPrefix(proxy.Name, managedPrefix) {
				return nil, errors.New("Duplicate or reserved proxy name in base")
			}
			if field == "proxy-groups" {
				proxy.Server = ""
			}
			names[proxy.Name] = proxy
		}
	}
	return names, nil
}

func makeManaged(base []byte, incoming, previous managedConfig) ([]byte, managedConfig, error) {
	incoming.Chains = slices.Clone(incoming.Chains)
	bad := func(err error) ([]byte, managedConfig, error) { return nil, managedConfig{}, err }
	if len(incoming.Chains) > 32 || len(incoming.Rules) > 128 {
		return bad(errors.New("Managed object limit exceeded"))
	}
	root, err := configTree(base)
	if err != nil {
		return bad(err)
	}
	if len(incoming.Rules) > 0 {
		if mode := yamlField(root, "mode"); mode != nil && !strings.EqualFold(mode.Value, "rule") {
			return bad(errors.New("Managed rules require Mihomo rule mode"))
		}
	}
	names, err := baseProxies(root)
	if err != nil {
		return bad(err)
	}
	old := make(map[string]*residential)
	for _, c := range previous.Chains {
		old[c.ID] = c.Proxy
	}
	chains := make(map[string]bool)
	newProxies := make([]*yaml.Node, 0, len(incoming.Chains))
	for i := range incoming.Chains {
		chain := &incoming.Chains[i]
		if !identifier.MatchString(chain.ID) || chains[chain.ID] {
			return bad(fmt.Errorf("Invalid or duplicate chain ID at row %d", i+1))
		}
		transit := names[chain.Transit]
		if transit.Server == "" || transit.Port == 0 || transit.Dialer != "" || slices.Contains([]string{"direct", "reject", "pass", "compatible", "dns"}, strings.ToLower(transit.Type)) {
			return bad(fmt.Errorf("Chain %d needs a fixed transit proxy without a dialer", i+1))
		}
		proxy := chain.Proxy
		if chain.Import != "" {
			if proxy != nil {
				return bad(fmt.Errorf("Choose one proxy input at row %d", i+1))
			}
			proxy, err = parseResidential(chain.Import)
			if err != nil {
				return bad(fmt.Errorf("Invalid residential proxy at row %d", i+1))
			}
		}
		if proxy == nil {
			proxy = old[chain.ID]
		}
		if !validateResidential(proxy) || (strings.EqualFold(proxy.Host, transit.Server) && proxy.Port == transit.Port) {
			return bad(fmt.Errorf("Invalid or recursive residential endpoint at row %d", i+1))
		}
		entry := map[string]any{"name": managedPrefix + chain.ID, "type": proxy.Type, "server": proxy.Host, "port": proxy.Port, "dialer-proxy": chain.Transit}
		if proxy.Type == "https" {
			entry["type"] = "http"
			entry["tls"] = true
		}
		if proxy.Username != "" {
			entry["username"] = proxy.Username
			entry["password"] = proxy.Password
		}
		var node yaml.Node
		if err := node.Encode(entry); err != nil {
			return bad(errors.New("Cannot generate proxy"))
		}
		newProxies = append(newProxies, &node)
		chain.Proxy = proxy
		chain.Import = ""
		chains[chain.ID] = true
	}
	managedRules, err := expandRules(incoming.Rules, chains)
	if err != nil {
		return bad(err)
	}
	for field, nodes := range map[string][]*yaml.Node{"proxies": newProxies, "rules": managedRules} {
		if len(nodes) == 0 {
			continue
		}
		prior := yamlField(root, field)
		if prior == nil || prior.Tag == "!!null" {
			prior = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			setYAML(root, field, prior)
			prior = yamlField(root, field)
		}
		if prior.Kind != yaml.SequenceNode {
			return bad(fmt.Errorf("Invalid %s section in base", field))
		}
		if field == "rules" {
			prior.Content = append(nodes, prior.Content...)
		} else {
			prior.Content = append(prior.Content, nodes...)
		}
	}
	if len(newProxies) == 0 && len(managedRules) == 0 {
		return base, incoming, nil
	}
	result, err := yaml.Marshal(root)
	if err != nil {
		return bad(errors.New("Cannot encode candidate"))
	}
	return result, incoming, nil
}

type ruleScope struct {
	chain, domain string
	source        netip.Prefix
}

func expandRules(rules []managedRule, chains map[string]bool) ([]*yaml.Node, error) {
	var scopes []ruleScope
	var nodes []*yaml.Node
	for i, rule := range rules {
		if !chains[rule.Chain] {
			return nil, fmt.Errorf("Unknown chain in rule %d", i+1)
		}
		if len(rule.Domains) > 64 || len(rule.Sources) > 32 || len(rule.Presets) > len(presets) {
			return nil, fmt.Errorf("Selector limit exceeded in rule %d", i+1)
		}
		domains := slices.Clone(rule.Domains)
		for _, preset := range rule.Presets {
			list, ok := presets[preset]
			if !ok {
				return nil, fmt.Errorf("Unknown domain preset in rule %d", i+1)
			}
			domains = append(domains, list...)
		}
		if len(domains) == 0 && len(rule.Sources) == 0 {
			return nil, fmt.Errorf("Rule %d requires a domain or source selector", i+1)
		}
		sources := []netip.Prefix{{}}
		if len(rule.Sources) > 0 {
			sources = nil
			for _, source := range rule.Sources {
				prefix, err := netip.ParsePrefix(source)
				if err != nil || prefix.Addr().Is4In6() || prefix.Bits() == 0 {
					return nil, fmt.Errorf("Invalid source prefix in rule %d", i+1)
				}
				sources = append(sources, prefix.Masked())
			}
		}
		if len(domains) == 0 {
			domains = []string{""}
		}
		seen := make(map[ruleScope]bool)
		for _, domain := range domains {
			domain = strings.ToLower(strings.TrimSpace(domain))
			if domain == "" && len(rule.Domains) > 0 || domain != "" && !validDomain(domain) {
				return nil, fmt.Errorf("Invalid domain in rule %d", i+1)
			}
			for _, source := range sources {
				scope := ruleScope{rule.Chain, domain, source}
				if seen[scope] {
					continue
				}
				seen[scope] = true
				for _, other := range scopes {
					if other.chain == scope.chain {
						continue
					}
					domainOverlap := other.domain == "" || domain == "" || other.domain == domain || strings.HasSuffix(domain, "."+other.domain) || strings.HasSuffix(other.domain, "."+domain)
					if domainOverlap && (!source.IsValid() || !other.source.IsValid() || source.Overlaps(other.source)) {
						return nil, fmt.Errorf("Conflicting source and domain selectors in rule %d", i+1)
					}
				}
				scopes = append(scopes, scope)
				if len(scopes) > 1024 {
					return nil, errors.New("Expanded rule limit exceeded")
				}
				match := "DOMAIN-SUFFIX," + domain
				if source.IsValid() {
					prefix := "SRC-IP-CIDR," + source.String()
					if domain == "" {
						match = prefix
					} else {
						match = "AND,((" + prefix + "),(" + match + "))"
					}
				}
				nodes = append(nodes, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: match + "," + managedPrefix + rule.Chain})
			}
		}
	}
	return nodes, nil
}

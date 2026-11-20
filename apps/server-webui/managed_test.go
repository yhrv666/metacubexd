package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const testBase = "mode: rule\nproxies:\n  - {name: transit, type: socks5, server: transit.example, port: 1080}\nrules:\n  - DOMAIN-SUFFIX,example.test,DIRECT\n  - MATCH,REJECT\n"

func TestManagedChainsAndRules(t *testing.T) {
	for _, input := range []string{"socks5h://synthetic-user:synthetic-password@residential.example:1080/", "residential.example:1080:synthetic-user:synthetic-password", "[2001:db8::2]:1080:synthetic-user:synthetic-password"} {
		incoming := managedConfig{
			Chains: []managedChain{{ID: "home", Transit: "transit", Import: input}},
			Rules:  []managedRule{{Chain: "home", Presets: []string{"openai", "antigravity"}, Sources: []string{"192.0.2.12/24"}}},
		}
		result, stored, err := makeManaged([]byte(testBase), incoming, managedConfig{})
		if err != nil {
			t.Fatal(err)
		}
		output := string(result)
		for _, expected := range []string{"dialer-proxy: transit", "synthetic-password", "AND,((SRC-IP-CIDR,192.0.2.0/24),(DOMAIN-SUFFIX,openai.com)),MCXD-RESIDENTIAL-home", "DOMAIN,cloudcode-pa.googleapis.com", "DOMAIN,accounts.google.com", "DOMAIN-SUFFIX,example.test,DIRECT", "MATCH,REJECT"} {
			if !strings.Contains(output, expected) {
				t.Errorf("candidate lacks %s", expected)
			}
		}
		if strings.Index(output, "DOMAIN-SUFFIX,openai.com") > strings.Index(output, "DOMAIN-SUFFIX,example.test,DIRECT") {
			t.Fatal("managed rule order")
		}
		preview, _ := json.Marshal(stored.preview())
		if strings.Contains(string(preview), "synthetic") || strings.Contains(string(preview), "residential.example") {
			t.Fatal("preview contains confidential input")
		}
		incoming.Chains[0].Proxy = nil
		incoming.Chains[0].Import = ""
		if _, _, err := makeManaged([]byte(testBase), incoming, stored); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManagedRejectsInvalidReferencesAndConflicts(t *testing.T) {
	first := managedChain{ID: "first", Transit: "transit", Import: "socks5://residential.example:1080"}
	second := managedChain{ID: "second", Transit: "transit", Import: "socks5://other.example:1080"}
	for _, input := range []managedConfig{
		{Chains: []managedChain{{ID: "first", Transit: "DIRECT", Import: first.Import}}},
		{Chains: []managedChain{{ID: "first", Transit: "transit", Import: "socks5://127.0.0.1:9090"}}},
		{Chains: []managedChain{{ID: "first", Transit: "transit", Import: "socks5://transit.example:1080"}}},
		{Chains: []managedChain{first}, Rules: []managedRule{{Chain: "missing", Presets: []string{"openai"}}}},
		{Chains: []managedChain{first, second}, Rules: []managedRule{
			{Chain: "first", Domains: []string{"openai.com"}, Sources: []string{"192.0.2.0/24"}},
			{Chain: "second", Domains: []string{"api.openai.com"}, Sources: []string{"192.0.2.12/32"}},
		}},
	} {
		if _, _, err := makeManaged([]byte(testBase), input, managedConfig{}); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}

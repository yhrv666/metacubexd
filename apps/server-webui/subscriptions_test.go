package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSubscriptionRefreshPreservesManagedAndOriginalRules(t *testing.T) {
	prior := revision{Base: []byte(testBase), Subscriptions: []subscription{{ID: "fixture", URL: "https://provider.example/?token=synthetic-only", Cached: []byte("proxies:\n  - {name: relay, type: socks5, server: relay.example, port: 1080}\nrules: ['MATCH,DIRECT']\n")}}}
	draft := managedConfig{Chains: []managedChain{{ID: "home", Transit: "MCXD-SUB-fixture-relay", Import: "socks5://residential.example:1080"}}, Rules: []managedRule{{Chain: "home", Presets: []string{"openai", "antigravity"}}}}
	first, err := composeRevision(prior, draft)
	if err != nil {
		t.Fatal(err)
	}
	first.Subscriptions[0].Cached = []byte(strings.ReplaceAll(string(first.Subscriptions[0].Cached), "relay.example", "replacement.example"))
	refreshed, err := composeRevision(first, first.Managed)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(refreshed.Config)
	for _, expected := range []string{"replacement.example", "DOMAIN-SUFFIX,openai.com,MCXD-RESIDENTIAL-home", "DOMAIN,cloudcode-pa.googleapis.com,MCXD-RESIDENTIAL-home", "DOMAIN-SUFFIX,example.test,DIRECT", "MATCH,REJECT"} {
		if !strings.Contains(cfg, expected) {
			t.Fatalf("missing %s", expected)
		}
	}
	if strings.Contains(cfg, "MATCH,DIRECT") || strings.Contains(cfg, "relay.example") {
		t.Fatal("foreign rules or expired proxy retained")
	}
	view, _ := json.Marshal(summarizeSubscriptions(refreshed.Subscriptions))
	if strings.Contains(string(view), "synthetic-only") || strings.Contains(string(view), "provider.example") {
		t.Fatal("subscription credential read back")
	}
}

func TestSubscriptionOverlayAndOrderedHops(t *testing.T) {
	item := subscription{ID: "fixture", Cached: []byte("proxies:\n  - {name: relay, type: socks5, server: relay.example, port: 1080}\n")}
	if err := changeSubscriptionNode(&item, "socks5://fixture-user:fixture-password@exit.example:1080", "", false); err != nil {
		t.Fatal(err)
	}
	relay, exit := "MCXD-SUB-fixture-relay", item.localName("SOCKS5-1")
	prior := revision{Base: []byte(testBase), Subscriptions: []subscription{item}}
	managed := managedConfig{Chains: []managedChain{{ID: "home", Hops: []string{"transit", relay, exit}}}}
	first, err := composeRevision(prior, managed)
	if err != nil {
		t.Fatal(err)
	}
	first.Subscriptions[0].Cached = []byte(strings.ReplaceAll(string(item.Cached), "relay.example", "updated.example"))
	refreshed, err := composeRevision(first, first.Managed)
	if err != nil {
		t.Fatal(err)
	}
	root, err := configTree(refreshed.Config)
	if err != nil {
		t.Fatal(err)
	}
	middle := managedPrefix + "home.2"
	dialers := map[string]string{}
	for _, node := range inlineProxies(root) {
		var p baseProxy
		if node.Decode(&p) != nil {
			t.Fatal("could not decode generated proxy")
		}
		dialers[p.Name] = p.Dialer
	}
	if dialers[middle] != "transit" || dialers[managedPrefix+"home"] != middle || dialers[relay] != "" || dialers[exit] != "" {
		t.Fatal("chain order or original nodes changed")
	}
	if config := string(refreshed.Config); !strings.Contains(config, "fixture-password") || !strings.Contains(config, "updated.example") {
		t.Fatal("refresh lost a local or remote node")
	}
	view, _ := json.Marshal(summarizeSubscriptions(refreshed.Subscriptions))
	if strings.Contains(string(view), "fixture-password") || strings.Contains(string(view), "exit.example") {
		t.Fatal("subscription view disclosed local node credentials")
	}
	managed.Chains[0].Hops = []string{"transit", "transit"}
	if _, err := composeRevision(prior, managed); err == nil {
		t.Fatal("repeated hop accepted")
	}
}

func TestMigrationReportOmitsScriptAndConfigValues(t *testing.T) {
	script := []byte("curl https://provider.example/pull?token=synthetic-only > /private/config.yaml\ncurl -X PUT http://127.0.0.1:9090/configs\n")
	report, err := migrationCheck([]byte(testBase), script)
	if err != nil || !report.ScriptScanned || report.ScriptWriteIndicators < 1 || report.ScriptReloadIndicators != 1 || report.Rules != 2 {
		t.Fatalf("unexpected report: %+v, %v", report, err)
	}
	result, _ := json.Marshal(report)
	if strings.Contains(string(result), "synthetic-only") || strings.Contains(string(result), "transit") || strings.Contains(string(result), "/private") {
		t.Fatal("preflight report disclosed inputs")
	}
}

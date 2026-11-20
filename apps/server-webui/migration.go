package main

import (
	"errors"
	"regexp"
	"strings"
)

type migrationReport struct {
	ProxyNodes             int
	Groups                 int
	Rules                  int
	DialerLinks            int
	Providers              int
	ScopedRules            int
	ScriptScanned          bool
	ScriptWriteIndicators  int
	ScriptReloadIndicators int
	NeedsOperatorReview    bool
}

var legacyWrite = regexp.MustCompile("(?im)(>{1,2}\\s*[^&]|\\b(sed\\s+-i|tee|mv|cp|install|crontab)\\b)")
var legacyReload = regexp.MustCompile("(?im)(/configs\\b|systemctl\\s+(reload|restart)|docker\\s+(restart|kill)|\\bkill\\s+-[A-Z]+)")

func migrationCheck(config, script []byte) (migrationReport, error) {
	root, err := configTree(config)
	if err != nil {
		return migrationReport{}, errors.New("Migration preflight could not parse the startup YAML")
	}
	count := func(key string) int {
		n := yamlField(root, key)
		if n == nil {
			return 0
		}
		return len(n.Content)
	}
	report := migrationReport{ProxyNodes: count("proxies"), Groups: count("proxy-groups"), Rules: count("rules"), Providers: (count("proxy-providers") + count("rule-providers")) / 2, NeedsOperatorReview: true}
	nodes, err := baseProxies(root)
	if err != nil {
		return report, errors.New("Migration preflight found unsupported proxy names or definitions")
	}
	for _, node := range nodes {
		if node.Dialer != "" {
			report.DialerLinks++
		}
	}
	if rules := yamlField(root, "rules"); rules != nil {
		for _, rule := range rules.Content {
			if strings.Contains(rule.Value, "SRC-IP-CIDR,") {
				report.ScopedRules++
			}
		}
	}
	if script != nil {
		report.ScriptScanned = true
		report.ScriptWriteIndicators = len(legacyWrite.FindAllIndex(script, -1))
		report.ScriptReloadIndicators = len(legacyReload.FindAllIndex(script, -1))
	}
	return report, nil
}

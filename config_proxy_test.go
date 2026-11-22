package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestResolveNetworkProxy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cfg       Config
		available bool
		want      string
		probed    bool
	}{
		{"explicit wins", Config{NetworkProxy: " http://proxy.example.test:8080 "}, true, "http://proxy.example.test:8080", false},
		{"blank uses NAS", Config{}, true, "http://127.0.0.1:7890", true},
		{"whitespace uses NAS", Config{NetworkProxy: "  "}, true, "http://127.0.0.1:7890", true},
		{"missing local proxy uses direct", Config{}, false, "", true},
		{"do not use our own port", Config{ProxyPort: 7890}, true, "", false},
		{"do not use upstream server as proxy", Config{ServerPort: 7890}, true, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probed := false
			got := tc.cfg.resolveNetworkProxy(func() bool { probed = true; return tc.available })
			if got != tc.want || probed != tc.probed {
				t.Fatalf("proxy = %q, detected = %t; want %q, %t", got, probed, tc.want, tc.probed)
			}
		})
	}
}

func TestProxyEnvKeepsLocalAndMirrorBypasses(t *testing.T) {
	const proxy = "http://127.0.0.1:7890"
	readEnv := func(entries []string) map[string]string {
		env := make(map[string]string)
		for _, entry := range entries {
			key, value, _ := strings.Cut(entry, "=")
			env[key] = value
		}
		return env
	}
	cfg := Config{NetworkProxy: proxy}
	disabled := readEnv(networkProxyEnv(cfg, false))
	if disabled["HTTPS_PROXY"] != "" || disabled["NODE_USE_ENV_PROXY"] != "" {
		t.Fatal("runtime proxy disabled but proxy variables are present")
	}
	enabled := readEnv(proxyEnv(Config{}, proxy))
	if enabled["HTTPS_PROXY"] != proxy || enabled["npm_config_https_proxy"] != proxy || enabled["NODE_USE_ENV_PROXY"] != "1" {
		t.Fatal("missing proxy variables for NAS default")
	}
	for _, bypass := range []string{"127.0.0.1", "192.168.0.0/16", "registry.npmmirror.com"} {
		if !strings.Contains(enabled["NO_PROXY"], bypass) {
			t.Fatalf("missing proxy bypass for %s", bypass)
		}
	}
}

func TestOutboundTransportRespectsRegistryChoice(t *testing.T) {
	cfg := Config{NetworkProxy: "http://proxy.example.test:7890"}
	transport, err := cfg.outboundTransport(cfg.usesOfficialRegistry())
	if err != nil || transport.Proxy != nil {
		t.Fatalf("mirror transport must connect directly: %v", err)
	}
	cfg.NpmRegistry = "https://registry.npmjs.org"
	transport, err = cfg.outboundTransport(cfg.usesOfficialRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if transport.Proxy == nil {
		t.Fatal("official registry must use the configured proxy")
	}
	req, err := http.NewRequest(http.MethodGet, cfg.GetNpmRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := transport.Proxy(req)
	if err != nil || proxy == nil || proxy.String() != cfg.NetworkProxy {
		t.Fatalf("proxy = %v, err = %v; want %s", proxy, err, cfg.NetworkProxy)
	}
}

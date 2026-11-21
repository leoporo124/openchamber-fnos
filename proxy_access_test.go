package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyListenAddress(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"default", Config{}, "127.0.0.1:3001"},
		{"upgraded passwordless port mode", Config{ProxyPort: 4900, AccessMode: "port"}, "127.0.0.1:4900"},
		{"whitespace only password", Config{AccessPassword: "  "}, "127.0.0.1:3001"},
		{"password protected", Config{ProxyPort: 4900, AccessPassword: "test-password"}, "0.0.0.0:4900"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.proxyListenAddress(); got != tt.want {
				t.Fatalf("listen address = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProxyWithAuthIndependentPortAndGateway(t *testing.T) {
	previous := GetConfig()
	t.Cleanup(func() {
		configMu.Lock()
		globalConfig = previous
		configMu.Unlock()
	})
	const password = "test-password"
	for _, tt := range []struct {
		name, prefix, peer, password string
		withCookie                   bool
		want                         int
	}{
		{"anonymous remote TCP", "", "192.168.1.8:1234", "", false, http.StatusForbidden},
		{"NAS reverse proxy", "", "127.0.0.1:1234", "", false, http.StatusNoContent},
		{"passwordless gateway after gateway auth", fnGatewayPrefix, "@", "", false, http.StatusNoContent},
		{"remote TCP requires login", "", "192.168.1.8:1234", password, false, http.StatusUnauthorized},
		{"remote TCP with session", "", "192.168.1.8:1234", password, true, http.StatusNoContent},
		{"gateway with additional password requires login", fnGatewayPrefix, "@", password, false, http.StatusUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			configMu.Lock()
			globalConfig.AccessPassword = tt.password
			configMu.Unlock()
			req := httptest.NewRequest(http.MethodGet, tt.prefix+"/api/dev-servers", nil)
			req.RemoteAddr = tt.peer
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			if tt.withCookie {
				req.AddCookie(&http.Cookie{Name: authCookieName, Value: getAuthToken(tt.password)})
			}
			res := httptest.NewRecorder()
			proxyWithAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}), tt.prefix).ServeHTTP(res, req)
			if res.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", res.Code, tt.want, res.Body)
			}
		})
	}
}

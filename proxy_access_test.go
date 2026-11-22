package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
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

func TestWebSocketOriginThroughProxies(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		// Match the upstream preference for X-Forwarded-Host over Host.
		return r.Header.Get("Origin") == "http://"+r.Header.Get("X-Forwarded-Host")
	}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/terminal/ws" && r.URL.Path != "/api/global/event/ws" {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte("ready"))
	}))
	t.Cleanup(upstream.Close)

	available, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := available.Addr().(*net.TCPAddr).Port
	available.Close()
	previous := GetConfig()
	configMu.Lock()
	globalConfig = Config{ServerPort: upstream.Listener.Addr().(*net.TCPAddr).Port, ProxyPort: port}
	configMu.Unlock()
	t.Cleanup(func() {
		stopReverseProxy()
		configMu.Lock()
		globalConfig = previous
		configMu.Unlock()
	})
	if err := startReverseProxy(); err != nil {
		t.Fatal(err)
	}

	socket, err := net.Listen("unix", filepath.Join(t.TempDir(), "web.sock"))
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	WebFS = embeddedWebFS
	InitRoutes(router)
	server := &http.Server{Handler: router}
	go func() { _ = server.Serve(socket) }()
	t.Cleanup(func() { _ = server.Close() })
	gatewayDialer := websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket.Addr().String())
		},
		HandshakeTimeout: 3 * time.Second,
	}
	independentDialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	check := func(t *testing.T, dialer *websocket.Dialer, target string, headers http.Header, want int) {
		t.Helper()
		conn, resp, err := dialer.Dial(target, headers)
		if resp != nil {
			defer resp.Body.Close()
		}
		if conn != nil {
			defer conn.Close()
		}
		if resp == nil || resp.StatusCode != want {
			t.Fatalf("handshake response = %v, err = %v; want status %d", resp, err, want)
		}
		if conn != nil {
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, data, err := conn.ReadMessage(); err != nil || string(data) != "ready" {
				t.Fatalf("read from upstream: %q, %v", data, err)
			}
		}
	}
	const terminal = "/api/terminal/ws"
	gateway := "ws://nas.example.test" + fnGatewayPrefix
	independent := GetConfig().proxyListenAddress()
	admin := http.Header{"X-Trim-Isadmin": {"true"}, "X-Trim-Userid": {"1000"}}
	sameGateway := admin.Clone()
	sameGateway.Set("Origin", "http://nas.example.test:5666")
	foreignGateway := admin.Clone()
	foreignGateway.Set("Origin", "http://other.example.test")

	t.Run("gateway terminal with stripped port", func(t *testing.T) {
		check(t, &gatewayDialer, gateway+terminal, sameGateway, http.StatusSwitchingProtocols)
	})
	t.Run("gateway event stream", func(t *testing.T) {
		check(t, &gatewayDialer, gateway+"/api/global/event/ws", sameGateway, http.StatusSwitchingProtocols)
	})
	t.Run("gateway cross-origin rejected", func(t *testing.T) {
		check(t, &gatewayDialer, gateway+terminal, foreignGateway, http.StatusForbidden)
	})
	t.Run("gateway without admin rejected", func(t *testing.T) {
		check(t, &gatewayDialer, gateway+terminal, http.Header{"Origin": {sameGateway.Get("Origin")}}, http.StatusForbidden)
	})
	t.Run("independent port terminal", func(t *testing.T) {
		check(t, &independentDialer, "ws://"+independent+terminal, http.Header{"Origin": {"http://" + independent}}, http.StatusSwitchingProtocols)
	})
	t.Run("independent port cross-origin rejected", func(t *testing.T) {
		check(t, &independentDialer, "ws://"+independent+terminal, http.Header{"Origin": {"http://other.example.test"}}, http.StatusForbidden)
	})
}

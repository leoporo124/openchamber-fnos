package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestFilterDevServerDiscovery(t *testing.T) {
	const openList = `{"port":5244,"pid":123,"command":"openlist","directory":"/vol1/share","url":"http://localhost:5244/","extra":true}`
	const vite = `{"port":5173,"command":"node","url":"http://localhost:5173/"}`
	const upstream = `{"servers":[{"port":111},{"port":2049,"pid":null,"command":""},{"port":20048},{"port":40513,"command":"rpc.mountd"},{"port":41750,"pid":null,"command":""},` + openList + `,` + vite + `],"other":true}`
	for _, tt := range []struct {
		name, method, path string
		status             int
		filtered           bool
	}{
		{"discovery", http.MethodGet, "/api/dev-servers", http.StatusOK, true},
		{"trailing slash and query", http.MethodGet, "/api/dev-servers/?any=value", http.StatusOK, true},
		{"upstream unavailable", http.MethodGet, "/api/dev-servers", http.StatusServiceUnavailable, false},
		{"other method", http.MethodPost, "/api/dev-servers", http.StatusOK, false},
		{"Space", http.MethodGet, "/api/spaces/space-1/dev-servers", http.StatusOK, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("If-None-Match", `"previous"`)
			prepareDevServerDiscovery(req)
			if (req.Header.Get("If-None-Match") == "") != (tt.filtered || tt.status == http.StatusServiceUnavailable) {
				t.Fatalf("unexpected cache validation header for %s %s", tt.method, tt.path)
			}
			resp := &http.Response{Request: req, StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(upstream)), Header: http.Header{
				"Content-Type": {"application/json; charset=utf-8"}, "Etag": {`"upstream"`}, "Last-Modified": {"Tue, 10 Nov 2026 12:00:00 GMT"},
			}}
			if err := filterDevServerDiscoveryWithRPCPorts(resp, func(context.Context) map[int]bool {
				if !tt.filtered {
					t.Fatal("RPC lookup for unrelated response")
				}
				return map[int]bool{41750: true}
			}); err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !tt.filtered {
				if string(body) != upstream || resp.Header.Get("ETag") == "" {
					t.Fatalf("unrelated response changed: %s %v", body, resp.Header)
				}
				return
			}
			var got struct {
				Servers []json.RawMessage `json:"servers"`
				Other   bool              `json:"other"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if !got.Other || len(got.Servers) != 2 || string(got.Servers[0]) != openList || string(got.Servers[1]) != vite {
				t.Fatalf("unexpected discovered servers: %s", body)
			}
			if resp.ContentLength != int64(len(body)) || resp.Header.Get("Content-Length") != strconv.Itoa(len(body)) ||
				resp.Header.Get("ETag") != "" || resp.Header.Get("Last-Modified") != "" || resp.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("unexpected headers: %v", resp.Header)
			}
		})
	}
}

func TestFilterDevServerDiscoveryKeepsJSONShape(t *testing.T) {
	for input, want := range map[string]string{
		`{"servers":[{"port":2049}]}`: `{"servers":[]}`,
		`{"results":[{"port":2049}]}`: `{"results":[{"port":2049}]}`,
	} {
		resp := &http.Response{
			Request: httptest.NewRequest(http.MethodGet, "/api/dev-servers", nil), StatusCode: http.StatusOK,
			Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(input)),
		}
		err := filterDevServerDiscoveryWithRPCPorts(resp, func(context.Context) map[int]bool { return nil })
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || readErr != nil || string(body) != want {
			t.Fatalf("input %s: got %s, errors %v %v, want %s", input, body, err, readErr, want)
		}
	}
}

func TestParseNFSRPCPorts(t *testing.T) {
	const output = `   program vers proto   port  service
    100000    4   tcp     111  portmapper
    100005    1   udp    7412  mountd
    100005    1   tcp    9024  mountd
    100021    1   tcp   41750
    100021    3   tcp   41750  nlockmgr
    100024    1   tcp   42423  status
    765432    1   tcp    5244  unrelated
    100003    4   tcp       0  nfs
    100003    4   tcp   99999  nfs
`
	got := parseNFSRPCPorts(output)
	if len(got) != 4 || !got[111] || !got[9024] || !got[41750] || !got[42423] {
		t.Fatalf("unexpected registered ports: %v", got)
	}
}

func TestDevServerDiscoveryThroughProxies(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dev-servers/" || r.URL.Query().Get("test") != "1" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"servers":[{"port":2049},{"port":5244,"url":"http://localhost:5244/"}]}`)
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
	const path = "/api/dev-servers/?test=1"
	client := &http.Client{Transport: localTransport, Timeout: 3 * time.Second}
	checkResponse := func(t *testing.T, url, header, value string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(header, value)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" || string(data) != `{"servers":[{"port":5244,"url":"http://localhost:5244/"}]}` {
			t.Fatalf("unexpected response: %d %v %s", resp.StatusCode, resp.Header, data)
		}
	}

	t.Run("independent port", func(t *testing.T) {
		checkResponse(t, "http://"+GetConfig().proxyListenAddress()+path, "If-None-Match", `"previous"`)
	})
	t.Run("fnOS gateway proxy", func(t *testing.T) {
		router := gin.New()
		InitFnGateway(router.Group(basePath))
		gateway := httptest.NewServer(router)
		t.Cleanup(gateway.Close)
		checkResponse(t, gateway.URL+fnGatewayPrefix+path, "If-Modified-Since", "Tue, 10 Nov 2026 12:00:00 GMT")
	})
}

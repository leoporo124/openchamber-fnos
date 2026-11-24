package main

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGatewayWaitsForReadiness(t *testing.T) {
	const splash = `<div id="initial-loading"></div><script>
// Fallback: hide loading screen after 10 seconds if React fails to load.
setTimeout(function() { document.getElementById('initial-loading')?.remove() }, 10000);
</script>`
	const csp = "frame-ancestors 'self'"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", csp)
		fmt.Fprint(w, `<html><head></head><body>`+splash+`</body></html>`)
	}))
	defer upstream.Close()
	oldState, oldConfig := state, GetConfig()
	state = &ServiceState{status: StatusStarting}
	configMu.Lock()
	globalConfig = Config{ServerPort: upstream.Listener.Addr().(*net.TCPAddr).Port}
	configMu.Unlock()
	t.Cleanup(func() {
		state = oldState
		configMu.Lock()
		globalConfig = oldConfig
		configMu.Unlock()
	})
	router := gin.New()
	InitFnGateway(router.Group(basePath))
	request := func(method string) *httptest.ResponseRecorder {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		req := httptest.NewRequestWithContext(ctx, method, fnGatewayPrefix+"/", nil)
		req.Header.Set("Accept", "text/html")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	page := request(http.MethodGet)
	if page.Code != http.StatusServiceUnavailable || !strings.Contains(page.Body.String(), "服务启动中") {
		t.Fatalf("expected startup page, got HTTP %d", page.Code)
	}
	probe := request(http.MethodHead)
	if probe.Code != http.StatusServiceUnavailable || probe.Header().Get("X-OpenChamber-Wait") != "1" || probe.Body.Len() != 0 {
		t.Fatalf("unexpected startup probe: %d, %v, %q", probe.Code, probe.Header(), probe.Body)
	}
	state.SetStatus(StatusRunning, "")
	probe = request(http.MethodHead)
	if probe.Code != http.StatusOK || probe.Header().Get("X-OpenChamber-Wait") != "" {
		t.Fatalf("probe not released after readiness: %d, %v", probe.Code, probe.Header())
	}
	page = request(http.MethodGet)
	if body := page.Body.String(); page.Code != http.StatusOK || !strings.Contains(body, splash) ||
		page.Header().Get("Content-Security-Policy") != csp || strings.Contains(body, "webui-startup.js") {
		t.Fatalf("gateway changed the upstream splash or CSP: %d, %v", page.Code, page.Header())
	}
}

func TestGatewayAssetDelivery(t *testing.T) {
	const script = `/assets/main-abcd1234.js`
	const unmodified = `/assets/font-abcd1234.woff2`
	const raw = `const font = "/assets/font-abcd1234.woff2";` + "\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("ETag", `"original"`)
		switch r.URL.Path {
		case script:
			if r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("If-None-Match") != "" || r.Header.Get("Range") != "" {
				t.Errorf("rewritten asset must be fetched in full: %v", r.Header)
			}
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, strings.Repeat(raw, 100))
		case unmodified:
			if r.Header.Get("Accept-Encoding") != "br, gzip" || r.Header.Get("If-None-Match") != `"original"` {
				t.Errorf("unmodified asset lost request headers: %v", r.Header)
			}
			w.WriteHeader(http.StatusNotModified)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	oldConfig := GetConfig()
	configMu.Lock()
	globalConfig = Config{ServerPort: upstream.Listener.Addr().(*net.TCPAddr).Port}
	configMu.Unlock()
	t.Cleanup(func() {
		configMu.Lock()
		globalConfig = oldConfig
		configMu.Unlock()
	})
	router := gin.New()
	InitFnGateway(router.Group(basePath))
	request := func(path, encoding string) *httptest.ResponseRecorder {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, fnGatewayPrefix+path, nil)
		req.Header.Set("Accept-Encoding", encoding)
		req.Header.Set("If-None-Match", `"original"`)
		if path == script {
			req.Header.Set("Range", "bytes=0-15")
		}
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	want := strings.Repeat(strings.Replace(raw, "/assets/", fnGatewayPrefix+"/assets/", 1), 100)
	for _, tt := range []struct {
		encoding string
		gzip     bool
	}{{"br, gzip", true}, {"br, gzip;q=0", false}, {"identity", false}} {
		res := request(script, tt.encoding)
		if res.Code != http.StatusOK || (res.Header().Get("Content-Encoding") == "gzip") != tt.gzip ||
			res.Header().Get("Vary") != "Accept-Encoding" || res.Header().Get("ETag") != "" ||
			!strings.Contains(res.Header().Get("Cache-Control"), "private, max-age=") {
			t.Fatalf("encoding %q: unexpected response %d %v", tt.encoding, res.Code, res.Header())
		}
		body := res.Body.String()
		if tt.gzip {
			reader, err := gzip.NewReader(res.Body)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			body = string(data)
		}
		if body != want {
			t.Fatal("rewritten asset is incomplete or has the wrong prefix")
		}
	}
	res := request(unmodified, "br, gzip")
	if res.Code != http.StatusNotModified || res.Header().Get("ETag") != `"original"` ||
		res.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Fatalf("unexpected unchanged asset response %d %v", res.Code, res.Header())
	}
}

func TestSnapshotCancellationRemainsVisibleUntilTaskFinishes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snapshotProgressMu.Lock()
	previous, previousCancel := snapshotProgress, snapshotCancel
	snapshotCancel = cancel
	snapshotProgressMu.Unlock()
	t.Cleanup(func() {
		snapshotProgressMu.Lock()
		snapshotProgress, snapshotCancel = previous, previousCancel
		snapshotProgressMu.Unlock()
	})
	setSnapshotProgress(SnapshotProgress{Active: true, CanCancel: true, Action: "create", Stage: "正在打包持久化数据"})
	if err := CancelSnapshotCreation(); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != context.Canceled || CancelSnapshotCreation() == nil {
		t.Fatal("expected one cancellation")
	}
	setSnapshotProgress(SnapshotProgress{Active: true, CanCancel: true, Action: "create", Stage: "正在校验归档"})
	if got := GetCurrentSnapshotProgress(); !got.Active || got.CanCancel || got.Stage != "正在取消快照创建" {
		t.Fatalf("late stage overwrote cancellation: %+v", got)
	}
	finishSnapshotTask("create", ctx.Err())
	if got := GetCurrentSnapshotProgress(); got.Active || got.CanCancel || got.Error != "" || got.Stage != "快照创建已取消" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestSnapshotArchiveRoundTrip(t *testing.T) {
	previousData, previousSnapshots, previousRuntime := globalPkgVar, globalSnapshotsDir, runtimeDir
	previousProgress := GetCurrentSnapshotProgress()
	globalPkgVar = t.TempDir()
	globalSnapshotsDir = filepath.Join(globalPkgVar, "snapshots")
	runtimeDir = filepath.Join(globalPkgVar, "runtime")
	t.Cleanup(func() {
		globalPkgVar, globalSnapshotsDir, runtimeDir = previousData, previousSnapshots, previousRuntime
		setSnapshotProgress(previousProgress)
	})
	files := map[string]string{
		"config.json":                       `{"server_port":3000}`,
		"data/opencode/opencode.db":         "session data",
		"openchamber-data/cache/ignored.js": "cache data",
		"projects/example/main.go":          "project source",
	}
	for name, content := range files {
		target := filepath.Join(globalPkgVar, name)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := createSnapshot(ctx, CreateSnapshotParams{Name: "round-trip", CompressionLevel: 1}); err != nil {
		t.Fatal(err)
	}
	summary, err := ListSnapshots()
	if err != nil || len(summary.Items) != 1 {
		t.Fatalf("expected one completed snapshot, got %+v: %v", summary.Items, err)
	}
	meta := summary.Items[0]
	archive := filepath.Join(globalSnapshotsDir, meta.ID, "data.tar.gz")
	if digest, err := hashSnapshotArchive(ctx, archive); err != nil || digest != meta.ArchiveSHA256 {
		t.Fatalf("archive digest mismatch: %q, %v", digest, err)
	}
	stage := t.TempDir()
	if err := extractSnapshot(archive, stage); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", "data/opencode/opencode.db"} {
		if got, err := os.ReadFile(filepath.Join(stage, name)); err != nil || string(got) != files[name] {
			t.Fatalf("unexpected contents for %s: %q, %v", name, got, err)
		}
	}
	for _, name := range []string{"openchamber-data/cache/ignored.js", "projects/example/main.go"} {
		if _, err := os.Stat(filepath.Join(stage, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected archive entry %s: %v", name, err)
		}
	}
}

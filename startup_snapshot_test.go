package main

import (
	"context"
	"fmt"
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
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head></head><body id="root">upstream</body></html>`)
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
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), fnGatewayPrefix+fnOSAssetsPath+"webui-startup.js") {
		t.Fatalf("ready page did not receive startup fallback: %d, %q", page.Code, page.Body)
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

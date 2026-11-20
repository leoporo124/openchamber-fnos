package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const snapshotFormatVersion = 1

var (
	validIDRegex         = regexp.MustCompile(`^snap_[0-9]{8}_[0-9]{6}_[a-f0-9]{12}$`)
	snapshotSubsMu       sync.Mutex
	snapshotSubs         = make(map[chan struct{}]struct{})
	snapshotProgressMu   sync.RWMutex
	snapshotProgress     SnapshotProgress
	snapshotProgressSubs = make(map[chan SnapshotProgress]struct{})
	snapshotCancel       context.CancelFunc
	// 仅归档应用持久化数据；OpenCode 工作树不在清单中。
	snapshotTargets = []string{"config.json", "openchamber-data", "config/opencode", "data/opencode/opencode.db", "data/opencode/opencode.db-wal", "data/opencode/opencode.db-shm", "data/opencode/auth.json"}
)

type SnapshotProgress struct {
	Active  bool   `json:"active"`
	Action  string `json:"action"`
	Percent int    `json:"percent"`
	Stage   string `json:"stage"`
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

type SnapshotMeta struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	CreatedAt          int64  `json:"created_at"`
	SizeBytes          int64  `json:"size_bytes"`
	AppVersion         string `json:"app_version"`
	OpenChamberVersion string `json:"openchamber_version"`
	OpenCodeVersion    string `json:"opencode_version"`
	FormatVersion      int    `json:"format_version"`
	CompressionLevel   int    `json:"compression_level"`
	ArchiveSHA256      string `json:"archive_sha256"`
}

type CreateSnapshotParams struct {
	Name             string `json:"name"`
	CompressionLevel int    `json:"compression_level"`
}

type SnapshotSummary struct {
	Items          []SnapshotMeta   `json:"items"`
	TotalSizeBytes int64            `json:"total_size_bytes"`
	DiskFreeBytes  uint64           `json:"disk_free_bytes"`
	DiskTotalBytes uint64           `json:"disk_total_bytes"`
	CurrentTask    SnapshotProgress `json:"current_task"`
}

func GetCurrentSnapshotProgress() SnapshotProgress {
	snapshotProgressMu.RLock()
	defer snapshotProgressMu.RUnlock()
	return snapshotProgress
}

func setSnapshotProgress(progress SnapshotProgress) {
	snapshotProgressMu.Lock()
	defer snapshotProgressMu.Unlock()
	snapshotProgress = progress
	for ch := range snapshotProgressSubs {
		select {
		case ch <- progress:
		default:
		}
	}
}

func SubscribeSnapshotProgress(buf int) (<-chan SnapshotProgress, func()) {
	ch := make(chan SnapshotProgress, buf)
	snapshotProgressMu.Lock()
	snapshotProgressSubs[ch] = struct{}{}
	snapshotProgressMu.Unlock()
	return ch, func() { snapshotProgressMu.Lock(); delete(snapshotProgressSubs, ch); snapshotProgressMu.Unlock() }
}

func SubscribeSnapshot(buf int) (<-chan struct{}, func()) {
	ch := make(chan struct{}, buf)
	snapshotSubsMu.Lock()
	snapshotSubs[ch] = struct{}{}
	snapshotSubsMu.Unlock()
	return ch, func() { snapshotSubsMu.Lock(); delete(snapshotSubs, ch); snapshotSubsMu.Unlock() }
}

func notifySnapshot() {
	snapshotSubsMu.Lock()
	defer snapshotSubsMu.Unlock()
	for ch := range snapshotSubs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func CancelSnapshotCreation() error {
	snapshotProgressMu.Lock()
	defer snapshotProgressMu.Unlock()
	if snapshotCancel == nil {
		return fmt.Errorf("当前没有正在执行的快照创建任务")
	}
	snapshotCancel()
	return nil
}

func formatBytes(bytes uint64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value, unit := float64(bytes), 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

func checkSnapshotDisk(required uint64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(globalPkgVar, &stat); err != nil {
		return fmt.Errorf("无法读取可用磁盘空间: %w", err)
	}
	if stat.Bavail*uint64(stat.Bsize) < required {
		return fmt.Errorf("可用磁盘空间不足，需要至少 %s", formatBytes(required))
	}
	return nil
}

func ListSnapshots() (SnapshotSummary, error) {
	summary := SnapshotSummary{Items: []SnapshotMeta{}, CurrentTask: GetCurrentSnapshotProgress()}
	if err := os.MkdirAll(globalSnapshotsDir, 0700); err != nil {
		return summary, err
	}
	entries, err := os.ReadDir(globalSnapshotsDir)
	if err != nil {
		return summary, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validIDRegex.MatchString(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(globalSnapshotsDir, entry.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var meta SnapshotMeta
		if json.Unmarshal(data, &meta) != nil || meta.ID != entry.Name() {
			continue
		}
		summary.Items = append(summary.Items, meta)
		summary.TotalSizeBytes += meta.SizeBytes
	}
	sort.Slice(summary.Items, func(i, j int) bool { return summary.Items[i].CreatedAt > summary.Items[j].CreatedAt })
	var stat syscall.Statfs_t
	if syscall.Statfs(globalPkgVar, &stat) == nil {
		summary.DiskFreeBytes, summary.DiskTotalBytes = stat.Bavail*uint64(stat.Bsize), stat.Blocks*uint64(stat.Bsize)
	}
	return summary, nil
}

func isSnapshotPath(name string) bool {
	name = strings.TrimSuffix(name, "/")
	if name == "" || path.Clean(name) != name || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return false
	}
	isAllowed := false
	for _, target := range snapshotTargets {
		if name == target || ((target == "openchamber-data" || target == "config/opencode") && strings.HasPrefix(name, target+"/")) {
			isAllowed = true
			break
		}
	}
	if !isAllowed {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		switch part {
		case "node_modules", ".git", "cache", "chats", "speech-models", "managed-opencode", "logs":
			return false
		}
	}
	return true
}

func collectSnapshotFiles(base string) ([]string, int64, error) {
	files := []string{}
	var total int64
	for _, target := range snapshotTargets {
		if err := validateSnapshotParents(base, target); err != nil {
			return nil, 0, err
		}
		err := filepath.Walk(filepath.Join(base, filepath.FromSlash(target)), func(full string, info os.FileInfo, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(base, full)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if !isSnapshotPath(relative) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("快照范围内不支持符号链接: %s", relative)
			}
			if info.Mode().IsRegular() {
				files = append(files, relative)
				total += info.Size()
			}
			return nil
		})
		if err != nil {
			return nil, 0, err
		}
	}
	return files, total, nil
}

func beginSnapshotTask(action string) (bool, error) {
	if err := checkSnapshotResources(); err != nil {
		return false, err
	}
	if !maintenanceMu.TryLock() {
		return false, fmt.Errorf("正在执行服务操作或维护任务，请稍候")
	}
	if state.Status() == StatusStarting {
		maintenanceMu.Unlock()
		return false, fmt.Errorf("服务正在启动中，请稍候")
	}
	if err := checkMaintenanceRecovery(); err != nil {
		maintenanceMu.Unlock()
		return false, err
	}
	if err := cleanupSnapshotArtifacts(); err != nil {
		LogWarning("[快照] 上次任务的临时文件尚未清理: %s", err)
	}
	wasRunning := GetActiveServicePID() > 0
	state.SetStatus(StatusSnapshotting, "正在执行快照维护")
	setSnapshotProgress(SnapshotProgress{Active: true, Action: action, Stage: "正在准备快照维护"})
	return wasRunning, nil
}

func checkSnapshotResources() error {
	const minAvailable = 256 * 1024 * 1024
	const loadPerCore = 2.5
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[0] != "MemAvailable:" {
				continue
			}
			available, err := strconv.ParseUint(fields[1], 10, 64)
			if err == nil && available*1024 < minAvailable {
				return fmt.Errorf("系统可用内存不足，快照操作需要至少 256 MiB")
			}
			break
		}
	}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			load, err := strconv.ParseFloat(fields[0], 64)
			if err == nil && load > float64(runtime.NumCPU())*loadPerCore {
				LogWarning("[快照] 当前系统负载较高，快照任务可能耗时较长")
			}
		}
	}
	return nil
}

func finishSnapshotTask(action string, err error) {
	if err != nil {
		stage := "快照任务未完成"
		message := err.Error()
		if action == "create" && errors.Is(err, context.Canceled) {
			stage = "快照创建已取消"
			message = stage
			LogInfo("[快照] %s", stage)
		} else {
			LogError("[快照] %s 失败: %s", action, err)
		}
		setSnapshotProgress(SnapshotProgress{Action: action, Stage: stage, Error: message})
	} else {
		setSnapshotProgress(SnapshotProgress{Action: action, Percent: 100, Stage: "快照任务完成"})
	}
	notifySnapshot()
}

func queueSnapshotCreation(params CreateSnapshotParams) error {
	if !isRuntimeReady() {
		return fmt.Errorf("请先部署兼容运行版本，再创建快照")
	}
	params.Name = strings.TrimSpace(params.Name)
	if params.Name == "" || len([]rune(params.Name)) > 40 {
		return fmt.Errorf("快照名称必须为 1 ~ 40 个字符")
	}
	if params.CompressionLevel < 1 || params.CompressionLevel > 9 {
		params.CompressionLevel = gzip.BestSpeed
	}
	if err := checkSnapshotDisk(1024 * 1024 * 1024); err != nil {
		return err
	}
	wasRunning, err := beginSnapshotTask("create")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(appContext)
	snapshotProgressMu.Lock()
	snapshotCancel = cancel
	snapshotProgressMu.Unlock()
	go func() {
		defer maintenanceMu.Unlock()
		defer cancel()
		stopAndWait()
		err := createSnapshot(ctx, params)
		snapshotProgressMu.Lock()
		snapshotCancel = nil
		snapshotProgressMu.Unlock()
		state.SetStatus(StatusStopped, "")
		if wasRunning && appContext.Err() == nil {
			if startErr := startService(); startErr != nil {
				state.SetStatus(StatusStopped, "快照维护后启动失败: "+startErr.Error())
				if err == nil {
					err = startErr
				}
			}
		}
		finishSnapshotTask("create", err)
	}()
	return nil
}

func createSnapshot(ctx context.Context, params CreateSnapshotParams) (resultErr error) {
	summary, err := ListSnapshots()
	if err != nil {
		return err
	}
	for _, item := range summary.Items {
		if strings.EqualFold(item.Name, params.Name) {
			return fmt.Errorf("已存在同名快照「%s」", params.Name)
		}
	}
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	id := "snap_" + time.Now().Format("20060102_150405") + "_" + hex.EncodeToString(random)
	directory := filepath.Join(globalSnapshotsDir, ".creating-"+id)
	if err := os.Mkdir(directory, 0700); err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			if err := os.RemoveAll(directory); err != nil {
				LogWarning("[快照] 清理未完成归档失败: %s", err)
			}
		}
	}()
	files, total, err := collectSnapshotFiles(globalPkgVar)
	if err != nil {
		return err
	}
	if err := checkSnapshotDisk(uint64(total) + 100*1024*1024); err != nil {
		return err
	}
	archive := filepath.Join(directory, "data.tar.gz")
	if err := archiveSnapshot(ctx, archive, files, total, params.CompressionLevel); err != nil {
		return err
	}
	info, err := os.Stat(archive)
	if err != nil {
		return err
	}
	meta := SnapshotMeta{ID: id, Name: params.Name, CreatedAt: time.Now().Unix(), SizeBytes: info.Size(), AppVersion: globalAppVer, OpenChamberVersion: readVersion(), OpenCodeVersion: readOpenCodeVersion(), FormatVersion: snapshotFormatVersion, CompressionLevel: params.CompressionLevel}
	meta.ArchiveSHA256, err = hashSnapshotArchive(ctx, archive)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(directory, "meta.json"), meta); err != nil {
		return err
	}
	completed := filepath.Join(globalSnapshotsDir, id)
	if err := os.Rename(directory, completed); err != nil {
		return err
	}
	directory = completed
	if err := syncSnapshotDirectory(globalSnapshotsDir); err != nil {
		return err
	}
	LogInfo("[快照] 已创建 %s，归档大小 %s", id, formatBytes(uint64(meta.SizeBytes)))
	return nil
}

type snapshotReader struct {
	ctx    context.Context
	reader io.Reader
	onRead func(int)
}

func (r *snapshotReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(buffer)
	r.onRead(n)
	return n, err
}

func archiveSnapshot(ctx context.Context, archive string, files []string, total int64, level int) (resultErr error) {
	file, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if err := file.Close(); resultErr == nil {
			resultErr = err
		}
	}()
	compressed, err := gzip.NewWriterLevel(file, level)
	if err != nil {
		return err
	}
	defer compressed.Close()
	writer := tar.NewWriter(compressed)
	defer writer.Close()
	var processed int64
	var lastReport time.Time
	onRead := func(n int) {
		processed += int64(n)
		if time.Since(lastReport) < 150*time.Millisecond {
			return
		}
		lastReport = time.Now()
		percent := 5
		if total > 0 {
			percent += int(processed * 85 / total)
		}
		if percent > 90 {
			percent = 90
		}
		setSnapshotProgress(SnapshotProgress{Active: true, Action: "create", Percent: percent, Stage: "正在打包持久化数据", Message: formatBytes(uint64(processed)) + " / " + formatBytes(uint64(total))})
	}
	for _, name := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := appendSnapshotFile(writer, name, &snapshotReader{ctx: ctx, onRead: onRead}); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	return file.Sync()
}

func appendSnapshotFile(writer *tar.Writer, name string, reader *snapshotReader) error {
	file, err := os.Open(filepath.Join(globalPkgVar, filepath.FromSlash(name)))
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("快照源文件类型无效: %s", name)
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name, header.Mode = name, 0600
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	reader.reader = file
	_, err = io.Copy(writer, reader)
	return err
}

func ValidateSnapshotForRestore(id string) (*SnapshotMeta, error) {
	if !validIDRegex.MatchString(id) {
		return nil, fmt.Errorf("快照 ID 无效")
	}
	data, err := os.ReadFile(filepath.Join(globalSnapshotsDir, id, "meta.json"))
	if err != nil {
		return nil, fmt.Errorf("无法读取快照元数据: %w", err)
	}
	var meta SnapshotMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("快照元数据无效: %w", err)
	}
	if meta.ID != id || meta.FormatVersion != snapshotFormatVersion {
		return nil, fmt.Errorf("快照格式不兼容")
	}
	if meta.SizeBytes < 0 || len(meta.ArchiveSHA256) != sha256.Size*2 {
		return nil, fmt.Errorf("快照完整性信息无效")
	}
	if _, err := hex.DecodeString(meta.ArchiveSHA256); err != nil {
		return nil, fmt.Errorf("快照完整性信息无效")
	}
	web, webOK := parseSemver(meta.OpenChamberVersion)
	code, codeOK := parseSemver(meta.OpenCodeVersion)
	if !webOK || !codeOK || web.Core[0] != 2 || code.Core[0] != 2 || CompareSemver(meta.OpenChamberVersion, minWebVersion) < 0 {
		return nil, fmt.Errorf("快照的运行版本不受当前管理器支持")
	}
	if err := checkSnapshotDisk(uint64(meta.SizeBytes) + 100*1024*1024); err != nil {
		return nil, err
	}
	return &meta, nil
}

func queueSnapshotRestore(id string) (*SnapshotMeta, error) {
	meta, err := ValidateSnapshotForRestore(id)
	if err != nil {
		return nil, err
	}
	wasRunning, err := beginSnapshotTask("restore")
	if err != nil {
		return nil, err
	}
	go func() {
		defer maintenanceMu.Unlock()
		err := restoreSnapshot(*meta, wasRunning)
		if cleanupErr := cleanupSnapshotArtifacts(); cleanupErr != nil {
			LogWarning("[快照] 临时文件尚未清理，本轮不再重试；恢复记录未处理完成时仍限制启动和维护: %s", cleanupErr)
		}
		if GetActiveServicePID() > 0 {
			state.SetStatus(StatusRunning, "")
		} else {
			state.SetStatus(StatusStopped, "")
		}
		if wasRunning && appContext.Err() == nil && GetActiveServicePID() == 0 && isRuntimeReady() {
			if startErr := startService(); startErr != nil {
				state.SetStatus(StatusStopped, "快照还原后启动失败: "+startErr.Error())
				if err == nil {
					err = startErr
				}
			}
		}
		finishSnapshotTask("restore", err)
	}()
	return meta, nil
}

// extractSnapshot 先完整校验并解压到隔离目录。
func extractSnapshot(archive, stage string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(&snapshotReader{ctx: appContext, reader: file, onRead: func(int) {}})
	if err != nil {
		return err
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	seen := map[string]bool{}
	var total uint64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if !isSnapshotPath(header.Name) || header.Typeflag != tar.TypeReg || seen[header.Name] || header.Size < 0 {
			return fmt.Errorf("快照包含不允许的路径或文件类型")
		}
		seen[header.Name] = true
		if uint64(header.Size) > ^uint64(0)-total-100*1024*1024 {
			return fmt.Errorf("快照文件总大小无效")
		}
		total += uint64(header.Size)
		if err := checkSnapshotDisk(uint64(header.Size) + 100*1024*1024); err != nil {
			return err
		}
		if err := extractSnapshotFile(reader, filepath.Join(stage, filepath.FromSlash(header.Name))); err != nil {
			return err
		}
	}
	if _, err := io.Copy(io.Discard, compressed); err != nil {
		return fmt.Errorf("压缩包校验失败: %w", err)
	}
	if !seen["config.json"] {
		return fmt.Errorf("快照缺少应用配置")
	}
	return checkSnapshotDisk(total + 100*1024*1024)
}

func extractSnapshotFile(reader io.Reader, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, reader)
	if copyErr == nil {
		copyErr = file.Sync()
	}
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func restoreSnapshot(meta SnapshotMeta, wasRunning bool) error {
	archive := filepath.Join(globalSnapshotsDir, meta.ID, "data.tar.gz")
	digest, err := hashSnapshotArchive(appContext, archive)
	if err != nil {
		return err
	}
	if digest != meta.ArchiveSHA256 {
		return fmt.Errorf("快照归档完整性校验失败")
	}
	stage, err := os.MkdirTemp(globalPkgVar, "snapshot-stage-")
	if err != nil {
		return err
	}
	if err := extractSnapshot(archive, stage); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(stage, "config.json"))
	if err != nil {
		return err
	}
	var restoredConfig Config
	if json.Unmarshal(data, &restoredConfig) != nil {
		return fmt.Errorf("快照中的配置格式无效")
	}
	if err := validateConfig(restoredConfig); err != nil {
		return err
	}
	if restoredConfig.Version != meta.OpenChamberVersion {
		return fmt.Errorf("快照配置与版本信息不一致")
	}
	for _, port := range []int{restoredConfig.GetServerPort(), restoredConfig.GetProxyPort()} {
		currentConfig := GetConfig()
		if port != currentConfig.GetServerPort() && port != currentConfig.GetProxyPort() {
			if err := checkPortAvailable(port); err != nil {
				return fmt.Errorf("快照配置的端口 %d 已被占用，请释放端口后还原", port)
			}
		}
	}
	hasRuntimeChange, err := prepareSnapshotRuntime(meta, stage)
	if err != nil {
		return err
	}
	currentConfig := GetConfig()
	restoredConfig.DataLibraryPath = globalPkgVar
	restoredConfig.BuildTime = currentConfig.BuildTime
	if hasRuntimeChange {
		restoredConfig.BuildTime = time.Now().Format("2006-01-02 15:04")
	}
	restoredConfig.LastRunState = StatusStopped
	if wasRunning {
		restoredConfig.LastRunState = StatusRunning
	}
	if err := writeJSONFile(filepath.Join(stage, "config.json"), restoredConfig); err != nil {
		return err
	}
	if err := appContext.Err(); err != nil {
		return err
	}
	setSnapshotProgress(SnapshotProgress{Active: true, Action: "restore", Percent: 45, Stage: "正在停止服务并隔离现有数据"})
	stopAndWait()
	backup, err := os.MkdirTemp(globalPkgVar, "snapshot-backup-")
	if err != nil {
		return err
	}
	current, _, err := collectSnapshotFiles(globalPkgVar)
	if err != nil {
		return err
	}
	restored, _, err := collectSnapshotFiles(stage)
	if err != nil {
		return err
	}
	for _, name := range restored {
		if err := validateSnapshotParents(globalPkgVar, name); err != nil {
			return err
		}
		info, err := os.Lstat(filepath.Join(globalPkgVar, filepath.FromSlash(name)))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("快照目标存在非普通文件: %s", name)
		}
	}
	journal := snapshotRecovery{Backup: filepath.Base(backup), Current: current, Restored: restored, Runtime: hasRuntimeChange}
	if info, err := os.Lstat(runtimeDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("运行包路径必须是普通目录")
		}
		journal.HadRuntime = true
	} else if !os.IsNotExist(err) {
		return err
	}
	return applySnapshotRestore(stage, journal, restoredConfig)
}

// replaceSnapshotFiles 逐文件切换并保留未归档的源码、缓存与运行包。
func replaceSnapshotFiles(stage, backup string, journal *snapshotRecovery) error {
	for _, name := range journal.Current {
		if err := moveSnapshotFile(globalPkgVar, backup, name); err != nil {
			return err
		}
	}
	journal.Installing = true
	if err := saveSnapshotRecovery(*journal); err != nil {
		return err
	}
	for _, name := range journal.Restored {
		if err := moveSnapshotFile(stage, globalPkgVar, name); err != nil {
			return err
		}
	}
	return nil
}

func moveSnapshotFile(source, target, name string) error {
	destination := filepath.Join(target, filepath.FromSlash(name))
	if err := validateSnapshotParents(source, name); err != nil {
		return err
	}
	if err := validateSnapshotParents(target, name); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	origin := filepath.Join(source, filepath.FromSlash(name))
	if err := os.Rename(origin, destination); err != nil {
		return err
	}
	if err := syncSnapshotDirectory(filepath.Dir(destination)); err != nil {
		return err
	}
	return syncSnapshotDirectory(filepath.Dir(origin))
}

func validateSnapshotParents(base, name string) error {
	current := base
	parts := strings.Split(path.Dir(name), "/")
	for _, part := range parts {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("快照目录包含符号链接或非目录项: %s", current)
		}
	}
	return nil
}

func hashSnapshotArchive(ctx context.Context, archive string) (string, error) {
	file, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	reader := &snapshotReader{ctx: ctx, reader: file, onRead: func(int) {}}
	if _, err := io.Copy(digest, reader); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func DeleteSnapshot(id string) error {
	if !validIDRegex.MatchString(id) {
		return fmt.Errorf("快照 ID 无效")
	}
	if !maintenanceMu.TryLock() {
		return fmt.Errorf("正在执行服务操作或维护任务，请稍候")
	}
	defer maintenanceMu.Unlock()
	if err := checkMaintenanceRecovery(); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(globalSnapshotsDir, id)); err != nil {
		return err
	}
	notifySnapshot()
	return nil
}

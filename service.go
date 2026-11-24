package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

const (
	StatusStopped         = "stopped"
	StatusStarting        = "starting"
	StatusRunning         = "running"
	StatusBuilding        = "building"
	StatusSnapshotting    = "snapshotting"
	serviceStartupTimeout = 5 * time.Minute
)

type ServiceState struct {
	mu            sync.RWMutex
	status        string
	targetVersion string
	startTime     time.Time
	lastMessage   string
	stateSubs     map[chan struct{}]struct{}
}

var state = &ServiceState{status: StatusStopped, stateSubs: make(map[chan struct{}]struct{})}

func (s *ServiceState) SetStatus(status, msg string) {
	s.mu.Lock()
	oldStatus := s.status
	changed := oldStatus != status || s.lastMessage != msg
	if status == StatusRunning && oldStatus != status {
		s.startTime = time.Now()
	}
	s.status, s.lastMessage = status, msg
	if status != StatusBuilding {
		s.targetVersion = ""
	}
	s.mu.Unlock()
	if changed {
		LogInfo("[状态] %s → %s: %s", oldStatus, status, msg)
		s.notify()
	}
}

func (s *ServiceState) SetTargetVersion(version string) {
	s.mu.Lock()
	s.targetVersion = version
	s.mu.Unlock()
	s.notify()
}

func (s *ServiceState) Status() string { s.mu.RLock(); defer s.mu.RUnlock(); return s.status }

func (s *ServiceState) Snapshot() (status, uptime, lastMsg, version, buildTime, targetVersion string, startedAt int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status, lastMsg, targetVersion = s.status, s.lastMessage, s.targetVersion
	version, buildTime = GetVersion(), GetBuildTime()
	if status == StatusRunning && !s.startTime.IsZero() {
		startedAt = s.startTime.Unix()
		uptime = formatDuration(time.Since(s.startTime))
	}
	return
}

func (s *ServiceState) notify() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for ch := range s.stateSubs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *ServiceState) SubscribeState(buf int) (<-chan struct{}, func()) {
	ch := make(chan struct{}, buf)
	s.mu.Lock()
	s.stateSubs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() { s.mu.Lock(); delete(s.stateSubs, ch); s.mu.Unlock() }
}

func (s *ServiceState) Poke() { s.notify() }

func isRuntimeReady() bool { return validateRuntime(runtimeDir) == nil }

func InitService() {
	StartUsageSampler()
	maintenanceMu.Lock()
	deploymentActive.Store(true)
	go func() {
		defer maintenanceMu.Unlock()
		defer func() { deploymentActive.Store(false); state.Poke() }()
		if !recoverDeployment() {
			return
		}
		refreshVersion()
		state.SetStatus(StatusStopped, "")
		if !isRuntimeReady() {
			LogInfo("[部署] OpenChamber 尚未部署，请选择发布版本在线部署")
			return
		}
		if GetLastRunState() == StatusRunning {
			if err := startService(); err != nil {
				state.SetStatus(StatusStopped, "启动失败: "+err.Error())
			}
		}
	}()
}

func Start() error {
	if !maintenanceMu.TryLock() {
		return fmt.Errorf("正在执行服务操作或维护任务，请稍候")
	}
	defer maintenanceMu.Unlock()
	return startService()
}

func startService() error {
	if appContext.Err() != nil {
		return fmt.Errorf("应用正在停止")
	}
	if err := checkMaintenanceRecovery(); err != nil {
		return err
	}
	return startPreparedService()
}

func startPreparedService() error {
	if err := appContext.Err(); err != nil {
		return err
	}
	procMu.Lock()
	defer procMu.Unlock()
	switch state.Status() {
	case StatusBuilding, StatusSnapshotting:
		return fmt.Errorf("正在执行维护任务，请稍候再试")
	case StatusStarting:
		return fmt.Errorf("服务正在启动中")
	case StatusRunning:
		return fmt.Errorf("服务已在运行中")
	}
	launch, err := inspectRuntime(runtimeDir)
	if err != nil {
		return fmt.Errorf("运行环境未就绪或核心依赖缺失: %w", err)
	}
	if err := checkRuntimePrerequisites(launch.nodeEngine); err != nil {
		return err
	}
	return startLocked(launch.cliPath)
}

func startLocked(cli string) error {
	cleanupServiceLocked()
	cfg := GetConfig()
	cmd := exec.Command(nodeBin(), cli, "serve", "--foreground", "--host", "127.0.0.1", "--port", strconv.Itoa(cfg.GetServerPort()))
	cmd.Dir = globalHomeDir
	output := NewLogWriter()
	cmd.Stdout, cmd.Stderr = output, output
	cmd.Env = runtimeEnv()
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		output.Flush()
		state.SetStatus(StatusStopped, "启动失败: "+err.Error())
		return err
	}
	mp := &managedProcess{cmd: cmd, done: make(chan struct{})}
	process = mp
	if err := os.WriteFile(pidFilePath(), []byte(strconv.Itoa(mp.Pid())), 0600); err != nil {
		mp.stopRequested = true
		killProcessTree(mp.Pid())
		_ = cmd.Wait()
		output.Flush()
		process = nil
		return fmt.Errorf("写入服务 PID 失败: %w", err)
	}
	state.SetStatus(StatusStarting, fmt.Sprintf("主进程已启动 (PID=%d)，等待 OpenChamber 与 OpenCode 就绪", mp.Pid()))
	go waitAndActivateReverseProxy(mp, cfg.GetServerPort())
	go waitServiceExit(mp, output)
	return nil
}

func waitServiceExit(mp *managedProcess, output *LineLogWriter) {
	err := mp.cmd.Wait()
	output.Flush()
	procMu.Lock()
	defer procMu.Unlock()
	close(mp.done)
	if process != mp {
		return
	}
	process = nil
	removePidFileIfMatches(mp.Pid())
	stopReverseProxy()
	if mp.stopRequested {
		return
	}
	cleanupServiceProcesses()
	if err != nil {
		LogWarning("[进程] OpenChamber 退出: %s", err)
	}
	if status := state.Status(); status != StatusBuilding && status != StatusSnapshotting {
		state.SetStatus(StatusStopped, "OpenChamber 服务进程已退出，请查看运行日志")
	}
}

func stopAndWait() {
	procMu.Lock()
	mp := process
	if mp != nil {
		mp.stopRequested = true
	}
	procMu.Unlock()
	stopReverseProxy()
	if mp == nil {
		cleanupServiceProcesses()
		if state.Status() != StatusBuilding && state.Status() != StatusSnapshotting {
			state.SetStatus(StatusStopped, "")
		}
		return
	}
	// 先让官方服务关闭 OpenCode 及其独立进程组，再清理宿主进程组。
	_ = signalService(mp.Pid())
	select {
	case <-mp.done:
	case <-time.After(15 * time.Second):
		killProcessTree(mp.Pid())
	}
	_ = killProcessGroup(mp.Pid())
	cleanupServiceProcesses()
	procMu.Lock()
	if process == mp {
		process = nil
	}
	removePidFileIfMatches(mp.Pid())
	procMu.Unlock()
	if state.Status() != StatusBuilding && state.Status() != StatusSnapshotting {
		state.SetStatus(StatusStopped, "")
	}
}

func Stop() error {
	if !maintenanceMu.TryLock() {
		return fmt.Errorf("正在执行服务操作或维护任务，请稍候")
	}
	defer maintenanceMu.Unlock()
	stopAndWait()
	SetLastRunState(StatusStopped)
	return nil
}

func Restart() error {
	if !maintenanceMu.TryLock() {
		return fmt.Errorf("正在执行服务操作或维护任务，请稍候")
	}
	defer maintenanceMu.Unlock()
	stopAndWait()
	return startService()
}

func formatDuration(d time.Duration) string {
	total := int(d.Round(time.Second).Seconds())
	h, m, s := total/3600, total/60%60, total%60
	if h > 0 {
		return fmt.Sprintf("%d小时%d分%d秒", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%d分%d秒", m, s)
	}
	return fmt.Sprintf("%d秒", s)
}

func waitAndActivateReverseProxy(mp *managedProcess, port int) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	timeout := time.NewTimer(serviceStartupTimeout)
	defer timeout.Stop()
	for {
		select {
		case <-mp.done:
			return
		case <-appContext.Done():
			return
		case <-timeout.C:
			if deploymentActive.Load() {
				return
			}
			maintenanceMu.Lock()
			procMu.Lock()
			isCurrent := process == mp && !mp.stopRequested
			procMu.Unlock()
			if isCurrent {
				stopAndWait()
				state.SetStatus(StatusStopped, "服务就绪探测超时，请查看 OpenChamber 与 OpenCode 运行日志")
			}
			maintenanceMu.Unlock()
			return
		case <-ticker.C:
			if !isServiceHealthy(port) {
				continue
			}
			procMu.Lock()
			if process == mp && !mp.stopRequested {
				if err := startReverseProxy(); err != nil {
					LogWarning("[代理] 独立端口启动失败: %s", err)
					state.SetStatus(StatusRunning, "独立代理端口不可用: "+err.Error())
				} else {
					state.SetStatus(StatusRunning, "")
				}
				SetLastRunState(StatusRunning)
			}
			procMu.Unlock()
			return
		}
	}
}

func isServiceHealthy(port int) bool {
	client := &http.Client{Transport: localTransport, Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var health struct {
		Status          string `json:"status"`
		Version         string `json:"openchamberVersion"`
		OpenCodeRunning bool   `json:"openCodeRunning"`
		IsOpenCodeReady bool   `json:"isOpenCodeReady"`
	}
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&health) == nil && health.Status == "ok" && health.Version == readVersion() && health.OpenCodeRunning && health.IsOpenCodeReady
}

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type managedProcess struct {
	cmd           *exec.Cmd
	stopRequested bool
	done          chan struct{}
}

func (mp *managedProcess) Pid() int {
	if mp == nil || mp.cmd == nil || mp.cmd.Process == nil {
		return 0
	}
	return mp.cmd.Process.Pid
}

var (
	procMu        sync.Mutex
	process       *managedProcess
	maintenanceMu sync.Mutex
)

func pidFilePath() string { return filepath.Join(globalPkgVar, "service.pid") }

func removePidFileIfMatches(pid int) {
	if data, err := os.ReadFile(pidFilePath()); err == nil && strings.TrimSpace(string(data)) == strconv.Itoa(pid) {
		if err := os.Remove(pidFilePath()); err != nil && !os.IsNotExist(err) {
			LogWarning("[进程] 删除 PID 文件失败: %s", err)
		}
	}
}

func setProcessGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func signalService(pid int) error   { return syscall.Kill(pid, syscall.SIGTERM) }

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func killProcessGroup(pgid int) bool {
	if pgid <= 1 || pgid == syscall.Getpgrp() {
		return false
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err == syscall.ESRCH {
		return true
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pgid, 0) == syscall.ESRCH {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		LogWarning("[进程] 无法终止进程组 %d: %s", pgid, err)
	}
	return syscall.Kill(-pgid, 0) == syscall.ESRCH
}

func killProcessTree(pid int) {
	if pid <= 1 {
		return
	}
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid {
		killProcessGroup(pgid)
		return
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		LogWarning("[进程] 无法终止进程 %d: %s", pid, err)
	}
}

func isRuntimePath(path string) bool {
	if runtimeDir == "" {
		return false
	}
	clean := filepath.Clean(path)
	if strings.HasPrefix(clean, filepath.Clean(runtimeDir)+string(os.PathSeparator)) {
		return true
	}
	relative, err := filepath.Rel(globalPkgVar, clean)
	if err != nil {
		return false
	}
	root := strings.Split(relative, string(os.PathSeparator))[0]
	return root == "runtime-previous" || strings.HasPrefix(root, "runtime-stage-") ||
		strings.HasPrefix(root, "snapshot-stage-") || strings.HasPrefix(root, "snapshot-backup-")
}

// isServiceProcess 仅识别本应用私有运行包中的进程。
func isServiceProcess(pid int) bool {
	return matchesProcessPath(pid, isRuntimePath)
}

func matchesProcessPath(pid int, matches func(string) bool) bool {
	if pid <= 1 {
		return false
	}
	if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil && matches(exe) {
		return true
	}
	if cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid)); err == nil && matches(cwd) {
		return true
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	for _, arg := range strings.Split(string(data), "\x00") {
		if filepath.IsAbs(arg) && matches(arg) {
			return true
		}
	}
	return false
}

func cleanupServiceProcesses() {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		LogWarning("[进程] 无法读取进程列表: %s", err)
		return
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err == nil && isServiceProcess(pid) {
			killProcessTree(pid)
		}
	}
}

func cleanupServiceLocked() {
	cleanupServiceProcesses()
	if err := os.Remove(pidFilePath()); err != nil && !os.IsNotExist(err) {
		LogWarning("[进程] 清理 PID 文件失败: %s", err)
	}
}

func GetActiveServicePID() int {
	procMu.Lock()
	defer procMu.Unlock()
	if process != nil && isProcessAlive(process.Pid()) {
		return process.Pid()
	}
	return 0
}

type procSample struct{ ticks, total uint64 }
type UsageStats struct {
	Cpu    string `json:"cpu"`
	Memory string `json:"memory"`
}

var (
	usageCacheMu            sync.RWMutex
	cachedCPU, cachedMemory = "-", "-"
	usageSubMu              sync.Mutex
	usageSubs               = make(map[chan UsageStats]struct{})
	wakeUsageCh             = make(chan struct{}, 1)
	lastSample              procSample
	lastSamplePID           int
)

func GetCachedServiceUsage() (string, string) {
	usageCacheMu.RLock()
	defer usageCacheMu.RUnlock()
	return cachedCPU, cachedMemory
}

func SubscribeUsage(buf int) (<-chan UsageStats, func()) {
	ch := make(chan UsageStats, buf)
	usageSubMu.Lock()
	usageSubs[ch] = struct{}{}
	usageSubMu.Unlock()
	select {
	case wakeUsageCh <- struct{}{}:
	default:
	}
	return ch, func() { usageSubMu.Lock(); delete(usageSubs, ch); usageSubMu.Unlock() }
}

func StartUsageSampler() {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-wakeUsageCh:
			}
			usageSubMu.Lock()
			hasSubscribers := len(usageSubs) > 0
			usageSubMu.Unlock()
			if !hasSubscribers {
				continue
			}
			cpu, memory := calculateServiceUsage(GetActiveServicePID())
			usageCacheMu.Lock()
			cachedCPU, cachedMemory = cpu, memory
			usageCacheMu.Unlock()
			usageSubMu.Lock()
			for ch := range usageSubs {
				select {
				case ch <- UsageStats{Cpu: cpu, Memory: memory}:
				default:
				}
			}
			usageSubMu.Unlock()
		}
	}()
}

func readProcRSS(pid int) uint64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "VmRSS:" {
			value, _ := strconv.ParseUint(fields[1], 10, 64)
			return value
		}
	}
	return 0
}

func readSystemTotalJiffies() uint64 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "cpu" {
			continue
		}
		var total uint64
		// guest 与 guest_nice 已计入 user 与 nice。
		for i := 1; i < len(fields) && i <= 8; i++ {
			value, _ := strconv.ParseUint(fields[i], 10, 64)
			total += value
		}
		return total
	}
	return 0
}

func readProcTicks(pid int) uint64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	end := strings.LastIndex(string(data), ")")
	if end < 0 {
		return 0
	}
	fields := strings.Fields(string(data)[end+1:])
	if len(fields) < 13 {
		return 0
	}
	user, _ := strconv.ParseUint(fields[11], 10, 64)
	system, _ := strconv.ParseUint(fields[12], 10, 64)
	return user + system
}

func getFamilyPids(rootPID int) []int {
	if rootPID <= 0 {
		return nil
	}
	pids := []int{rootPID}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", rootPID, rootPID))
	if err != nil {
		return pids
	}
	for _, field := range strings.Fields(string(data)) {
		if child, err := strconv.Atoi(field); err == nil && child > 0 {
			pids = append(pids, getFamilyPids(child)...)
		}
	}
	return pids
}

func calculateServiceUsage(pid int) (string, string) {
	if pid <= 0 {
		lastSamplePID = 0
		return "-", "-"
	}
	var ticks, rss uint64
	for _, child := range getFamilyPids(pid) {
		ticks += readProcTicks(child)
		rss += readProcRSS(child)
	}
	total := readSystemTotalJiffies()
	memory := fmt.Sprintf("%.1f MB", float64(rss)/1024)
	if rss >= 1024*1024 {
		memory = fmt.Sprintf("%.1f GB", float64(rss)/(1024*1024))
	}
	cpu := 0.0
	if pid == lastSamplePID && total > lastSample.total && ticks >= lastSample.ticks {
		cpu = float64(ticks-lastSample.ticks) * 100 / float64(total-lastSample.total)
	}
	if cpu > 100 {
		cpu = 100
	}
	lastSamplePID, lastSample = pid, procSample{ticks: ticks, total: total}
	return fmt.Sprintf("%.1f%%", cpu), memory
}

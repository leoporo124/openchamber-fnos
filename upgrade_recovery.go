package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type deploymentJournal struct {
	Stage           string `json:"stage"`
	Phase           string `json:"phase"`
	HadRuntime      bool   `json:"had_runtime"`
	WasRunning      bool   `json:"was_running"`
	PreviousVersion string `json:"previous_version"`
	PreviousCode    string `json:"previous_code"`
	PreviousBuild   string `json:"previous_build"`
}

func deploymentJournalPath() string { return filepath.Join(globalPkgVar, "runtime-update.json") }

func checkMaintenanceRecovery() error {
	if err := checkSnapshotRecovery(); err != nil {
		return err
	}
	if _, err := os.Lstat(deploymentJournalPath()); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("更新恢复记录尚未处理完成，请重新启动应用重试恢复")
}

func prepareDeployment() (deploymentJournal, error) {
	info, statErr := os.Lstat(runtimeDir)
	if statErr != nil && !os.IsNotExist(statErr) {
		return deploymentJournal{}, statErr
	}
	if statErr == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return deploymentJournal{}, fmt.Errorf("运行包路径必须是普通目录")
	}
	stage, err := os.MkdirTemp(globalPkgVar, "runtime-stage-")
	if err != nil {
		return deploymentJournal{}, err
	}
	journal := deploymentJournal{
		Stage: filepath.Base(stage), Phase: "preparing", HadRuntime: statErr == nil,
		WasRunning: GetActiveServicePID() > 0, PreviousVersion: currentRuntimeVersion(),
		PreviousCode: readOpenCodeVersion(), PreviousBuild: GetBuildTime(),
	}
	if err := saveDeploymentJournal(journal); err != nil {
		if cleanupErr := cleanupDeploymentArtifacts(); cleanupErr != nil {
			return journal, fmt.Errorf("创建更新记录失败: %v；清理未完成: %w", err, cleanupErr)
		}
		return journal, err
	}
	return journal, nil
}

func saveDeploymentJournal(journal deploymentJournal) error {
	return writeJSONFile(deploymentJournalPath(), journal)
}

func readDeploymentJournal() (deploymentJournal, error) {
	data, err := os.ReadFile(deploymentJournalPath())
	if err != nil {
		return deploymentJournal{}, err
	}
	var journal deploymentJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return journal, err
	}
	if filepath.Base(journal.Stage) != journal.Stage || !strings.HasPrefix(journal.Stage, "runtime-stage-") {
		return journal, fmt.Errorf("更新记录的暂存目录无效")
	}
	switch journal.Phase {
	case "preparing", "switching", "committed", "restored":
		return journal, nil
	default:
		return journal, fmt.Errorf("更新记录的阶段无效")
	}
}

func syncDeploymentDirectories(stage string) error {
	if err := syncSnapshotDirectory(stage); err != nil {
		return err
	}
	return syncSnapshotDirectory(globalPkgVar)
}

// recoverRuntimeSwitch 根据持久记录完成回滚或提交，再清理安装残留。
func recoverRuntimeSwitch() error {
	journal, err := readDeploymentJournal()
	if os.IsNotExist(err) {
		return recoverLegacyRuntime()
	}
	if err != nil {
		return err
	}
	if err := syncSnapshotDirectory(globalPkgVar); err != nil {
		return err
	}
	if journal.Phase == "switching" {
		if err := cleanupDeploymentProcesses(); err != nil {
			return err
		}
		if err := restoreDeployment(journal); err != nil {
			return err
		}
		journal.Phase = "restored"
		if err := saveDeploymentJournal(journal); err != nil {
			return err
		}
	}
	if journal.Phase == "committed" {
		if err := validateRuntime(runtimeDir); err != nil {
			return fmt.Errorf("已提交的运行包无法校验，保留更新记录: %w", err)
		}
	}
	if journal.Phase == "committed" || journal.Phase == "restored" {
		if err := persistDeploymentConfig(journal); err != nil {
			return err
		}
	}
	if err := removeRecoveryRecord(deploymentJournalPath(), journal); err != nil {
		return err
	}
	cleanupDeploymentRemainder()
	return nil
}

func persistDeploymentConfig(journal deploymentJournal) error {
	configMu.Lock()
	defer configMu.Unlock()
	cfg := globalConfig
	cfg.Version, cfg.LastRunState = readVersion(), StatusRunning
	if journal.Phase == "restored" {
		cfg.Version, cfg.BuildTime = journal.PreviousVersion, journal.PreviousBuild
		cfg.LastRunState = StatusStopped
		if journal.WasRunning {
			cfg.LastRunState = StatusRunning
		}
	}
	if err := persistConfigLocked(cfg); err != nil {
		return err
	}
	globalConfig = cfg
	return nil
}

func restoreDeployment(journal deploymentJournal) error {
	stage := filepath.Join(globalPkgVar, journal.Stage)
	backup := filepath.Join(stage, "previous")
	if info, err := os.Lstat(backup); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("原运行包备份路径无效，保留恢复记录")
		}
		if err := os.RemoveAll(runtimeDir); err != nil {
			return err
		}
		if err := os.Rename(backup, runtimeDir); err != nil {
			return err
		}
		return syncDeploymentDirectories(stage)
	} else if !os.IsNotExist(err) {
		return err
	}
	if !journal.HadRuntime {
		if err := os.RemoveAll(runtimeDir); err != nil {
			return err
		}
		return syncSnapshotDirectory(globalPkgVar)
	}
	if readVersion() != journal.PreviousVersion || readOpenCodeVersion() != journal.PreviousCode {
		return fmt.Errorf("原运行包未找到或版本不匹配，保留恢复记录")
	}
	return syncDeploymentDirectories(stage)
}

func recoverLegacyRuntime() error {
	backup := runtimeDir + "-previous"
	if _, err := os.Lstat(backup); err == nil {
		if _, err := os.Lstat(runtimeDir); os.IsNotExist(err) {
			if err := validateRuntime(backup); err != nil {
				return err
			}
			if err := os.Rename(backup, runtimeDir); err != nil {
				return err
			}
			if err := syncSnapshotDirectory(globalPkgVar); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if err := validateRuntime(runtimeDir); err != nil {
			return fmt.Errorf("旧版更新遗留的运行包无法确认，保留备份: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	cleanupDeploymentRemainder()
	return nil
}

func cleanupDeploymentRemainder() {
	if err := cleanupDeploymentArtifacts(); err != nil {
		LogWarning("[更新] 运行包恢复已完成，安装残留将在下次维护或启动时重试清理: %s", err)
	}
}

// cleanupDeploymentArtifacts 只删除本应用约定的安装目录及其中的临时缓存。
func cleanupDeploymentArtifacts() error {
	if err := cleanupDeploymentProcesses(); err != nil {
		return err
	}
	entries, err := os.ReadDir(globalPkgVar)
	if err != nil {
		return err
	}
	paths := []string{deploymentJournalPath() + ".tmp"}
	for _, entry := range entries {
		if entry.Name() == "runtime-previous" || strings.HasPrefix(entry.Name(), "runtime-stage-") {
			paths = append(paths, filepath.Join(globalPkgVar, entry.Name()))
		}
	}
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("清理 %s 失败: %w", filepath.Base(path), err)
		}
	}
	return syncSnapshotDirectory(globalPkgVar)
}

func isDeploymentPath(path string) bool {
	relative, err := filepath.Rel(globalPkgVar, filepath.Clean(path))
	if err != nil {
		return false
	}
	root := strings.Split(relative, string(os.PathSeparator))[0]
	return root == "runtime-previous" || strings.HasPrefix(root, "runtime-stage-")
}

func cleanupDeploymentProcesses() error {
	return cleanupMatchingProcesses(isDeploymentPath)
}

func cleanupMatchingProcesses(matches func(string) bool) error {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() || !matchesProcessPath(pid, matches) {
			continue
		}
		killProcessTree(pid)
		if matchesProcessPath(pid, matches) {
			return fmt.Errorf("安装进程 %d 尚未退出", pid)
		}
	}
	return nil
}

func recoverDeployment() bool {
	if err := recoverRuntimeSwitch(); err != nil {
		status := StatusStopped
		if GetActiveServicePID() > 0 {
			status = StatusRunning
		}
		state.SetStatus(status, "更新恢复未完成，请保留恢复记录与备份，查看日志并重新启动应用: "+err.Error())
		LogError("[更新] 恢复未完成: %s", err)
		return false
	}
	return true
}

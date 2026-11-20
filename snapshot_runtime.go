package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// prepareSnapshotRuntime 仅为快照还原准备对应精确版本，不放开普通部署降级。
func prepareSnapshotRuntime(meta SnapshotMeta, stage string) (bool, error) {
	if isRuntimeReady() && readVersion() == meta.OpenChamberVersion && readOpenCodeVersion() == meta.OpenCodeVersion {
		return false, nil
	}
	setSnapshotProgress(SnapshotProgress{Active: true, Action: "restore", Percent: 25, Stage: "正在联网准备快照对应的精确运行版本"})
	cfg := GetConfig()
	release, err := resolveRuntimeRelease(cfg, meta.OpenChamberVersion)
	if err != nil {
		return false, err
	}
	if release.CodeVersion != meta.OpenCodeVersion {
		return false, fmt.Errorf("快照的 OpenCode 版本与官方发布依赖不一致")
	}
	if err := checkRuntimePrerequisites(release.NodeEngine); err != nil {
		return false, err
	}
	if err := installRuntime(filepath.Join(stage, "runtime-install"), release, cfg); err != nil {
		return false, err
	}
	return true, cleanupMatchingProcesses(isSnapshotInstallPath)
}

func isSnapshotInstallPath(path string) bool {
	relative, err := filepath.Rel(globalPkgVar, filepath.Clean(path))
	return err == nil && strings.HasPrefix(strings.Split(relative, string(os.PathSeparator))[0], "snapshot-stage-")
}

func switchSnapshotRuntime(stage, backup string, journal snapshotRecovery) error {
	if !journal.Runtime {
		return nil
	}
	if journal.HadRuntime {
		if err := os.Rename(runtimeDir, filepath.Join(backup, "runtime")); err != nil {
			return err
		}
		if err := syncDeploymentDirectories(backup); err != nil {
			return err
		}
	}
	payload := filepath.Join(stage, "runtime-install", "payload")
	if err := os.Rename(payload, runtimeDir); err != nil {
		return err
	}
	return syncDeploymentDirectories(filepath.Dir(payload))
}

func rollbackSnapshotRuntime(journal snapshotRecovery, backup string) error {
	previous := filepath.Join(backup, "runtime")
	info, err := os.Lstat(previous)
	if os.IsNotExist(err) && journal.HadRuntime {
		return syncDeploymentDirectories(backup)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("快照还原的运行包备份目录无效")
	}
	if err := os.RemoveAll(runtimeDir); err != nil {
		return err
	}
	if journal.HadRuntime {
		if err := os.Rename(previous, runtimeDir); err != nil {
			return err
		}
	}
	return syncDeploymentDirectories(backup)
}

func applySnapshotRestore(stage string, journal snapshotRecovery, cfg Config) (resultErr error) {
	previousConfig := GetConfig()
	backup := filepath.Join(globalPkgVar, journal.Backup)
	if err := saveSnapshotRecovery(journal); err != nil {
		return err
	}
	defer func() {
		if resultErr == nil || journal.Committed {
			return
		}
		stopAndWait()
		if err := rollbackSnapshotJournal(journal, backup); err != nil {
			resultErr = fmt.Errorf("还原失败: %v；数据与运行包回滚未完成，请重启应用重试: %w", resultErr, err)
			return
		}
		configMu.Lock()
		globalConfig = previousConfig
		configMu.Unlock()
		ApplyNpmRegistryEnv()
		if err := removeRecoveryRecord(snapshotRecoveryPath(), journal); err != nil {
			resultErr = fmt.Errorf("原数据与运行包已恢复，恢复记录清理失败: %w", err)
		}
	}()
	if err := switchSnapshotRuntime(stage, backup, journal); err != nil {
		return err
	}
	if err := replaceSnapshotFiles(stage, backup, &journal); err != nil {
		return err
	}
	configMu.Lock()
	globalConfig = cfg
	configMu.Unlock()
	ApplyNpmRegistryEnv()
	state.SetStatus(StatusStopped, "")
	if cfg.LastRunState == StatusRunning {
		if err := startPreparedService(); err != nil {
			return err
		}
		if err := waitDeploymentReady(); err != nil {
			return err
		}
	}
	if err := appContext.Err(); err != nil {
		return err
	}
	return commitSnapshotRestore(&journal)
}

func commitSnapshotRestore(journal *snapshotRecovery) error {
	journal.Committed = true
	if err := saveSnapshotRecovery(*journal); err != nil {
		var saved snapshotRecovery
		data, readErr := os.ReadFile(snapshotRecoveryPath())
		if readErr != nil || json.Unmarshal(data, &saved) != nil || !saved.Committed {
			journal.Committed = false
			return err
		}
		if err := syncSnapshotDirectory(globalPkgVar); err != nil {
			return fmt.Errorf("快照已还原，提交记录尚未同步，请重启应用完成清理: %w", err)
		}
	}
	if err := removeRecoveryRecord(snapshotRecoveryPath(), *journal); err != nil {
		return fmt.Errorf("快照已还原，恢复记录清理失败，请重启应用完成清理: %w", err)
	}
	LogInfo("[快照] 数据与对应运行版本已还原")
	return nil
}

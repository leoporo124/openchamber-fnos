package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

var recoveryRecordWriteFailed atomic.Bool

type snapshotRecovery struct {
	Backup     string   `json:"backup"`
	Current    []string `json:"current"`
	Restored   []string `json:"restored"`
	Installing bool     `json:"installing"`
	Runtime    bool     `json:"runtime,omitempty"`
	HadRuntime bool     `json:"had_runtime,omitempty"`
	Committed  bool     `json:"committed,omitempty"`
}

func snapshotRecoveryPath() string { return filepath.Join(globalPkgVar, "snapshot-recovery.json") }

func checkSnapshotRecovery() error {
	if recoveryRecordWriteFailed.Load() {
		return fmt.Errorf("恢复记录写回失败，请保留备份，检查存储状态后在飞牛应用中心重启应用")
	}
	if _, err := os.Lstat(snapshotRecoveryPath()); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("快照恢复记录尚未处理完成，请重新启动应用完成恢复或清理")
}

func saveSnapshotRecovery(journal snapshotRecovery) error {
	return writeJSONFile(snapshotRecoveryPath(), journal)
}

func syncSnapshotDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// removeRecoveryRecord 确认记录删除已持久化；同步失败时重写记录并保留备份。
func removeRecoveryRecord(recordPath string, journal any) error {
	if err := os.Remove(recordPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := syncSnapshotDirectory(filepath.Dir(recordPath)); err != nil {
		if saveErr := writeJSONFile(recordPath, journal); saveErr != nil {
			recoveryRecordWriteFailed.Store(true)
			return fmt.Errorf("删除恢复记录后目录同步失败: %v；重写记录失败，已阻止后续启动和维护: %w", err, saveErr)
		}
		return fmt.Errorf("删除恢复记录后目录同步失败，已重写恢复记录: %w", err)
	}
	return nil
}

func cleanupSnapshotArtifacts() error {
	if err := checkSnapshotRecovery(); err != nil {
		return err
	}
	if err := cleanupMatchingProcesses(isSnapshotInstallPath); err != nil {
		return err
	}
	entries, err := os.ReadDir(globalPkgVar)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "snapshot-stage-") || strings.HasPrefix(name, "snapshot-backup-") || name == "snapshot-recovery.json.tmp" {
			if err := os.RemoveAll(filepath.Join(globalPkgVar, name)); err != nil {
				return fmt.Errorf("清理快照临时文件 %s 失败: %w", name, err)
			}
		}
	}
	if err := cleanupIncompleteSnapshots(); err != nil {
		return err
	}
	return syncSnapshotDirectory(globalPkgVar)
}

func cleanupIncompleteSnapshots() error {
	entries, err := os.ReadDir(globalSnapshotsDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		isIncomplete := strings.HasPrefix(name, ".creating-") && validIDRegex.MatchString(strings.TrimPrefix(name, ".creating-"))
		if validIDRegex.MatchString(name) {
			_, err := os.Lstat(filepath.Join(globalSnapshotsDir, name, "meta.json"))
			isIncomplete = os.IsNotExist(err)
			if err != nil && !isIncomplete {
				return err
			}
		}
		if isIncomplete {
			if err := os.RemoveAll(filepath.Join(globalSnapshotsDir, name)); err != nil {
				return fmt.Errorf("清理未完成快照 %s 失败: %w", name, err)
			}
		}
	}
	return syncSnapshotDirectory(globalSnapshotsDir)
}

// recoverSnapshotRestore 将未完成的逐文件恢复回滚到操作前的数据。
func recoverSnapshotRestore() error {
	data, err := os.ReadFile(snapshotRecoveryPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var journal snapshotRecovery
	if err := json.Unmarshal(data, &journal); err != nil {
		return err
	}
	if !strings.HasPrefix(journal.Backup, "snapshot-backup-") || filepath.Base(journal.Backup) != journal.Backup {
		return fmt.Errorf("快照恢复记录的备份目录无效")
	}
	for _, names := range [][]string{journal.Current, journal.Restored} {
		for _, name := range names {
			if !isSnapshotPath(name) {
				return fmt.Errorf("快照恢复记录的文件路径无效")
			}
		}
	}
	backup := filepath.Join(globalPkgVar, journal.Backup)
	info, err := os.Lstat(backup)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("快照恢复备份目录无效")
	}
	if err := syncSnapshotDirectory(globalPkgVar); err != nil {
		return err
	}
	if !journal.Committed {
		if err := rollbackSnapshotJournal(journal, backup); err != nil {
			return err
		}
	}
	if err := removeRecoveryRecord(snapshotRecoveryPath(), journal); err != nil {
		return err
	}
	if err := os.RemoveAll(backup); err != nil {
		LogWarning("[快照] 清理已回滚备份失败: %s", err)
	}
	if journal.Committed {
		LogInfo("[快照] 已完成还原任务的中断清理")
	} else {
		LogInfo("[快照] 已恢复中断前的数据与运行包")
	}
	return nil
}

func rollbackSnapshotJournal(journal snapshotRecovery, backup string) error {
	if journal.Runtime {
		if err := rollbackSnapshotRuntime(journal, backup); err != nil {
			return err
		}
	}
	current := make(map[string]bool, len(journal.Current))
	for _, name := range journal.Current {
		current[name] = true
	}
	if journal.Installing {
		installed, _, err := collectSnapshotFiles(globalPkgVar)
		if err != nil {
			return err
		}
		for _, name := range installed {
			if current[name] {
				continue
			}
			if err := validateSnapshotParents(globalPkgVar, name); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(globalPkgVar, filepath.FromSlash(name))); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := syncSnapshotDirectory(filepath.Dir(filepath.Join(globalPkgVar, filepath.FromSlash(name)))); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	for _, name := range journal.Current {
		if _, err := os.Lstat(filepath.Join(backup, filepath.FromSlash(name))); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := moveSnapshotFile(backup, globalPkgVar, name); err != nil {
			return err
		}
	}
	return nil
}

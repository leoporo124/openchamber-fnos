package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

const (
	webPackageName      = "@openchamber/web"
	openCodePackageName = "@opencode/cli"
	nodeBinDir          = "/var/apps/nodejs_v24/target/bin"
	minNodeVersion      = "24.14.0"
)

var deploymentActive atomic.Bool

func nodeBin() string { return filepath.Join(nodeBinDir, "node") }
func npmBin() string  { return filepath.Join(nodeBinDir, "npm") }
func opencodeBin() string {
	return filepath.Join(runtimeDir, "node_modules", "@opencode", "cli", "bin", "opencode.exe")
}

type RuntimeVersionsResult struct {
	CurrentVersion     string   `json:"current_version"`
	Versions           []string `json:"versions"`
	RecommendedVersion string   `json:"recommended_version"`
	MinVersion         string   `json:"min_version"`
}

type CheckUpdateResult struct {
	HasUpdate       bool   `json:"has_update"`
	CurrentVersion  string `json:"current_version"`
	RemoteVersion   string `json:"remote_version"`
	OpenCodeVersion string `json:"opencode_version,omitempty"`
	Channel         string `json:"channel"`
	Message         string `json:"message"`
}

func checkRuntimePrerequisites(nodeRequirement string) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("仅支持 fnOS Linux x86_64 原生运行环境")
	}
	version, err := readNodeVersion()
	if err != nil {
		return err
	}
	if parsed, ok := parseSemver(version); !ok || parsed.Core[0] != 24 || CompareSemver(version, minNodeVersion) < 0 {
		return fmt.Errorf("需要 Node.js 24 且版本至少 %s，当前为 %s", minNodeVersion, version)
	}
	if !matchesVersionRequirement(version, nodeRequirement) {
		return fmt.Errorf("运行包要求 Node.js %s，当前为 %s", nodeRequirement, version)
	}
	if _, err := os.Stat(npmBin()); err != nil {
		return fmt.Errorf("飞牛 nodejs_v24 缺少 npm: %w", err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("缺少 Git，无法使用官方项目与 Git 功能")
	}
	return nil
}

func readNodeVersion() (string, error) {
	ctx, cancel := context.WithTimeout(appContext, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, nodeBin(), "--version").Output()
	if err != nil {
		return "", fmt.Errorf("飞牛 nodejs_v24 不可用: %w", err)
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "v"), nil
}

func readInstalledPackage(dir, scope, name string) (npmPackage, error) {
	data, err := os.ReadFile(filepath.Join(dir, "node_modules", scope, name, "package.json"))
	if err != nil {
		return npmPackage{}, err
	}
	var pkg npmPackage
	err = json.Unmarshal(data, &pkg)
	return pkg, err
}

func readPackageVersion(dir, scope, name string) string {
	pkg, _ := readInstalledPackage(dir, scope, name)
	return pkg.Version
}

func readVersion() string         { return readPackageVersion(runtimeDir, "@openchamber", "web") }
func readOpenCodeVersion() string { return readPackageVersion(runtimeDir, "@opencode", "cli") }
func refreshVersion() {
	if version := readVersion(); version != "" {
		if _, ok := parseSemver(version); ok {
			SetVersion(version)
		}
	}
}

func currentRuntimeVersion() string {
	if version := readVersion(); version != "" {
		if _, ok := parseSemver(version); ok {
			return version
		}
	}
	return GetVersion()
}

type runtimeLaunch struct {
	cliPath    string
	nodeEngine string
}

func validateRuntime(dir string) error {
	_, err := inspectRuntime(dir)
	return err
}

func inspectRuntime(dir string) (runtimeLaunch, error) {
	pkg, err := readInstalledPackage(dir, "@openchamber", "web")
	if err != nil || pkg.Name != webPackageName {
		return runtimeLaunch{}, fmt.Errorf("OpenChamber 运行包缺失或格式无效")
	}
	release, err := releaseFromPackage(pkg)
	if err != nil {
		return runtimeLaunch{}, err
	}
	code, err := readInstalledPackage(dir, "@opencode", "cli")
	if err != nil || code.Name != openCodePackageName || code.Version != release.CodeVersion {
		return runtimeLaunch{}, fmt.Errorf("运行包的 OpenCode CLI 与 client/schema 版本不一致")
	}

	var bins map[string]string
	if err := json.Unmarshal(pkg.Bin, &bins); err != nil || bins["openchamber"] == "" {
		return runtimeLaunch{}, fmt.Errorf("OpenChamber 运行包未声明 openchamber CLI 入口")
	}
	cli, err := filepath.Localize(strings.TrimPrefix(bins["openchamber"], "./"))
	if err != nil || cli == "." {
		return runtimeLaunch{}, fmt.Errorf("OpenChamber 运行包声明的 CLI 入口路径无效")
	}
	cliRel := filepath.Join("@openchamber", "web", cli)
	for _, rel := range []string{cliRel, "@openchamber/web/dist/index.html", "@opencode/cli/bin/opencode.exe"} {
		info, err := os.Stat(filepath.Join(dir, "node_modules", filepath.FromSlash(rel)))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return runtimeLaunch{}, fmt.Errorf("运行包入口缺失: %s", rel)
		}
	}
	return runtimeLaunch{cliPath: filepath.Join(dir, "node_modules", cliRel), nodeEngine: release.NodeEngine}, nil
}

func Upgrade(version string) error       { return queueDeployment(version, true) }
func DeployVersion(version string) error { return queueDeployment(version, false) }
func RepairCurrent() error               { return queueDeployment(currentRuntimeVersion(), false) }

func queueDeployment(version string, isUpgrade bool) error {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version != "" {
		if _, ok := parseSemver(version); !ok {
			return fmt.Errorf("目标版本格式无效")
		}
	} else if !isUpgrade {
		return fmt.Errorf("请选择要部署的版本，尚未安装时不能修复")
	}
	if !maintenanceMu.TryLock() {
		return fmt.Errorf("正在执行服务操作或维护任务，请稍候")
	}
	if err := checkSnapshotRecovery(); err != nil {
		maintenanceMu.Unlock()
		return err
	}
	if state.Status() == StatusStarting || appContext.Err() != nil {
		maintenanceMu.Unlock()
		return fmt.Errorf("应用正在启动或停止，请稍候")
	}
	deploymentActive.Store(true)
	state.SetStatus(StatusBuilding, "正在准备在线部署与更新，读取官方发布信息")
	state.SetTargetVersion(version)
	cfg := GetConfig()
	go func() {
		defer maintenanceMu.Unlock()
		defer func() { deploymentActive.Store(false); state.Poke() }()
		deployVersion(version, cfg)
	}()
	return nil
}

func deploymentEnv(cfg Config, stage string) []string {
	values := map[string]string{
		"HOME":            filepath.Join(stage, "home"),
		"XDG_CONFIG_HOME": filepath.Join(stage, "config"), "XDG_DATA_HOME": filepath.Join(stage, "data"),
		"XDG_STATE_HOME": filepath.Join(stage, "state"), "XDG_CACHE_HOME": filepath.Join(stage, "cache"),
		"npm_config_cache":  filepath.Join(stage, "npm-cache"),
		"npm_config_devdir": filepath.Join(stage, "node-gyp"), "npm_config_prefix": filepath.Join(stage, "npm-global"),
		"TMPDIR": filepath.Join(stage, "tmp"), "TMP": filepath.Join(stage, "tmp"), "TEMP": filepath.Join(stage, "tmp"),
		"npm_config_registry": cfg.GetNpmRegistry(), "npm_config_engine_strict": "true",
		"npm_config_update_notifier": "false",
	}
	env := make([]string, 0, len(os.Environ())+len(values))
	for _, entry := range withoutProxyEnv() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[key]; !replaced {
			env = append(env, entry)
		}
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return append(env, networkProxyEnv(cfg, true)...)
}

func installRuntime(stage string, release runtimeRelease, cfg Config) error {
	dir := filepath.Join(stage, "payload")
	for _, name := range []string{"payload", "tmp", "npm-cache", "home", "config", "data", "state", "cache"} {
		if err := os.MkdirAll(filepath.Join(stage, name), 0700); err != nil {
			return err
		}
	}
	manifest := map[string]any{"private": true, "dependencies": map[string]string{webPackageName: release.WebVersion, openCodePackageName: release.CodeVersion}}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), data, 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(appContext, 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, npmBin(), "install", "--omit=dev", "--save-exact", "--no-audit", "--no-fund", "--registry="+cfg.GetNpmRegistry())
	cmd.Dir, cmd.Env = dir, deploymentEnv(cfg, stage)
	output := NewLogWriter()
	defer output.Flush()
	cmd.Stdout, cmd.Stderr = output, output
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			killProcessTree(cmd.Process.Pid)
		}
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
	LogInfo("[更新] 安装 OpenChamber %s / OpenCode %s", release.WebVersion, release.CodeVersion)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("官方运行包安装失败: %w", err)
	}
	if err := validateRuntime(dir); err != nil {
		return err
	}
	if readPackageVersion(dir, "@openchamber", "web") != release.WebVersion {
		return fmt.Errorf("安装结果与目标版本不一致")
	}
	return nil
}

func resolveDeployment(version string, cfg Config) (runtimeRelease, error) {
	if version == "" {
		var tags struct {
			DistTags map[string]string `json:"dist-tags"`
		}
		if err := fetchNpmJSON(cfg, webPackageName, "", &tags); err != nil {
			return runtimeRelease{}, err
		}
		version = tags.DistTags[cfg.GetUpdateChannel()]
	}
	current := currentRuntimeVersion()
	if version != current && !isChannelVersion(version, cfg.GetUpdateChannel()) {
		return runtimeRelease{}, fmt.Errorf("目标版本不属于所选更新渠道")
	}
	if current != "" && CompareSemver(version, current) < 0 {
		return runtimeRelease{}, fmt.Errorf("禁止降级：当前 %s，目标 %s", current, version)
	}
	release, err := resolveRuntimeRelease(cfg, version)
	if err != nil {
		return release, err
	}
	return release, checkRuntimePrerequisites(release.NodeEngine)
}

func deployVersion(version string, cfg Config) {
	if !recoverDeployment() {
		return
	}
	state.SetStatus(StatusBuilding, "正在读取更新渠道与配套运行版本")
	release, err := resolveDeployment(version, cfg)
	if err != nil {
		finishDeploymentFailure(err)
		return
	}
	state.SetTargetVersion(release.WebVersion)
	journal, err := prepareDeployment()
	if err != nil {
		if !recoverDeployment() {
			return
		}
		finishDeploymentFailure(err)
		return
	}
	stage := filepath.Join(globalPkgVar, journal.Stage)
	err = installRuntime(stage, release, cfg)
	if err == nil {
		err = appContext.Err()
	}
	if err == nil {
		err = cleanupDeploymentProcesses()
	}
	if err == nil {
		err = activateDeployment(&journal)
	}
	if err != nil {
		stopDeploymentAndRestore(journal, err)
		return
	}
	journal.Phase = "committed"
	if err := saveDeploymentJournal(journal); err != nil {
		persisted, readErr := readDeploymentJournal()
		if readErr != nil || persisted.Phase != "committed" {
			stopDeploymentAndRestore(journal, err)
			return
		}
		LogWarning("[更新] 提交记录尚未同步，将在清理前重试同步: %s", err)
	}
	_, _, serviceMessage, _, _, _, _ := state.Snapshot()
	state.SetStatus(StatusRunning, "新版服务已就绪，正在清理旧运行包、安装缓存和临时文件")
	if !recoverDeployment() {
		return
	}
	if GetActiveServicePID() == 0 {
		state.SetStatus(StatusStopped, "更新已提交，但服务已退出，请查看运行日志")
		return
	}
	LogInfo("[更新] 更新已提交，残留清理结果请查看此前日志")
	state.SetStatus(StatusRunning, serviceMessage)
}

func activateDeployment(journal *deploymentJournal) error {
	journal.Phase = "switching"
	if err := saveDeploymentJournal(*journal); err != nil {
		return err
	}
	stopAndWait()
	stage := filepath.Join(globalPkgVar, journal.Stage)
	if journal.HadRuntime {
		if err := os.Rename(runtimeDir, filepath.Join(stage, "previous")); err != nil {
			return err
		}
		if err := syncDeploymentDirectories(stage); err != nil {
			return err
		}
	}
	if err := os.Rename(filepath.Join(stage, "payload"), runtimeDir); err != nil {
		return err
	}
	if err := syncDeploymentDirectories(stage); err != nil {
		return err
	}
	refreshVersion()
	SetBuildTime(time.Now())
	state.SetStatus(StatusStopped, "")
	if err := startPreparedService(); err != nil {
		return err
	}
	return waitServiceReady()
}

// waitServiceReady is called under maintenanceMu. The caller stops a failed launch
// before releasing the lock or rolling back data/runtime.
func waitServiceReady() error {
	deadline := time.NewTimer(serviceStartupTimeout)
	ticker := time.NewTicker(time.Second)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-appContext.Done():
			return appContext.Err()
		case <-deadline.C:
			return fmt.Errorf("服务就绪探测超时，请查看运行日志")
		case <-ticker.C:
			if state.Status() == StatusRunning {
				return nil
			}
			if state.Status() == StatusStopped {
				return fmt.Errorf("服务未能成功启动，请查看运行日志")
			}
		}
	}
}

func stopDeploymentAndRestore(journal deploymentJournal, cause error) {
	if journal.Phase != "preparing" {
		stopAndWait()
	}
	LogError("[更新] 更新失败，恢复原运行包并清理: %s", cause)
	if !recoverDeployment() {
		return
	}
	if journal.WasRunning && GetActiveServicePID() == 0 && appContext.Err() == nil {
		state.SetStatus(StatusStopped, "更新失败，已恢复原运行包，正在启动")
		if err := startService(); err != nil {
			state.SetStatus(StatusStopped, "原运行包已恢复，启动失败: "+err.Error())
			return
		}
		if err := waitServiceReady(); err != nil {
			stopAndWait()
			state.SetStatus(StatusStopped, "原运行包已恢复，就绪检查失败: "+err.Error())
			return
		}
	}
	finishDeploymentFailure(cause)
}

func finishDeploymentFailure(err error) {
	refreshVersion()
	status := StatusStopped
	if GetActiveServicePID() > 0 {
		status = StatusRunning
	}
	state.SetStatus(status, "更新失败，现有数据保留: "+err.Error())
	LogError("[更新] %s", err)
}

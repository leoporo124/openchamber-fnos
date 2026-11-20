package main

import (
	"context"
	"embed"
	"fmt"
	"net"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/gin-gonic/gin"
)

//go:embed frontend/dist/*
var embeddedWebFS embed.FS

var (
	appContext, cancelApp = context.WithCancel(context.Background())
	globalPkgVar          string
	globalAppVer          string
	globalAppDest         string
	globalOpenChamberHome string
	globalHomeDir         string
	globalNpmCache        string
	globalSnapshotsDir    string
	runtimeDir            string
)

func main() {
	globalPkgVar = os.Getenv("DATA_LIBRARY_PATH")
	if globalPkgVar == "" {
		LogFatal("[主程序] 环境变量缺失: DATA_LIBRARY_PATH")
	}
	dataPath, err := filepath.EvalSymlinks(globalPkgVar)
	if err != nil || !filepath.IsAbs(dataPath) || dataPath == "/" {
		LogFatal("[主程序] 应用数据目录无效")
	}
	globalPkgVar = dataPath

	globalAppDest = strings.TrimSpace(os.Getenv("TRIM_APPDEST"))
	if globalAppDest == "" {
		LogFatal("[主程序] 环境变量缺失: TRIM_APPDEST")
	}

	globalAppVer = strings.TrimSpace(os.Getenv("TRIM_APPVER"))
	if globalAppVer == "" {
		LogFatal("[主程序] 环境变量缺失: TRIM_APPVER")
	}

	globalOpenChamberHome = filepath.Join(globalPkgVar, "openchamber-data")
	globalHomeDir = filepath.Join(globalPkgVar, "home")
	globalNpmCache = filepath.Join(globalPkgVar, "cache", "npm")
	globalSnapshotsDir = filepath.Join(globalPkgVar, "snapshots")
	runtimeDir = filepath.Join(globalPkgVar, "runtime")
	isCleanup := len(os.Args) == 2 && os.Args[1] == "--cleanup-runtime"
	var listener net.Listener
	if !isCleanup {
		listener, err = prepareGatewayListener()
		if err != nil {
			LogFatal("[主程序] 准备网关 Socket 失败: %s", err)
		}
		defer listener.Close()
	}
	if err := applyRuntimeUser(); err != nil {
		LogFatal("[主程序] 设置运行用户失败: %s", err)
	}
	if isCleanup {
		cleanupServiceProcesses()
		return
	}

	InitLogger()
	LogInfo("[主程序] OpenChamber 初始化 (数据目录=%s, 版本=%s)", globalPkgVar, globalAppVer)
	cleanupServiceProcesses()
	if err := recoverSnapshotRestore(); err != nil {
		LogFatal("[快照] 恢复中断任务失败，保留数据并停止启动: %s", err)
	}
	if err := cleanupSnapshotArtifacts(); err != nil {
		LogWarning("[快照] 临时文件尚未清理，将在下次快照操作或启动时重试: %s", err)
	}
	InitConfig()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		LogInfo("[主程序] 捕获终止信号 %s，执行停机流程", sig)
		cancelApp()
		maintenanceMu.Lock()
		stopAndWait()
		os.Exit(0)
	}()

	InitAppEnv()
	InitService()
	StartAppUpdateChecker()

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	WebFS = embeddedWebFS
	InitRoutes(r)
	StartWorkspaceWatch()

	LogInfo("[主程序] HTTP 服务已就绪，监听 Socket: %s", listener.Addr())
	if err := r.RunListener(listener); err != nil {
		LogFatal("[主程序] HTTP 服务异常退出: %s", err)
	}
}

func prepareGatewayListener() (net.Listener, error) {
	socketPath := filepath.Join(globalAppDest, "web.sock")
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socketPath, 0666); err != nil {
		listener.Close()
		return nil, err
	}
	return newGatewayListener(listener.(*net.UnixListener)), nil
}

// applyRuntimeUser 在启动管理服务和子进程前切换到安装时选定的用户。
func applyRuntimeUser() error {
	name := os.Getenv("OPENCHAMBER_RUN_USER")
	if name == "" || name == "root" {
		return nil
	}
	if name != "openchamber" {
		return fmt.Errorf("运行用户无效")
	}
	account, err := user.Lookup(name)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil || uid <= 0 {
		return fmt.Errorf("应用用户 UID 无效")
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil || gid <= 0 {
		return fmt.Errorf("应用用户 GID 无效")
	}
	groupIDs, err := account.GroupIds()
	if err != nil {
		return err
	}
	groups := make([]int, 0, len(groupIDs))
	for _, id := range groupIDs {
		group, err := strconv.Atoi(id)
		if err != nil || group < 0 {
			return fmt.Errorf("应用用户附加组无效")
		}
		groups = append(groups, group)
	}
	if err := syscall.Setgroups(groups); err != nil {
		return err
	}
	if err := syscall.Setgid(gid); err != nil {
		return err
	}
	if err := syscall.Setuid(uid); err != nil {
		return err
	}
	if err := os.Setenv("USER", name); err != nil {
		return err
	}
	return os.Setenv("LOGNAME", name)
}

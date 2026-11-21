package main

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var WebFS embed.FS

const basePath = "/app/openchamber"

func InitRoutes(r *gin.Engine) {
	r.Use(func(c *gin.Context) {
		path := c.Request.URL.Path
		isManaged := path == basePath || strings.HasPrefix(path, basePath+"/")
		if strings.HasPrefix(path, basePath+"/api/") {
			c.Header("Cache-Control", "no-store")
		}
		if isManaged {
			if _, ok := gatewayAdminID(c.Request); !ok {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"message": "需要通过飞牛网关以管理员身份访问"})
				return
			}
		}
		if isManaged && !hasValidOrigin(c.Request) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"message": "请求来源不匹配"})
			return
		}
		c.Next()
	})
	base := r.Group(basePath)

	// 注册飞牛网关直连 WebUI 代理路由
	InitFnGateway(base)

	api := base.Group("/api")
	{
		api.GET("/ws", handleWS)
		api.GET("/status", func(c *gin.Context) { OK(c, statusPayload()) })
		api.POST("/action", handleAction)
		api.GET("/check-update", handleCheckUpdate)
		api.GET("/deploy/versions", handleListRuntimeVersions)
		api.GET("/logs", handleGetLogs)
		api.DELETE("/logs", handleDeleteLogs)
		api.GET("/logs/download", handleDownloadLogs)
		api.GET("/config", handleGetConfig)
		api.POST("/config", handleSaveConfig)
		api.GET("/workspace/list", handleGetWorkspaces)
		api.GET("/snapshots", handleListSnapshots)
		api.POST("/snapshots", handleCreateSnapshot)
		api.POST("/snapshots/cancel", handleCancelSnapshot)
		api.POST("/snapshots/:id/restore", handleRestoreSnapshot)
		api.DELETE("/snapshots/:id", handleDeleteSnapshot)
	}

	sub, err := fs.Sub(WebFS, "frontend/dist")
	if err != nil {
		LogFatal("[API] 静态前端资源装载失败: %s", err)
	}
	fileServer := http.FileServer(http.FS(sub))

	base.GET("/", func(c *gin.Context) {
		c.Request.URL.Path = "/"
		fileServer.ServeHTTP(c.Writer, c.Request)
	})

	r.NoRoute(func(c *gin.Context) {
		if c.Request.URL.Path == basePath {
			target := basePath + "/"
			if c.Request.URL.RawQuery != "" {
				target += "?" + c.Request.URL.RawQuery
			}
			c.Redirect(http.StatusMovedPermanently, target)
			return
		}
		if c.Request.URL.Path != basePath && !strings.HasPrefix(c.Request.URL.Path, basePath+"/") {
			c.Status(http.StatusNotFound)
			return
		}
		// 飞牛网关直连 WebUI 请求交由网关代理处理
		if c.Request.URL.Path == fnGatewayPrefix || strings.HasPrefix(c.Request.URL.Path, fnGatewayPrefix+"/") {
			handleFnGateway(c)
			return
		}

		// 未知 API 路径返回 404 JSON，不回退首页
		if strings.HasPrefix(c.Request.URL.Path, basePath+"/api/") {
			c.JSON(http.StatusNotFound, gin.H{"message": "接口不存在"})
			return
		}
		fp := strings.TrimPrefix(c.Request.URL.Path, basePath)
		if fp == "" {
			fp = "/"
		}
		f, err := sub.Open(strings.TrimPrefix(fp, "/"))
		if err == nil {
			f.Close()
			c.Request.URL.Path = fp
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		c.Request.URL.Path = "/"
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}

// ApiResponse 统一 API 响应契约
type ApiResponse struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data,omitempty"`
	Timestamp int64  `json:"timestamp,omitempty"`
}

func OK(c *gin.Context, data any) {
	OKMsg(c, "success", data)
}

func OKMsg(c *gin.Context, msg string, data any) {
	c.JSON(http.StatusOK, ApiResponse{
		Code:      0,
		Message:   msg,
		Data:      data,
		Timestamp: time.Now().UnixMilli(),
	})
}

func Fail(c *gin.Context, status int, msg string) {
	c.JSON(status, ApiResponse{
		Code:      status,
		Message:   msg,
		Timestamp: time.Now().UnixMilli(),
	})
}

func statusPayload() gin.H {
	status, uptime, lastMsg, version, buildTime, targetVersion, startedAt := state.Snapshot()
	cfg := GetConfig()

	appURL := ":" + strconv.Itoa(cfg.GetProxyPort()) + "/"
	serverPort := cfg.GetServerPort()
	var pidVal any
	if status == StatusRunning || status == StatusStarting {
		if pid := GetActiveServicePID(); pid > 0 {
			pidVal = pid
		}
	}

	var verVal any
	if version != "" {
		verVal = version
	}

	var buildTimeVal any
	if buildTime != "" {
		buildTimeVal = buildTime
	}

	var uptimeVal any
	if uptime != "" {
		uptimeVal = uptime
	}

	cpuVal, memVal := GetCachedServiceUsage()
	remoteAppVersion, hasAppUpdate := appUpdateStatus()

	return gin.H{
		"name":               "OpenChamber",
		"version":            verVal,
		"opencode_version":   readOpenCodeVersion(),
		"app_version":        globalAppVer,
		"app_remote_version": remoteAppVersion,
		"app_has_update":     hasAppUpdate,
		"target_version":     targetVersion,
		"status":             status,
		"uptime":             uptimeVal,
		"started_at":         startedAt,
		"server_port":        serverPort,
		"server_time":        time.Now().Unix(),
		"build_time":         buildTimeVal,
		"app_url":            appURL,
		"pid":                pidVal,
		"cpu":                cpuVal,
		"memory":             memVal,
		"last_message":       lastMsg,
		"maintenance_active": deploymentActive.Load(),
		"config_revision":    configRevision(cfg),
	}
}

// wsUpgrader 的来源校验由管理路由中间件统一执行。
var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// wsMsg WebSocket 消息：统一信封结构
type wsMsg struct {
	Type      string `json:"type"`
	Data      any    `json:"data"`
	Timestamp int64  `json:"timestamp"`
}

// handleWS WebSocket：状态与日志实时推送及双向心跳
func handleWS(c *gin.Context) {
	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		LogWarning("[API] WebSocket 连接升级失败: %s", err)
		return
	}
	defer conn.Close()

	conn.SetReadLimit(4096)
	if err := conn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
		return
	}
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	sendMsg := func(msgType string, data any) bool {
		if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return false
		}
		return conn.WriteJSON(wsMsg{
			Type:      msgType,
			Data:      data,
			Timestamp: time.Now().UnixMilli(),
		}) == nil
	}

	// 先订阅再读取当前数据，连接期间的变更不会遗漏。
	stateCh, unsubscribeState := state.SubscribeState(16)
	defer unsubscribeState()
	logCh, unsubscribeLog := SubscribeLog(256)
	defer unsubscribeLog()
	wsCh, unsubscribeWs := SubscribeWorkspace(16)
	defer unsubscribeWs()
	snapCh, unsubscribeSnap := SubscribeSnapshot(16)
	defer unsubscribeSnap()
	snapProgressCh, unsubscribeSnapProgress := SubscribeSnapshotProgress(32)
	defer unsubscribeSnapProgress()
	usageCh, unsubscribeUsage := SubscribeUsage(16)
	defer unsubscribeUsage()

	done := make(chan struct{})
	ping := make(chan struct{}, 1)
	go func() {
		defer close(done)
		defer conn.Close()
		for {
			var inMsg struct {
				Type string `json:"type"`
			}
			if err := conn.ReadJSON(&inMsg); err != nil {
				return
			}
			if err := conn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
				return
			}
			if inMsg.Type == "ping" {
				select {
				case ping <- struct{}{}:
				default:
				}
			}
		}
	}()
	var lastLogSequence uint64
	sendLogs := func() bool {
		payload, err := readLogPayload(300)
		if err != nil {
			return false
		}
		lastLogSequence = payload.Sequence
		return sendMsg("log_snapshot", payload)
	}
	if !sendMsg("status", statusPayload()) || !sendMsg("workspace", GetWorkspaces()) || !sendLogs() {
		return
	}
	summary, err := ListSnapshots()
	if err != nil {
		LogWarning("[API] 读取快照列表失败: %s", err)
		return
	}
	if !sendMsg("snapshot", summary) {
		return
	}
	if !sendMsg("snapshot_progress", GetCurrentSnapshotProgress()) {
		return
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		var sent bool
		select {
		case <-appContext.Done():
			return
		case <-done:
			return
		case <-ping:
			sent = sendMsg("pong", gin.H{"server_time": time.Now().Unix()})
		case <-stateCh:
			sent = sendMsg("status", statusPayload())
		case chunk := <-logCh:
			if chunk.Sequence <= lastLogSequence {
				continue
			}
			if chunk.Reset || chunk.Sequence != lastLogSequence+1 {
				sent = sendLogs()
			} else {
				sent = sendMsg("log", chunk.Text)
				lastLogSequence = chunk.Sequence
			}
		case <-wsCh:
			sent = sendMsg("workspace", GetWorkspaces())
		case <-snapCh:
			summary, err := ListSnapshots()
			if err != nil {
				LogWarning("[API] 读取快照列表失败: %s", err)
				return
			}
			sent = sendMsg("snapshot", summary)
		case <-snapProgressCh:
			sent = sendMsg("snapshot_progress", GetCurrentSnapshotProgress())
		case u := <-usageCh:
			sent = sendMsg("usage", u)
		case <-heartbeat.C:
			sent = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)) == nil
		}
		if !sent {
			return
		}
	}
}

func handleAction(c *gin.Context) {
	var req struct {
		Action        string `json:"action"`
		TargetVersion string `json:"target_version"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "参数错误")
		return
	}

	switch req.Action {
	case "start", "stop", "restart":
		if state.Status() == StatusBuilding {
			Fail(c, http.StatusConflict, "正在部署更新中，请稍候再试")
			return
		}
		if state.Status() == StatusSnapshotting {
			Fail(c, http.StatusConflict, "正在快照维护中，请稍候再试")
			return
		}
		if state.Status() == StatusStarting && req.Action == "start" {
			Fail(c, http.StatusConflict, "服务正在启动中，请稍候")
			return
		}
		var err error
		switch req.Action {
		case "start":
			err = Start()
		case "stop":
			err = Stop()
		case "restart":
			err = Restart()
		}
		if err != nil {
			Fail(c, actionErrStatus(err), err.Error())
			return
		}
	case "upgrade", "deploy", "repair":
		if state.Status() == StatusBuilding {
			Fail(c, http.StatusConflict, "正在部署更新中，请稍候再试")
			return
		}
		if state.Status() == StatusSnapshotting {
			Fail(c, http.StatusConflict, "正在快照维护中，请稍候再试")
			return
		}
		var err error
		switch req.Action {
		case "upgrade":
			err = Upgrade(req.TargetVersion)
		case "deploy":
			targetVersion := strings.TrimSpace(req.TargetVersion)
			if targetVersion == "" {
				Fail(c, http.StatusBadRequest, "请选择要部署的 OpenChamber 版本")
				return
			}
			err = DeployVersion(targetVersion)
		case "repair":
			err = RepairCurrent()
		}
		if err != nil {
			Fail(c, actionErrStatus(err), err.Error())
			return
		}
	default:
		Fail(c, http.StatusBadRequest, "未知操作: "+req.Action)
		return
	}

	var msg string
	switch req.Action {
	case "start":
		msg = "服务正在启动…"
	case "stop":
		msg = "服务已停止"
	case "restart":
		msg = "服务正在重启…"
	case "upgrade":
		msg = "开始拉取远程更新并部署…"
	case "deploy":
		msg = "开始在线部署 OpenChamber 与 OpenCode…"
	case "repair":
		msg = "开始重新部署当前运行版本…"
	}

	OKMsg(c, msg, statusPayload())
}

func handleCheckUpdate(c *gin.Context) {
	res, err := CheckUpdate()
	if err != nil {
		Fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	OKMsg(c, res.Message, res)
}

func handleListRuntimeVersions(c *gin.Context) {
	res, err := ListRuntimeVersions()
	if err != nil {
		Fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	OK(c, res)
}

// actionErrStatus 将动作前置错误映射为合适的 HTTP 状态码
func actionErrStatus(err error) int {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "运行环境未就绪"), strings.Contains(msg, "未就绪"):
		return http.StatusNotFound
	case strings.Contains(msg, "部署中"), strings.Contains(msg, "更新中"), strings.Contains(msg, "启动中"), strings.Contains(msg, "运行中"), strings.Contains(msg, "维护任务"):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

type LogPayload struct {
	Lines    []string `json:"lines"`
	Content  string   `json:"content"`
	Sequence uint64   `json:"sequence"`
}

// readLastNLines 高效获取日志文件末尾的最新 N 行
func readLastNLines(path string, maxLines int) ([]string, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	size := stat.Size()
	if size == 0 {
		return []string{}, "", nil
	}

	// 若文件较小（<= 512KB），直接全量读取并截取后 N 行
	if size <= 512*1024 {
		data, err := io.ReadAll(file)
		if err != nil {
			return nil, "", err
		}
		rawLines := strings.Split(string(data), "\n")
		var lines []string
		for _, l := range rawLines {
			if l != "" {
				lines = append(lines, l+"\n")
			}
		}
		if len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
		}
		return lines, strings.Join(lines, ""), nil
	}

	// 若文件较大，从尾部逆向读取最多 256KB
	readSize := int64(256 * 1024)
	if readSize > size {
		readSize = size
	}
	buf := make([]byte, readSize)
	_, err = file.ReadAt(buf, size-readSize)
	if err != nil && err != io.EOF {
		return nil, "", err
	}

	raw := string(buf)
	if readSize < size {
		// 丢弃第一条可能截断的不完整半行
		if idx := strings.IndexByte(raw, '\n'); idx >= 0 {
			raw = raw[idx+1:]
		}
	}

	rawLines := strings.Split(raw, "\n")
	var lines []string
	for _, l := range rawLines {
		if l != "" {
			lines = append(lines, l+"\n")
		}
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines, strings.Join(lines, ""), nil
}

func handleGetLogs(c *gin.Context) {
	limitStr := c.DefaultQuery("limit", "300")
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = 300
	}
	if limit > 1000 {
		limit = 1000
	}
	payload, err := readLogPayload(limit)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "读取日志失败: "+err.Error())
		return
	}
	OK(c, payload)
}

func handleDeleteLogs(c *gin.Context) {
	if err := ClearLogs(); err != nil {
		Fail(c, http.StatusInternalServerError, "清空日志失败: "+err.Error())
		return
	}
	OKMsg(c, "运行日志已清空", true)
}

func handleDownloadLogs(c *gin.Context) {
	c.FileAttachment(GetLogFilePath(), "openchamber.log")
}

func handleGetConfig(c *gin.Context) {
	OK(c, configResponse())
}

// checkTCPAddressAvailable 按服务实际的监听地址检查端口。
func checkTCPAddressAvailable(address string) error {
	ln, err := net.Listen("tcp4", address)
	if err != nil {
		return err
	}
	_ = ln.Close()
	return nil
}

func handleSaveConfig(c *gin.Context) {
	var cfg Config
	if err := c.ShouldBindJSON(&cfg); err != nil {
		Fail(c, http.StatusBadRequest, "配置格式无效")
		return
	}
	if !maintenanceMu.TryLock() {
		Fail(c, http.StatusConflict, "正在执行服务操作或维护任务，请稍候")
		return
	}
	defer maintenanceMu.Unlock()
	if err := checkMaintenanceRecovery(); err != nil {
		Fail(c, http.StatusConflict, err.Error())
		return
	}
	if state.Status() == StatusStarting {
		Fail(c, http.StatusConflict, "服务正在启动中，请稍候再保存")
		return
	}
	if err := validateConfig(cfg); err != nil {
		Fail(c, http.StatusBadRequest, err.Error())
		return
	}

	oldCfg := GetConfig()
	if c.GetHeader("If-Match") != strconv.Quote(configRevision(oldCfg)) {
		Fail(c, http.StatusPreconditionFailed, "服务端设置已变更或缺少配置版本，请重新加载设置后再保存")
		return
	}
	serverPortChanged := oldCfg.ServerPort != cfg.ServerPort
	proxyPortChanged := oldCfg.ProxyPort != cfg.ProxyPort
	proxyAddressChanged := oldCfg.proxyListenAddress() != cfg.proxyListenAddress()
	heapMemChanged := oldCfg.HeapMemoryLimit != cfg.HeapMemoryLimit
	proxyRuntimeChanged := oldCfg.ProxyRuntime != cfg.ProxyRuntime ||
		(cfg.ProxyRuntime && (oldCfg.NetworkProxy != cfg.NetworkProxy || oldCfg.GetNpmRegistry() != cfg.GetNpmRegistry()))

	if serverPortChanged {
		if err := checkTCPAddressAvailable(fmt.Sprintf("127.0.0.1:%d", cfg.ServerPort)); err != nil {
			Fail(c, http.StatusBadRequest, fmt.Sprintf("内部监听端口 %d 已被占用，请更换端口", cfg.ServerPort))
			return
		}
	}

	if proxyPortChanged {
		if err := checkTCPAddressAvailable(cfg.proxyListenAddress()); err != nil {
			Fail(c, http.StatusBadRequest, fmt.Sprintf("反向代理端口 %d 已被占用，请更换端口", cfg.ProxyPort))
			return
		}
	}

	if err := SaveConfig(cfg); err != nil {
		Fail(c, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}

	ApplyNpmRegistryEnv()
	if state.Status() == StatusRunning && (serverPortChanged || heapMemChanged || proxyRuntimeChanged) {
		stopAndWait()
		if err := startService(); err != nil {
			state.SetStatus(StatusStopped, "设置已保存，服务重启失败: "+err.Error())
		}
	} else if proxyAddressChanged {
		restartReverseProxy()
	}

	state.Poke()
	OKMsg(c, "应用设置保存成功", configResponse())
}

func handleListSnapshots(c *gin.Context) {
	summary, err := ListSnapshots()
	if err != nil {
		Fail(c, http.StatusInternalServerError, "获取快照列表失败: "+err.Error())
		return
	}
	OK(c, summary)
}

func handleCreateSnapshot(c *gin.Context) {
	var params CreateSnapshotParams
	if err := c.ShouldBindJSON(&params); err != nil {
		Fail(c, http.StatusBadRequest, "快照参数无效")
		return
	}

	cur := state.Status()
	if cur == StatusBuilding {
		Fail(c, http.StatusConflict, "服务正在部署更新中，请稍候再试")
		return
	}
	if cur == StatusSnapshotting {
		Fail(c, http.StatusConflict, "已有快照任务正在执行中，请勿重复操作")
		return
	}
	if cur == StatusStarting {
		Fail(c, http.StatusConflict, "服务正在启动中，请等待启动完成后再创建快照")
		return
	}

	targetName := strings.TrimSpace(params.Name)
	if targetName == "" {
		targetName = "手动快照_" + time.Now().Format("0102_1504")
	}
	params.Name = targetName

	if err := queueSnapshotCreation(params); err != nil {
		Fail(c, http.StatusBadRequest, err.Error())
		return
	}

	OKMsg(c, "快照创建任务已启动", gin.H{"name": params.Name})
}

func handleCancelSnapshot(c *gin.Context) {
	if err := CancelSnapshotCreation(); err != nil {
		Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	OKMsg(c, "已提交取消请求，正在结束快照创建", nil)
}

func handleRestoreSnapshot(c *gin.Context) {
	id := c.Param("id")
	meta, err := queueSnapshotRestore(id)
	if err != nil {
		Fail(c, http.StatusBadRequest, err.Error())
		return
	}

	OKMsg(c, "快照还原任务已启动", gin.H{"id": id, "name": meta.Name})
}

func handleDeleteSnapshot(c *gin.Context) {
	id := c.Param("id")
	if err := DeleteSnapshot(id); err != nil {
		Fail(c, http.StatusInternalServerError, "删除快照失败: "+err.Error())
		return
	}
	OKMsg(c, "快照已成功删除", nil)
}

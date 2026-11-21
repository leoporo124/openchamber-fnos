package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

//go:embed templates/auth_login.html
var authLoginPageTplContent string

var authLoginPageTpl = template.Must(template.New("auth_login").Parse(authLoginPageTplContent))

const (
	authCookieName      = "openchamber_session"
	authLoginPath       = "/_openchamber_auth"
	authMaxAttempts     = 3
	authLockoutDuration = 1 * time.Hour
)

type clientAuthStatus struct {
	failedCount int
	lockUntil   time.Time
}

var (
	authLockMu       sync.Mutex
	clientAuthRecord = make(map[string]*clientAuthStatus)
	authSecret       [32]byte
	authSecretOnce   sync.Once
	localTransport   = &http.Transport{Proxy: nil, DisableCompression: true}
)

func getAuthClientKey(r *http.Request) string {
	if uid, ok := gatewayAdminID(r); ok {
		return "gateway-user:" + uid
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func checkAuthLockout(clientKey string) (bool, time.Duration, int) {
	authLockMu.Lock()
	defer authLockMu.Unlock()

	status, exists := clientAuthRecord[clientKey]
	if !exists {
		return false, 0, authMaxAttempts
	}

	if status.failedCount >= authMaxAttempts {
		now := time.Now()
		if now.Before(status.lockUntil) {
			return true, status.lockUntil.Sub(now), 0
		}
		delete(clientAuthRecord, clientKey)
		return false, 0, authMaxAttempts
	}

	return false, 0, authMaxAttempts - status.failedCount
}

func recordAuthFailure(clientKey string) (bool, time.Duration, int) {
	authLockMu.Lock()
	defer authLockMu.Unlock()

	status, exists := clientAuthRecord[clientKey]
	if !exists {
		status = &clientAuthStatus{}
		clientAuthRecord[clientKey] = status
	}

	status.failedCount++
	if status.failedCount >= authMaxAttempts {
		status.lockUntil = time.Now().Add(authLockoutDuration)
		return true, authLockoutDuration, 0
	}

	return false, 0, authMaxAttempts - status.failedCount
}

func recordAuthSuccess(clientKey string) {
	authLockMu.Lock()
	delete(clientAuthRecord, clientKey)
	authLockMu.Unlock()
}

func getAuthToken(pwd string) string {
	authSecretOnce.Do(func() {
		if _, err := rand.Read(authSecret[:]); err != nil {
			LogFatal("[访问验证] 无法生成会话密钥: %s", err)
		}
	})
	mac := hmac.New(sha256.New, authSecret[:])
	mac.Write([]byte(pwd))
	return hex.EncodeToString(mac.Sum(nil))
}

func isValidAuthCookie(r *http.Request, pwd string) bool {
	if c, err := r.Cookie(authCookieName); err == nil && c.Value != "" {
		return hmac.Equal([]byte(c.Value), []byte(getAuthToken(pwd)))
	}
	return false
}

// proxyWithAuth 密码中间件
func proxyWithAuth(next http.Handler, prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if prefix == "" && !hasValidOrigin(r) {
			http.Error(w, "请求来源不匹配", http.StatusForbidden)
			return
		}

		cfg := GetConfig()
		// 配置刚变更时，旧监听器可能仍在处理请求。只使用实际连接的对端地址。
		if prefix == "" && !cfg.allowsRemoteProxy() && !isLoopbackPeer(r.RemoteAddr) {
			http.Error(w, "请在 OpenChamber Admin 中设置访问密码后再使用独立端口", http.StatusForbidden)
			return
		}
		pwd := cfg.AccessPassword
		if pwd == "" {
			serveManagedWebUI(next, w, r, prefix)
			return
		}

		clientKey := getAuthClientKey(r)
		isLocked, lockRemaining, remainingAttempts := checkAuthLockout(clientKey)

		// 处理登录验证请求
		if r.URL.Path == prefix+authLoginPath && r.Method == http.MethodPost {
			w.Header().Set("Cache-Control", "no-store")
			r.Body = http.MaxBytesReader(w, r.Body, 8192)
			isJSONReq := strings.Contains(r.Header.Get("Content-Type"), "application/json") ||
				strings.Contains(r.Header.Get("Accept"), "application/json")

			if isLocked {
				mins := int(lockRemaining.Minutes()) + 1
				if isJSONReq {
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(http.StatusTooManyRequests)
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"code":        http.StatusTooManyRequests,
						"locked":      true,
						"remain_mins": mins,
						"message":     fmt.Sprintf("密码错误达 3 次已锁定，请等待约 %d 分钟或重启服务", mins),
					})
					return
				}
				serveLoginPage(w, isLocked, lockRemaining, 0, prefix)
				return
			}

			// 获取输入的密码
			inputPwd := ""
			if isJSONReq {
				var reqBody struct {
					Password string `json:"password"`
				}
				if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
					http.Error(w, "密码请求格式无效", http.StatusBadRequest)
					return
				}
				inputPwd = reqBody.Password
			} else {
				if err := r.ParseForm(); err != nil {
					http.Error(w, "密码请求格式无效", http.StatusBadRequest)
					return
				}
				inputPwd = r.FormValue("password")
			}

			if inputPwd == pwd {
				recordAuthSuccess(clientKey)
				token := getAuthToken(pwd)
				cookie := &http.Cookie{
					Name:     authCookieName,
					Value:    token,
					Path:     prefix + "/",
					MaxAge:   86400 * 30, // 30天有效
					Expires:  time.Now().Add(30 * 24 * time.Hour),
					HttpOnly: true,
					Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
					SameSite: http.SameSiteLaxMode,
				}
				http.SetCookie(w, cookie)

				if isJSONReq {
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"code":     0,
						"message":  "验证成功",
						"redirect": prefix + "/",
					})
					return
				}
				http.Redirect(w, r, prefix+"/", http.StatusSeeOther)
			} else {
				lockedNow, remDuration, remAttempts := recordAuthFailure(clientKey)
				if isJSONReq {
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					status := http.StatusUnauthorized
					msg := fmt.Sprintf("密码错误，还可尝试 %d 次", remAttempts)
					if lockedNow {
						status = http.StatusTooManyRequests
						mins := int(remDuration.Minutes()) + 1
						msg = fmt.Sprintf("密码错误达 3 次已锁定，请等待约 %d 分钟或重启服务", mins)
					}
					w.WriteHeader(status)
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"code":               status,
						"locked":             lockedNow,
						"remaining_attempts": remAttempts,
						"message":            msg,
					})
					return
				}
				serveLoginPage(w, lockedNow, remDuration, remAttempts, prefix)
			}
			return
		}

		// 校验 cookie
		if !isValidAuthCookie(r, pwd) {
			if strings.Contains(r.Header.Get("Accept"), "text/html") || r.URL.Path == prefix+"/" {
				serveLoginPage(w, isLocked, lockRemaining, remainingAttempts, prefix)
			} else {
				http.Error(w, "请先验证访问密码", http.StatusUnauthorized)
			}
			return
		}

		serveManagedWebUI(next, w, r, prefix)
	})
}

func isLoopbackPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	return err == nil && net.ParseIP(host).IsLoopback()
}

// serveManagedWebUI 承接飞牛目录定位与官方更新的宿主适配。
func serveManagedWebUI(next http.Handler, w http.ResponseWriter, r *http.Request, prefix string) {
	if strings.HasPrefix(r.URL.Path, prefix+fnOSAssetsPath) {
		serveFnOSAssets(w, r, prefix)
		return
	}
	if strings.TrimSuffix(r.URL.Path, "/") == prefix+"/api/fs/reveal" && r.Method == http.MethodPost {
		serveFnOSReveal(w, r)
		return
	}
	if strings.TrimSuffix(r.URL.Path, "/") == prefix+"/api/openchamber/update-install" && r.Method == http.MethodPost {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "请在 OpenChamber Admin 中更新，以便统一校验版本、恢复失败更新并清理安装文件。"})
		return
	}
	next.ServeHTTP(w, r)
}

func serveLoginPage(w http.ResponseWriter, isLocked bool, lockRemaining time.Duration, remainingAttempts int, prefix string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	var errorHTML template.HTML
	buttonText := "进入"

	if isLocked {
		w.WriteHeader(http.StatusTooManyRequests)
		mins := int(lockRemaining.Minutes()) + 1
		errorHTML = template.HTML(fmt.Sprintf(`<div class="err" id="errMsg">
      <svg width="15" height="15" viewBox="0 0 20 20" fill="currentColor">
        <path fill-rule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zM8.707 7.293a1 1 0 00-1.414 1.414L8.586 10l-1.293 1.293a1 1 0 101.414 1.414L10 11.414l1.293 1.293a1 1 0 001.414-1.414L11.414 10l1.293-1.293a1 1 0 00-1.414-1.414L10 8.586 8.707 7.293z" clip-rule="evenodd"/>
      </svg>
      <span id="errText">密码错误达 3 次已锁定，请等待约 %d 分钟或重启服务</span>
    </div>`, mins))
		buttonText = "已锁定冷却中"
	} else if remainingAttempts < authMaxAttempts {
		w.WriteHeader(http.StatusUnauthorized)
		errorHTML = template.HTML(fmt.Sprintf(`<div class="err" id="errMsg">
      <svg width="15" height="15" viewBox="0 0 20 20" fill="currentColor">
        <path fill-rule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zM8.707 7.293a1 1 0 00-1.414 1.414L8.586 10l-1.293 1.293a1 1 0 101.414 1.414L10 11.414l1.293 1.293a1 1 0 001.414-1.414L11.414 10l1.293-1.293a1 1 0 00-1.414-1.414L10 8.586 8.707 7.293z" clip-rule="evenodd"/>
      </svg>
      <span id="errText">密码错误，还可尝试 %d 次</span>
    </div>`, remainingAttempts))
	}

	data := struct {
		IsLocked      bool
		ButtonText    string
		ErrorHTML     template.HTML
		AuthLoginPath string
	}{
		IsLocked:      isLocked,
		ButtonText:    buttonText,
		ErrorHTML:     errorHTML,
		AuthLoginPath: prefix + authLoginPath,
	}

	if err := authLoginPageTpl.Execute(w, data); err != nil {
		LogWarning("[访问验证] 输出页面失败: %s", err)
	}
}

var (
	proxyMu   sync.Mutex
	proxyHTTP *http.Server
)

func startReverseProxy() error {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	return startReverseProxyLocked()
}

func startReverseProxyLocked() error {
	if proxyHTTP != nil {
		return nil
	}

	cfg := GetConfig()
	proxyTarget, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", cfg.GetServerPort()))
	proxyAddr := cfg.proxyListenAddress()

	errHandler := func(w http.ResponseWriter, r *http.Request, err error) {
		// 过滤客户端主动断开连接/取消请求的正常行为
		if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) || strings.Contains(err.Error(), "context canceled") {
			return
		}
		LogWarning("[代理] 请求转发失败 [%s]: %s", proxyAddr, err)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":   "bad_gateway",
			"message": proxyErrMessage(),
			"detail":  err.Error(),
		})
	}

	proxy := &httputil.ReverseProxy{
		Transport:     localTransport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(proxyTarget)
			pr.SetXForwarded()
			pr.Out.Host = proxyTarget.Host
			pr.Out.Header.Set("Accept-Encoding", "identity")
			prepareDevServerDiscovery(pr.Out)
			if strings.Contains(pr.In.Header.Get("Accept"), "text/html") {
				pr.Out.Header.Del("If-None-Match")
				pr.Out.Header.Del("If-Modified-Since")
			}
			if pr.Out.Header.Get("Origin") != "" {
				pr.Out.Header.Set("Origin", fmt.Sprintf("%s://%s", proxyTarget.Scheme, proxyTarget.Host))
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if err := filterDevServerDiscovery(resp); err != nil {
				return err
			}
			// 改写上游重定向地址，去除回环主机头防止协议漂移
			if loc := resp.Header.Get("Location"); loc != "" {
				if u, err := url.Parse(loc); err == nil && u.Host != "" {
					if u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" {
						u.Scheme = ""
						u.Host = ""
						resp.Header.Set("Location", u.String())
					}
				}
			}

			contentType := strings.ToLower(resp.Header.Get("Content-Type"))
			if strings.Contains(contentType, "text/html") && resp.Body != nil {
				return rewriteWebUIHTML(resp, "")
			}

			// 处理 SSE 流式响应标头
			if strings.HasPrefix(contentType, "text/event-stream") {
				resp.Header.Set("Cache-Control", "no-cache, no-transform")
				resp.Header.Set("X-Accel-Buffering", "no")
				resp.Header.Del("Content-Length")
				return nil
			}

			return nil
		},
		ErrorHandler: errHandler,
	}

	// 建立 TCP 监听器
	ln, err := net.Listen("tcp4", proxyAddr)
	if err != nil {
		LogWarning("[代理] 端口监听失败 [%s]: %s", proxyAddr, err)
		return err
	}

	server := &http.Server{Handler: proxyWithAuth(proxy, ""), ReadHeaderTimeout: 10 * time.Second}
	proxyHTTP = server

	LogInfo("[代理] 已启动监听 [%s → %s]", proxyAddr, proxyTarget.String())

	go func() {
		if err := server.Serve(ln); err != nil && !isExpectedCloseErr(err) {
			LogWarning("[代理] HTTP 代理服务异常退出: %s", err)
		}
	}()

	return nil
}

func isExpectedCloseErr(err error) bool {
	if err == nil || err == http.ErrServerClosed || err == net.ErrClosed || errors.Is(err, context.Canceled) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "server closed") ||
		strings.Contains(msg, "closed network connection") ||
		strings.Contains(msg, "context canceled")
}

func stopReverseProxy() {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	stopReverseProxyLocked()
}

func stopReverseProxyLocked() {
	if proxyHTTP == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := proxyHTTP.Shutdown(ctx); err != nil {
		_ = proxyHTTP.Close()
	}
	proxyHTTP = nil
	LogInfo("[代理] 监听已停止")
}

// proxyErrMessage 根据当前服务状态给出准确的代理错误提示
func proxyErrMessage() string {
	switch state.Status() {
	case StatusStarting:
		return "服务正在启动"
	case StatusRunning:
		return "服务响应异常"
	case StatusBuilding:
		return "服务正在部署更新"
	case StatusSnapshotting:
		return "服务快照维护中"
	case StatusStopped:
		return "服务未运行"
	default:
		return "无法连接到后端服务"
	}
}

// restartReverseProxy 按最新配置重启反向代理
func restartReverseProxy() {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	stopReverseProxyLocked()
	if state.Status() != StatusRunning {
		return
	}
	LogInfo("[代理] 配置变更，执行热重载")
	if err := startReverseProxyLocked(); err != nil {
		LogWarning("[代理] 热重载失败: %s", err)
	}
}

func hasValidOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return request.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	localAddress, ok := request.Context().Value(http.LocalAddrContextKey).(net.Addr)
	isGateway := ok && localAddress.Network() == "unix"
	hosts := []string{request.Host}
	if isGateway {
		// 飞牛网关可能改写 Host 或移除外部端口。
		hosts = append(hosts, strings.Split(request.Header.Get("X-Forwarded-Host"), ",")...)
		if request.Header.Get("Sec-Fetch-Site") == "same-origin" {
			return true
		}
	}
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if strings.EqualFold(parsed.Host, host) {
			return true
		}
		forwarded, err := url.Parse("//" + host)
		if isGateway && err == nil && forwarded.Hostname() != "" && forwarded.Port() == "" &&
			strings.EqualFold(parsed.Hostname(), forwarded.Hostname()) {
			return true
		}
	}
	return false
}

package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

//go:embed templates/gateway_status.html
var gatewayStatusPageTplContent string

var gatewayStatusPageTpl = template.Must(template.New("gateway_status").Parse(gatewayStatusPageTplContent))

const fnGatewayPrefix = basePath + "/fngateway"

var cookiePathRegex = regexp.MustCompile(`(?i)\bpath\s*=\s*/(;|$)`)
var rootHTMLAttribute = regexp.MustCompile(`(?i)\b(src|href)=(["'])/([^/])`)

//go:embed templates/gateway_base.js
var gatewayBaseScript string

//go:embed templates/gateway_viewport.css
var gatewayViewportStyle string

// InitFnGateway 注册飞牛网关直连代理路由
func InitFnGateway(base *gin.RouterGroup) {
	base.Any("/fngateway", handleFnGateway)
	base.Any("/fngateway/*action", handleFnGateway)
}

// handleFnGateway 飞牛网关核心反向代理处理器
func handleFnGateway(c *gin.Context) {
	// 规范化裸路径为带末尾斜杠的重定向路径
	if c.Request.URL.Path == fnGatewayPrefix {
		target := fnGatewayPrefix + "/"
		if c.Request.URL.RawQuery != "" {
			target += "?" + c.Request.URL.RawQuery
		}
		c.Redirect(http.StatusMovedPermanently, target)
		return
	}

	// 获取后端监听端口
	serverPort := GetConfig().GetServerPort()
	targetURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", serverPort))

	// 构建反向代理
	proxy := &httputil.ReverseProxy{
		Transport:     localTransport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(targetURL)
			pr.SetXForwarded()

			// 剥除网关前缀
			p := strings.TrimPrefix(pr.Out.URL.Path, fnGatewayPrefix)
			if !strings.HasPrefix(p, "/") {
				p = "/" + p
			}
			pr.Out.URL.Path = p
			if pr.Out.URL.RawPath != "" {
				rawP := strings.TrimPrefix(pr.Out.URL.RawPath, fnGatewayPrefix)
				if !strings.HasPrefix(rawP, "/") {
					rawP = "/" + rawP
				}
				pr.Out.URL.RawPath = rawP
			}

			// 改写回环请求头与安全上下文
			pr.Out.Host = fmt.Sprintf("127.0.0.1:%d", serverPort)
			pr.Out.Header.Set("Host", fmt.Sprintf("127.0.0.1:%d", serverPort))
			pr.Out.Header.Set("Origin", fmt.Sprintf("http://127.0.0.1:%d", serverPort))
			pr.Out.Header.Set("Sec-Fetch-Site", "same-origin")
			pr.Out.Header.Set("Accept-Encoding", "identity")
			pr.Out.Header.Del("If-None-Match")
			pr.Out.Header.Del("If-Modified-Since")

		},
		ModifyResponse: func(resp *http.Response) error {
			if err := filterDevServerDiscovery(resp); err != nil {
				return err
			}
			contentType := strings.ToLower(resp.Header.Get("Content-Type"))

			// 改写重定向地址
			if loc := resp.Header.Get("Location"); loc != "" {
				resp.Header.Set("Location", rewriteGatewayLocation(loc))
			}

			// 改写 Cookie 作用域路径
			if cookies := resp.Header.Values("Set-Cookie"); len(cookies) > 0 {
				resp.Header.Del("Set-Cookie")
				for _, ck := range cookies {
					resp.Header.Add("Set-Cookie", rewriteGatewayCookie(ck))
				}
			}

			// 优化流式响应标头
			if strings.HasPrefix(contentType, "text/event-stream") {
				resp.Header.Set("Cache-Control", "no-cache, no-transform")
				resp.Header.Set("X-Accel-Buffering", "no")
				resp.Header.Del("Content-Length")
				return nil
			}

			// 改写页面标签并注入核心补丁脚本
			if strings.Contains(contentType, "text/html") && resp.Body != nil {
				return rewriteWebUIHTML(resp, fnGatewayPrefix)
			}
			if strings.Contains(contentType, "manifest+json") && resp.Body != nil {
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					return err
				}
				body = rewriteGatewayManifest(body)
				resp.Body = io.NopCloser(bytes.NewReader(body))
				resp.ContentLength = int64(len(body))
				resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
				resp.Header.Set("Cache-Control", "no-store")
				resp.Header.Del("ETag")
				resp.Header.Del("Last-Modified")
			}
			if resp.StatusCode == http.StatusOK && resp.Body != nil && isGatewayAsset(resp.Request.URL.Path, contentType) {
				return rewriteGatewayAsset(resp)
			}

			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// 返回结构化 JSON
			if !strings.Contains(r.Header.Get("Accept"), "text/html") {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusBadGateway)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error":   "bad_gateway",
					"status":  state.Status(),
					"message": proxyErrMessage(),
					"detail":  err.Error(),
				})
				return
			}

			// HTML 页面请求渲染符合系统风格的状态指示页
			serveFnGatewayStatusPage(w, r, err)
		},
	}

	proxyWithAuth(proxy, fnGatewayPrefix).ServeHTTP(c.Writer, c.Request)
	c.Abort()
}

func isGatewayAsset(path, contentType string) bool {
	return path == "/sw.js" || (strings.HasPrefix(path, "/assets/") &&
		(strings.Contains(contentType, "javascript") || strings.Contains(contentType, "text/css")))
}

// rewriteGatewayAsset 只在网关响应中适配官方资源路径。
func rewriteGatewayAsset(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	for _, prefix := range []string{`"/assets/`, `'/assets/`, "`/assets/", "url(/assets/"} {
		body = bytes.ReplaceAll(body, []byte(prefix), []byte(strings.Replace(prefix, "/assets/", fnGatewayPrefix+"/assets/", 1)))
	}
	if resp.Request.URL.Path == "/sw.js" {
		body = append([]byte(gatewayWorkerScript+"\n"), body...)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Set("Cache-Control", "no-cache")
	resp.Header.Del("ETag")
	resp.Header.Del("Last-Modified")
	return nil
}

//go:embed templates/gateway_worker.js
var gatewayWorkerScript string

// rewriteGatewayLocation 重写重定向地址
func rewriteGatewayLocation(loc string) string {
	if loc == "" {
		return loc
	}
	// 剥除回环 Host，避免协议或端口漂移
	if u, err := url.Parse(loc); err == nil && u.Host != "" {
		if u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" {
			u.Scheme = ""
			u.Host = ""
			loc = u.String()
		}
	}
	if !strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, "//") {
		return loc
	}
	if loc == fnGatewayPrefix || strings.HasPrefix(loc, fnGatewayPrefix+"/") {
		return loc
	}
	return fnGatewayPrefix + loc
}

// rewriteGatewayCookie 重写 Cookie 作用域路径
func rewriteGatewayCookie(ck string) string {
	return cookiePathRegex.ReplaceAllString(ck, "Path="+fnGatewayPrefix+"/$1")
}

// rewriteFnGatewayHtml 改写页面标签并注入核心补丁
func rewriteFnGatewayHtml(body []byte) []byte {
	modified := rootHTMLAttribute.ReplaceAll(body, []byte("${1}=${2}"+fnGatewayPrefix+"/${3}"))
	modified = bytes.ReplaceAll(modified, []byte("const baseUrl = location.origin;"), []byte("const baseUrl = location.origin + "+strconv.Quote(fnGatewayPrefix)+";"))
	return injectHtmlHead(modified, []byte("<base href=\""+fnGatewayPrefix+"/\"><style>"+gatewayViewportStyle+"</style><script>"+gatewayBaseScript+"</script>"))
}

func rewriteGatewayManifest(body []byte) []byte {
	var manifest map[string]any
	if json.Unmarshal(body, &manifest) != nil {
		return body
	}
	for _, key := range []string{"id", "start_url", "scope"} {
		if value, ok := manifest[key].(string); ok {
			manifest[key] = rewriteGatewayLocation(value)
		}
	}
	for _, key := range []string{"icons", "shortcuts"} {
		if entries, ok := manifest[key].([]any); ok {
			rewriteGatewayManifestEntries(entries)
		}
	}
	result, err := json.Marshal(manifest)
	if err != nil {
		return body
	}
	return result
}

func rewriteGatewayManifestEntries(entries []any) {
	for _, entry := range entries {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		for _, field := range []string{"src", "url"} {
			if value, ok := item[field].(string); ok {
				item[field] = rewriteGatewayLocation(value)
			}
		}
		if icons, ok := item["icons"].([]any); ok {
			rewriteGatewayManifestEntries(icons)
		}
	}
}

// injectHtmlHead 将补丁脚本注入 HTML 的 head 头部
func injectHtmlHead(body, script []byte) []byte {
	lower := bytes.ToLower(body)
	idx := bytes.Index(lower, []byte("<head"))
	if idx != -1 {
		closeIdx := bytes.IndexByte(lower[idx:], '>')
		if closeIdx != -1 {
			insertPos := idx + closeIdx + 1
			var res bytes.Buffer
			res.Grow(len(body) + len(script))
			res.Write(body[:insertPos])
			res.Write(script)
			res.Write(body[insertPos:])
			return res.Bytes()
		}
	}
	var res bytes.Buffer
	res.Grow(len(body) + len(script))
	res.Write(script)
	res.Write(body)
	return res.Bytes()
}

// serveFnGatewayStatusPage 渲染符合系统设计规范的网关状态/错误页面
func serveFnGatewayStatusPage(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadGateway)

	currentStatus := state.Status()
	title := "无法连接到后端服务"
	desc := "后端服务暂未响应"
	badgeClass := "badge-err"
	badgeText := "异常"
	isStarting := false
	errDetail := ""
	if err != nil {
		errDetail = err.Error()
	}

	switch currentStatus {
	case StatusStarting:
		title = "服务启动中"
		desc = "正在准备运行环境与初始化依赖"
		badgeClass = "badge-starting"
		badgeText = "启动中"
		isStarting = true
	case StatusBuilding:
		title = "服务部署中"
		desc = "正在部署与启动核心运行环境"
		badgeClass = "badge-starting"
		badgeText = "部署中"
		isStarting = true
	case StatusSnapshotting:
		title = "快照维护中"
		desc = "正在执行快照备份或还原操作"
		badgeClass = "badge-starting"
		badgeText = "快照中"
		isStarting = true
	case StatusStopped:
		title = "服务未运行"
		desc = "OpenChamber 服务当前处于停止状态"
		badgeClass = "badge-stopped"
		badgeText = "已停止"
	}

	var detailsHTML template.HTML
	if errDetail != "" && !isStarting {
		reqInfo := ""
		if r != nil {
			reqInfo = fmt.Sprintf("请求路径: %s %s\n发生时间: %s\n", r.Method, r.URL.Path, time.Now().Format("2006-01-02 15:04:05"))
		}
		detailsHTML = template.HTML(fmt.Sprintf(`<div class="details-box">
      <pre>%s%s</pre>
    </div>`, template.HTMLEscapeString(reqInfo), template.HTMLEscapeString(errDetail)))
	}

	data := struct {
		Title       string
		Desc        string
		BadgeClass  string
		BadgeText   string
		IsStarting  bool
		DetailsHTML template.HTML
	}{
		Title:       title,
		Desc:        desc,
		BadgeClass:  badgeClass,
		BadgeText:   badgeText,
		IsStarting:  isStarting,
		DetailsHTML: detailsHTML,
	}

	if err := gatewayStatusPageTpl.Execute(w, data); err != nil {
		LogWarning("[网关] 输出状态页面失败: %s", err)
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const fnOSAssetsPath = "/_fnos/assets/"

func serveFnOSAssets(w http.ResponseWriter, r *http.Request, prefix string) {
	assets, err := fs.Sub(WebFS, "frontend/dist/assets")
	if err != nil {
		http.Error(w, "飞牛适配资源不可用", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.StripPrefix(prefix+fnOSAssetsPath, http.FileServer(http.FS(assets))).ServeHTTP(w, r)
}

// rewriteWebUIHTML 在宿主响应中加载飞牛文件管理适配。
func rewriteWebUIHTML(resp *http.Response, prefix string) error {
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	if prefix == fnGatewayPrefix {
		body = rewriteFnGatewayHtml(body)
		resp.Header.Del("Content-Security-Policy")
		resp.Header.Del("Content-Security-Policy-Report-Only")
	}
	script := fmt.Sprintf(`<script type="module" src="%s%sfnos-bridge.js"></script>`, prefix, fnOSAssetsPath)
	body = injectHtmlHead(body, []byte(script))
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Set("Cache-Control", "no-store")
	for _, header := range []string{"ETag", "Last-Modified"} {
		resp.Header.Del(header)
	}
	return nil
}

func writeFnOSRevealError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// serveFnOSReveal 校验目标并将文件的父目录交给浏览器内的飞牛 SDK。
func serveFnOSReveal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("X-OpenChamber-FnOS") != "1" {
		writeFnOSRevealError(w, http.StatusConflict, "飞牛文件管理适配尚未加载，请刷新页面后重试")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var payload struct {
		Path string `json:"path"`
	}
	if json.NewDecoder(r.Body).Decode(&payload) != nil || !filepath.IsAbs(payload.Path) || strings.ContainsRune(payload.Path, '\x00') {
		writeFnOSRevealError(w, http.StatusBadRequest, "目标路径必须是飞牛系统中的绝对路径")
		return
	}
	resolved, err := filepath.EvalSymlinks(payload.Path)
	var info os.FileInfo
	if err == nil {
		info, err = os.Stat(resolved)
	}
	if err != nil {
		status, message := http.StatusInternalServerError, "无法读取目标路径"
		if os.IsNotExist(err) {
			status, message = http.StatusNotFound, "目标路径不存在"
		} else if os.IsPermission(err) {
			status, message = http.StatusForbidden, "应用运行用户无权访问目标路径"
		}
		writeFnOSRevealError(w, status, message)
		return
	}
	directory := resolved
	if !info.IsDir() {
		directory = filepath.Dir(resolved)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "path": resolved, "directory": directory})
}

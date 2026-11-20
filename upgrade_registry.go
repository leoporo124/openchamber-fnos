package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const minWebVersion = "2.7.0"

type npmPackage struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Bin          json.RawMessage   `json:"bin"`
	Dependencies map[string]string `json:"dependencies"`
	Engines      map[string]string `json:"engines"`
}

type npmMetadata struct {
	DistTags map[string]string     `json:"dist-tags"`
	Versions map[string]npmPackage `json:"versions"`
}

type runtimeRelease struct {
	WebVersion  string
	CodeVersion string
	NodeEngine  string
}

func fetchNpmJSON(cfg Config, name, version string, target any) error {
	endpoint := strings.TrimRight(cfg.GetNpmRegistry(), "/") + "/" + url.PathEscape(name)
	if version != "" {
		endpoint += "/" + url.PathEscape(version)
	}
	ctx, cancel := context.WithTimeout(appContext, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("npm 元数据地址无效")
	}
	req.Header.Set("Accept", "application/vnd.npm.install-v1+json")
	req.Header.Set("User-Agent", "OpenChamber-fnOS")
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	if cfg.NetworkProxy != "" && cfg.usesOfficialRegistry() {
		proxyURL, err := url.Parse(cfg.NetworkProxy)
		if err != nil {
			return fmt.Errorf("网络代理地址无效")
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return fmt.Errorf("获取 %s 元数据失败，请检查依赖源与网络代理", name)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("获取 %s 元数据失败，HTTP %d", name, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(target); err != nil {
		return fmt.Errorf("%s 的 npm 元数据无效或超过大小限制", name)
	}
	return nil
}

func releaseFromPackage(pkg npmPackage) (runtimeRelease, error) {
	web, ok := parseSemver(pkg.Version)
	if !ok || web.Core[0] != 2 || CompareSemver(pkg.Version, minWebVersion) < 0 {
		return runtimeRelease{}, fmt.Errorf("当前宿主仅支持 OpenChamber 2.x，最低 %s", minWebVersion)
	}
	codeVersion := pkg.Dependencies["@opencode/client"]
	code, ok := parseSemver(codeVersion)
	if !ok || code.Core[0] != 2 || strings.TrimPrefix(strings.TrimSpace(codeVersion), "v") != codeVersion || codeVersion != pkg.Dependencies["@opencode/schema"] {
		return runtimeRelease{}, fmt.Errorf("OpenChamber %s 未声明可确认的 OpenCode client/schema 精确版本", pkg.Version)
	}
	if engine := pkg.Engines["opencode"]; engine != "" && !matchesVersionRequirement(codeVersion, engine) {
		return runtimeRelease{}, fmt.Errorf("配套 OpenCode %s 不符合上游运行要求 %s", codeVersion, engine)
	}
	if pkg.Engines["node"] == "" {
		return runtimeRelease{}, fmt.Errorf("OpenChamber %s 未声明 Node.js 运行要求", pkg.Version)
	}
	return runtimeRelease{WebVersion: pkg.Version, CodeVersion: codeVersion, NodeEngine: pkg.Engines["node"]}, nil
}

func resolveRuntimeRelease(cfg Config, version string) (runtimeRelease, error) {
	var pkg npmPackage
	if err := fetchNpmJSON(cfg, webPackageName, version, &pkg); err != nil {
		return runtimeRelease{}, err
	}
	if pkg.Name != webPackageName || pkg.Version != version {
		return runtimeRelease{}, fmt.Errorf("OpenChamber 发布包身份或版本不匹配")
	}
	release, err := releaseFromPackage(pkg)
	if err != nil {
		return release, err
	}
	var code npmPackage
	if err := fetchNpmJSON(cfg, openCodePackageName, release.CodeVersion, &code); err != nil {
		return release, err
	}
	if code.Name != openCodePackageName || code.Version != release.CodeVersion {
		return release, fmt.Errorf("配套 OpenCode CLI 未发布或版本不匹配")
	}
	return release, nil
}

func ListRuntimeVersions() (*RuntimeVersionsResult, error) {
	cfg := GetConfig()
	var metadata npmMetadata
	if err := fetchNpmJSON(cfg, webPackageName, "", &metadata); err != nil {
		return nil, err
	}
	current := currentRuntimeVersion()
	versions := make([]string, 0)
	for version, pkg := range metadata.Versions {
		if !isChannelVersion(version, cfg.GetUpdateChannel()) || (current != "" && CompareSemver(version, current) < 0) {
			continue
		}
		if pkg.Version == version {
			if _, err := releaseFromPackage(pkg); err == nil {
				versions = append(versions, version)
			}
		}
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("渠道 %s 没有满足要求且不低于当前版本的发布包", cfg.GetUpdateChannel())
	}
	sort.Slice(versions, func(i, j int) bool { return CompareSemver(versions[i], versions[j]) > 0 })
	recommended := metadata.DistTags[cfg.GetUpdateChannel()]
	if !containsVersion(versions, recommended) {
		recommended = versions[0]
	}
	return &RuntimeVersionsResult{CurrentVersion: current, Versions: versions, RecommendedVersion: recommended, MinVersion: minWebVersion}, nil
}

func containsVersion(versions []string, target string) bool {
	for _, version := range versions {
		if version == target {
			return true
		}
	}
	return false
}

func isChannelVersion(version, channel string) bool {
	parsed, ok := parseSemver(version)
	if !ok {
		return false
	}
	return len(parsed.Pre) == 0 || (channel == "canary" && parsed.Pre[0] == "canary")
}

func CheckUpdate() (*CheckUpdateResult, error) {
	cfg := GetConfig()
	var metadata struct {
		DistTags map[string]string `json:"dist-tags"`
	}
	if err := fetchNpmJSON(cfg, webPackageName, "", &metadata); err != nil {
		return nil, err
	}
	target := metadata.DistTags[cfg.GetUpdateChannel()]
	if target == "" || !isChannelVersion(target, cfg.GetUpdateChannel()) {
		return nil, fmt.Errorf("OpenChamber 渠道 %s 尚无有效发布版本", cfg.GetUpdateChannel())
	}
	current := currentRuntimeVersion()
	result := &CheckUpdateResult{CurrentVersion: current, RemoteVersion: target, Channel: cfg.GetUpdateChannel()}
	if current != "" && CompareSemver(target, current) < 0 {
		result.Message = fmt.Sprintf("渠道 %s 的版本 %s 低于当前 %s，按禁止降级规则保留当前版本", result.Channel, target, current)
		return result, nil
	}
	release, err := resolveRuntimeRelease(cfg, target)
	if err != nil {
		return nil, err
	}
	result.OpenCodeVersion = release.CodeVersion
	result.HasUpdate = current == "" || CompareSemver(target, current) > 0
	result.Message = "当前已是渠道 " + result.Channel + " 的最新版本"
	if result.HasUpdate {
		result.Message = fmt.Sprintf("发现 OpenChamber %s / OpenCode %s，可确认后更新", target, release.CodeVersion)
	}
	return result, nil
}

// matchesVersionRequirement 解析发布包使用的数值版本比较、交集及并集；未知语法不放行。
func matchesVersionRequirement(version, requirement string) bool {
	for _, branch := range strings.Split(requirement, "||") {
		parts := strings.Fields(strings.ReplaceAll(branch, ",", " "))
		matches := len(parts) > 0
		for _, part := range parts {
			matches = matches && matchesVersionComparator(version, part)
		}
		if matches {
			return true
		}
	}
	return false
}

func matchesVersionComparator(version, comparator string) bool {
	operator := "="
	for _, prefix := range []string{">=", "<=", ">", "<", "=", "^", "~"} {
		if strings.HasPrefix(comparator, prefix) {
			operator, comparator = prefix, strings.TrimPrefix(comparator, prefix)
			break
		}
	}
	wanted, ok := parseSemver(comparator)
	actual, valid := parseSemver(version)
	if !ok || !valid {
		return false
	}
	comparison := CompareSemver(version, comparator)
	switch operator {
	case ">=":
		return comparison >= 0
	case "<=":
		return comparison <= 0
	case ">":
		return comparison > 0
	case "<":
		return comparison < 0
	case "^":
		if comparison < 0 || actual.Core[0] != wanted.Core[0] {
			return false
		}
		if wanted.Core[0] == 0 && actual.Core[1] != wanted.Core[1] {
			return false
		}
		return wanted.Core[0] != 0 || wanted.Core[1] != 0 || actual.Core[2] == wanted.Core[2]
	case "~":
		return comparison >= 0 && actual.Core[0] == wanted.Core[0] && actual.Core[1] == wanted.Core[1]
	default:
		return comparison == 0
	}
}

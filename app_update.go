package main

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

const appReleasesURL = "https://github.com/leoobear/openchamber-fnos/releases/latest"

var appUpdate struct {
	sync.RWMutex
	version string
}

func appUpdateStatus() (string, bool) {
	appUpdate.RLock()
	defer appUpdate.RUnlock()
	return appUpdate.version, appUpdate.version != "" && CompareSemver(appUpdate.version, globalAppVer) > 0
}

func fetchAppUpdate() {
	ctx, cancel := context.WithTimeout(appContext, 8*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, appReleasesURL, nil)
	if err != nil {
		return
	}
	request.Header.Set("User-Agent", "OpenChamber-fnOS")
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	if proxy := GetConfig().NetworkProxy; proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 300 || response.StatusCode >= 400 {
		return
	}
	location, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		return
	}
	version := strings.TrimPrefix(path.Base(location.Path), "v")
	if _, ok := parseSemver(version); !ok {
		return
	}
	appUpdate.Lock()
	appUpdate.version = version
	appUpdate.Unlock()
	state.Poke()
}

func StartAppUpdateChecker() {
	go func() {
		fetchAppUpdate()
		ticker := time.NewTicker(3 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-appContext.Done():
				return
			case <-ticker.C:
				fetchAppUpdate()
			}
		}
	}()
}

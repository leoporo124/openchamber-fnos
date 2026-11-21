package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxDevServerDiscoveryBytes = 1 << 20

func isDevServerDiscovery(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.TrimSuffix(r.URL.Path, "/") == "/api/dev-servers"
}

func prepareDevServerDiscovery(r *http.Request) {
	if isDevServerDiscovery(r) {
		// 避免用上游缓存校验直接返回未过滤的旧列表。
		r.Header.Del("If-None-Match")
		r.Header.Del("If-Modified-Since")
	}
}

func isNFSService(port int, command string) bool {
	switch port {
	case 111, 2049, 20048: // rpcbind, NFS, and the commonly used mountd port.
		return true
	}
	switch strings.ToLower(command) {
	case "rpcbind", "portmap", "nfsd", "rpc.nfsd", "rpc.mountd", "rpc.statd", "rpc.rquotad", "lockd", "rpc.lockd":
		return true
	}
	return false
}

var nfsRPCPortCache struct {
	sync.Mutex
	checked time.Time
	ports   map[int]bool
}

func localNFSRPCPorts(ctx context.Context) map[int]bool {
	nfsRPCPortCache.Lock()
	defer nfsRPCPortCache.Unlock()
	if time.Since(nfsRPCPortCache.checked) < 5*time.Second {
		return nfsRPCPortCache.ports
	}
	// The kernel NFS lock manager can bind a dynamic port with no userspace PID.
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	var ports map[int]bool
	for _, path := range []string{"/usr/sbin/rpcinfo", "/usr/bin/rpcinfo"} {
		output, err := exec.CommandContext(ctx, path, "-p", "127.0.0.1").Output()
		if err == nil {
			ports = parseNFSRPCPorts(string(output))
			break
		}
	}
	if ctx.Err() == context.Canceled {
		return nil
	}
	nfsRPCPortCache.checked, nfsRPCPortCache.ports = time.Now(), ports
	return ports
}

func parseNFSRPCPorts(output string) map[int]bool {
	ports := make(map[int]bool)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[2] != "tcp" {
			continue
		}
		switch fields[0] {
		case "100000", "100003", "100005", "100011", "100021", "100024", "100227":
			// portmapper, nfs, mountd, rquotad, nlockmgr, status, nfs_acl
			if port, err := strconv.Atoi(fields[3]); err == nil && port > 0 && port <= 65535 {
				ports[port] = true
			}
		}
	}
	return ports
}

// filterDevServerDiscovery applies only to the upstream local discovery endpoint.
// It does not probe candidate ports or change manually entered URLs or Space servers.
func filterDevServerDiscovery(resp *http.Response) error {
	return filterDevServerDiscoveryWithRPCPorts(resp, localNFSRPCPorts)
}

func filterDevServerDiscoveryWithRPCPorts(resp *http.Response, rpcPorts func(context.Context) map[int]bool) error {
	if resp.Request == nil || !isDevServerDiscovery(resp.Request) || resp.StatusCode != http.StatusOK || resp.Body == nil {
		return nil
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	encoding := resp.Header.Get("Content-Encoding")
	if mediaType != "application/json" || (encoding != "" && !strings.EqualFold(encoding, "identity")) {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDevServerDiscoveryBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxDevServerDiscoveryBytes {
		return fmt.Errorf("upstream dev server discovery response exceeds %d bytes", maxDevServerDiscoveryBytes)
	}

	var payload map[string]json.RawMessage
	var servers []json.RawMessage
	if json.Unmarshal(body, &payload) == nil && json.Unmarshal(payload["servers"], &servers) == nil && len(servers) > 0 {
		registered := rpcPorts(resp.Request.Context())
		kept := servers[:0]
		for _, server := range servers {
			var entry struct {
				Port    int    `json:"port"`
				Command string `json:"command"`
			}
			if json.Unmarshal(server, &entry) == nil && (registered[entry.Port] || isNFSService(entry.Port, entry.Command)) {
				continue
			}
			kept = append(kept, server)
		}
		if len(kept) != len(servers) {
			payload["servers"], err = json.Marshal(kept)
			if err != nil {
				return err
			}
			body, err = json.Marshal(payload)
			if err != nil {
				return err
			}
		}
	}

	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Set("Cache-Control", "no-store")
	resp.Header.Del("ETag")
	resp.Header.Del("Last-Modified")
	return nil
}

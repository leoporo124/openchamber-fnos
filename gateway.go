package main

import (
	"fmt"
	"net"
	"net/http"
	"os/user"
	"strconv"
	"syscall"
)

type gatewayListener struct {
	*net.UnixListener
	trustedUIDs map[uint32]bool
}

func newGatewayListener(listener *net.UnixListener) *gatewayListener {
	uids := map[uint32]bool{0: true}
	for _, name := range []string{"nginx", "www-data"} {
		account, err := user.Lookup(name)
		if err != nil {
			continue
		}
		uid, err := strconv.ParseUint(account.Uid, 10, 32)
		if err == nil && uid > 0 && uid < 1000 {
			uids[uint32(uid)] = true
		}
	}
	return &gatewayListener{UnixListener: listener, trustedUIDs: uids}
}

// Accept 仅接受可信系统账户建立的网关连接。
func (listener *gatewayListener) Accept() (net.Conn, error) {
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return nil, err
		}
		uid, err := socketPeerUID(conn)
		if err == nil && listener.trustedUIDs[uid] {
			return conn, nil
		}
		conn.Close()
	}
}

func socketPeerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credentials *syscall.Ucred
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, peerErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if peerErr != nil {
		return 0, peerErr
	}
	if credentials == nil {
		return 0, fmt.Errorf("无法读取网关连接凭据")
	}
	return credentials.Uid, nil
}

func gatewayAdminID(request *http.Request) (string, bool) {
	address, ok := request.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || address.Network() != "unix" || request.Header.Get("X-Trim-Isadmin") != "true" {
		return "", false
	}
	uid, err := strconv.ParseUint(request.Header.Get("X-Trim-Userid"), 10, 32)
	if err != nil {
		return "", false
	}
	return strconv.FormatUint(uid, 10), true
}

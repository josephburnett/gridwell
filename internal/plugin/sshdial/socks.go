package sshdial

// A minimal SOCKS5 server (RFC 1928: no-auth, CONNECT only) whose upstream
// connections go through a caller-provided dialer — for the ssh plugin, the
// SSH client's tunnel, so a browser pointed at this proxy exits onto the
// REMOTE node's network. That is the whole NetworkContext story for a remote
// plugin's live url tiles: page traffic enters the tunnel here and browses
// with the remote's network context (issue #24).
//
// Deliberately tiny: no BIND, no UDP, no auth (it listens on loopback for
// this machine's own Chromium; the tunnel provides the security boundary).

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
)

// serveSOCKS5 accepts proxy clients on ln and dials upstream through dial.
// Runs until ln closes.
func serveSOCKS5(ln net.Listener, dial func(network, addr string) (net.Conn, error)) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go handleSOCKS5(c, dial)
	}
}

func handleSOCKS5(c net.Conn, dial func(network, addr string) (net.Conn, error)) {
	defer c.Close()
	addr, err := socks5Handshake(c)
	if err != nil {
		return
	}
	up, err := dial("tcp", addr)
	if err != nil {
		// 5 = connection refused (close enough for every upstream failure).
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
}

// socks5Handshake performs greeting + CONNECT parsing and returns the
// requested "host:port".
func socks5Handshake(c net.Conn) (string, error) {
	// Greeting: VER NMETHODS METHODS...
	head := make([]byte, 2)
	if _, err := io.ReadFull(c, head); err != nil {
		return "", err
	}
	if head[0] != 5 {
		return "", fmt.Errorf("socks: version %d", head[0])
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", err
	}
	if _, err := c.Write([]byte{5, 0}); err != nil { // no auth
		return "", err
	}
	// Request: VER CMD RSV ATYP ...
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		return "", err
	}
	if req[0] != 5 || req[1] != 1 { // CONNECT only
		_, _ = c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0}) // command not supported
		return "", fmt.Errorf("socks: unsupported cmd %d", req[1])
	}
	var host string
	switch req[3] {
	case 1: // IPv4
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 3: // domain
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return "", err
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = string(b)
	case 4: // IPv6
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	default:
		return "", fmt.Errorf("socks: bad atyp %d", req[3])
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb)))), nil
}

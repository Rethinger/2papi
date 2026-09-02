package proxylib

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- test SOCKS5 server (RFC 1928 subset: no-auth and user/pass auth) ----

type socks5Target struct {
	atyp byte
	addr string
}

type socks5TestServer struct {
	addr    string
	auth    map[string]string // user -> pass; nil = no auth
	mu      sync.Mutex
	targets []socks5Target
}

func startSOCKS5(t *testing.T, auth map[string]string) *socks5TestServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &socks5TestServer{addr: ln.Addr().String(), auth: auth}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *socks5TestServer) handle(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)

	// Greeting: VER NMETHODS METHODS...
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil || head[0] != 5 {
		return
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	if s.auth == nil {
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			return
		}
	} else {
		if _, err := conn.Write([]byte{5, 2}); err != nil {
			return
		}
		// Auth: VER ULEN UNAME PLEN PASSWD
		av := make([]byte, 2)
		if _, err := io.ReadFull(br, av); err != nil || av[0] != 1 {
			return
		}
		uname := make([]byte, int(av[1]))
		if _, err := io.ReadFull(br, uname); err != nil {
			return
		}
		plen := make([]byte, 1)
		if _, err := io.ReadFull(br, plen); err != nil {
			return
		}
		passwd := make([]byte, int(plen[0]))
		if _, err := io.ReadFull(br, passwd); err != nil {
			return
		}
		if s.auth[string(uname)] != string(passwd) {
			_, _ = conn.Write([]byte{1, 1})
			return
		}
		if _, err := conn.Write([]byte{1, 0}); err != nil {
			return
		}
	}

	// Request: VER CMD RSV ATYP ADDR PORT
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil || req[0] != 5 || req[1] != 1 {
		return
	}
	var addr string
	switch req[3] {
	case 1: // IPv4
		ip := make([]byte, 4)
		if _, err := io.ReadFull(br, ip); err != nil {
			return
		}
		addr = net.IP(ip).String()
	case 3: // domain
		hlen := make([]byte, 1)
		if _, err := io.ReadFull(br, hlen); err != nil {
			return
		}
		host := make([]byte, int(hlen[0]))
		if _, err := io.ReadFull(br, host); err != nil {
			return
		}
		addr = string(host)
	case 4: // IPv6
		ip := make([]byte, 16)
		if _, err := io.ReadFull(br, ip); err != nil {
			return
		}
		addr = net.IP(ip).String()
	default:
		return
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(br, port); err != nil {
		return
	}
	s.mu.Lock()
	s.targets = append(s.targets, socks5Target{atyp: req[3], addr: net.JoinHostPort(addr, fmt.Sprint(binary.BigEndian.Uint16(port)))})
	s.mu.Unlock()

	if _, err := conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	echo(conn, br)
}

func echo(conn net.Conn, br *bufio.Reader) {
	buf := make([]byte, 1024)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			if _, werr := conn.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// ---- test SOCKS4 server (RFC 1928 subset) ----

type socks4TestServer struct {
	addr    string
	mu      sync.Mutex
	targets []string
}

func startSOCKS4(t *testing.T) *socks4TestServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &socks4TestServer{addr: ln.Addr().String()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *socks4TestServer) handle(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	head := make([]byte, 8)
	if _, err := io.ReadFull(br, head); err != nil || head[0] != 4 || head[1] != 1 {
		return
	}
	port := binary.BigEndian.Uint16(head[2:4])
	ip := net.IP(head[4:8]).String()
	userid, err := br.ReadString(0)
	if err != nil {
		return
	}
	userid = strings.TrimRight(userid, "\x00")
	target := fmt.Sprintf("%s:%d", ip, port)
	if ip == "0.0.0.1" { // SOCKS4a: hostname follows
		host, err := br.ReadString(0)
		if err != nil {
			return
		}
		host = strings.TrimRight(host, "\x00")
		target = fmt.Sprintf("%s:%d", host, port)
	}
	s.mu.Lock()
	s.targets = append(s.targets, target)
	s.mu.Unlock()
	if _, err := conn.Write([]byte{0, 90, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	echo(conn, br)
}

// ---- tests ----

func dialEcho(t *testing.T, e Entry, target string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := e.DialContext(ctx, "tcp", target)
	if err != nil {
		t.Fatalf("DialContext(%s, %s): %v", e.String(), target, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo mismatch: %q", buf)
	}
}

// These exercise the SOCKS5 handshake (no-auth and user/pass), not DNS, so the
// target is "localhost": socks5 resolves the target locally, and a public
// hostname would make the handshake tests depend on external DNS. That matters
// most for the wrong-password case below, where a DNS failure would satisfy the
// "connection refused" assertion without the proxy ever rejecting the auth.
func TestDialSOCKS5NoAuth(t *testing.T) {
	srv := startSOCKS5(t, nil)
	e, err := ParseEntry("socks5://" + srv.addr)
	if err != nil {
		t.Fatal(err)
	}
	dialEcho(t, e, "localhost:443")
}

func TestDialSOCKS5Auth(t *testing.T) {
	srv := startSOCKS5(t, map[string]string{"user": "pass"})
	e, err := ParseEntry("socks5://user:pass@" + srv.addr)
	if err != nil {
		t.Fatal(err)
	}
	dialEcho(t, e, "localhost:443")

	bad, err := ParseEntry("socks5://user:wrong@" + srv.addr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if conn, err := bad.DialContext(ctx, "tcp", "localhost:443"); err == nil {
		conn.Close()
		t.Fatal("wrong credentials should fail")
	}
}

func TestDialSOCKS5LocalDNS(t *testing.T) {
	srv := startSOCKS5(t, nil)
	e, _ := ParseEntry("socks5://" + srv.addr) // socks5 = local DNS
	// Echo target on localhost: use a net.Listener for a real target port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		echo(conn, br)
	}()
	dialEcho(t, e, "localhost:"+portOfAddr(ln.Addr().String()))
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.targets) != 1 {
		t.Fatalf("proxy saw %d targets", len(srv.targets))
	}
	if srv.targets[0].atyp != 1 && srv.targets[0].atyp != 4 {
		t.Errorf("expected IP ATYP for local DNS, got %d (%s)", srv.targets[0].atyp, srv.targets[0].addr)
	}
	host, _, err := net.SplitHostPort(srv.targets[0].addr)
	if err != nil {
		t.Fatalf("proxy target %q: %v", srv.targets[0].addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Errorf("proxy resolved target as %q, want a loopback IP", srv.targets[0].addr)
	}
}

func TestDialSOCKS5hRemoteDNS(t *testing.T) {
	srv := startSOCKS5(t, nil)
	e, _ := ParseEntry("socks5h://" + srv.addr) // socks5h = remote DNS
	dialEcho(t, e, "remote-host.example:443")
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.targets) != 1 {
		t.Fatalf("proxy saw %d targets", len(srv.targets))
	}
	if srv.targets[0].atyp != 3 {
		t.Errorf("expected domain ATYP for remote DNS, got %d (%s)", srv.targets[0].atyp, srv.targets[0].addr)
	}
	if !strings.HasPrefix(srv.targets[0].addr, "remote-host.example:") {
		t.Errorf("proxy target = %s, want remote-host.example:443", srv.targets[0].addr)
	}
}

func TestDialSOCKS4(t *testing.T) {
	srv := startSOCKS4(t)
	e, _ := ParseEntry("socks4://" + srv.addr)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		echo(conn, bufio.NewReader(conn))
	}()
	dialEcho(t, e, "127.0.0.1:"+portOfAddr(ln.Addr().String()))
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.targets) != 1 || !strings.HasPrefix(srv.targets[0], "127.0.0.1:") {
		t.Errorf("proxy targets = %v, want 127.0.0.1:<port>", srv.targets)
	}
}

func TestDialSOCKS4LocalResolve(t *testing.T) {
	// Plain socks4 resolves hostnames locally (IPv4 only).
	srv := startSOCKS4(t)
	e, _ := ParseEntry("socks4://" + srv.addr)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		echo(conn, bufio.NewReader(conn))
	}()
	dialEcho(t, e, "localhost:"+portOfAddr(ln.Addr().String()))
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.targets) != 1 || !strings.HasPrefix(srv.targets[0], "127.0.0.1:") {
		t.Errorf("proxy targets = %v, want local resolution to 127.0.0.1", srv.targets)
	}
}

func TestDialSOCKS4aRemoteDNS(t *testing.T) {
	srv := startSOCKS4(t)
	e, _ := ParseEntry("socks4a://" + srv.addr)
	dialEcho(t, e, "remote-host.example:443")
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.targets) != 1 || !strings.HasPrefix(srv.targets[0], "remote-host.example:") {
		t.Errorf("proxy targets = %v, want remote-host.example:443", srv.targets)
	}
}

func portOfAddr(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}

// Package mdns publishes a `2papi.local` service via mDNS/Bonjour so the
// gateway resolves on the LAN as a real `.local` name without touching /etc/hosts.
// Pure Go (zeroconf) — works on macOS (Bonjour built-in), Linux (avahi not
// required), and Windows (best-effort). Publishing runs in the background.
package mdns

import (
	"fmt"
	"net"
	"strings"

	"github.com/grandcat/zeroconf"
)

const serviceType = "_2papi._tcp"

// Publisher announces the gateway over mDNS. Call Close() before shutdown.
type Publisher struct {
	server *zeroconf.Server
}

// NewPublisher starts mDNS advertising for hostname (without .local) on port.
// instance is a human-friendly name shown in browsers (Finder, Bonjour).
func NewPublisher(hostname string, port int, instance string) (*Publisher, error) {
	name := strings.TrimSuffix(hostname, ".local")
	if name == "" {
		name = "2papi"
	}
	if instance == "" {
		// mDNS labels cannot contain spaces; use hyphens.
		instance = "2papi-gateway"
	}
	instance = strings.ReplaceAll(instance, " ", "-")
	// nil ifaces = all interfaces (default). SelectIfaces is only for restricting.
	server, err := zeroconf.Register(
		instance,
		serviceType,
		"",
		port,
		[]string{"txtvers=1", "path=/dashboard/", "2papi=true"},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("mdns register: %w", err)
	}
	return &Publisher{server: server}, nil
}

// Close stops advertising.
func (p *Publisher) Close() {
	if p == nil || p.server == nil {
		return
	}
	p.server.Shutdown()
}

// localIP returns a non-loopback IPv4 address hint (informational).
func localIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.To4() == nil {
			continue
		}
		return ip.String(), nil
	}
	return "", fmt.Errorf("no non-loopback IPv4")
}

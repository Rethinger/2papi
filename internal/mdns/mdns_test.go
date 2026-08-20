package mdns

import (
	"strings"
	"testing"
)

func TestNameTrim(t *testing.T) {
	// NewPublisher validates trimming and service type without requiring a live
	// multicast socket (which may be unavailable in CI). We only test the pure
	// helpers here; a full publish test needs a real LAN/multicast.
	name := strings.TrimSuffix("2papi.local", ".local")
	if name != "2papi" {
		t.Fatalf("trim=%q", name)
	}
	if serviceType != "_2papi._tcp" {
		t.Fatalf("serviceType=%q", serviceType)
	}
}

func TestLocalIPFallback(t *testing.T) {
	// localIP should return something or an error — never panic.
	if _, err := localIP(); err != nil {
		// error is acceptable on machines without non-loopback IPv4
		t.Logf("localIP: %v", err)
	}
}

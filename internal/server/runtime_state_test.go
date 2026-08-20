package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/resilience"
)

func runtimeStateSnapshot(t *testing.T, alias string) *config.Snapshot {
	t.Helper()
	snapshot, err := config.Build(config.Config{
		Version:     1,
		Secret:      "secret",
		VirtualKeys: []config.VirtualKey{{Name: "limited", Key: "sk-limited", Models: []string{"*"}, RPM: 1}},
		Models:      []config.Model{{Alias: alias, UpstreamModel: "upstream", Accounts: []string{"account"}}},
		Accounts:    []config.Account{{Name: "account", BaseURL: "http://example.test", APIKey: "upstream", Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestAdoptPreservesRateLimitBuckets(t *testing.T) {
	gateway := NewRuntimeServer(runtimeStateSnapshot(t, "first"), resilience.New())
	before := gateway.Runtime()
	key := config.VirtualKey{Name: "limited", RPM: 1}
	if !before.Auth.Begin(key).Allowed || before.Auth.Begin(key).Allowed {
		t.Fatal("precondition: rate bucket was not exhausted")
	}

	gateway.Adopt(runtimeStateSnapshot(t, "second"))
	after := gateway.Runtime()
	if after.Auth.Begin(key).Allowed {
		t.Fatal("snapshot adoption reset the rate-limit bucket")
	}
}

func TestServerAlwaysCreatesRequestID(t *testing.T) {
	gateway := NewRuntimeServer(runtimeStateSnapshot(t, "model"), resilience.New())
	handler := gateway.Routes()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", "Bearer secret-must-not-cross-boundary")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	got := rec.Header().Get("X-Request-ID")
	if len(got) != 32 {
		t.Fatalf("request id length=%d want=32", len(got))
	}
	if got == "Bearer secret-must-not-cross-boundary" {
		t.Fatalf("gateway trusted attacker-controlled request id %q", got)
	}
}

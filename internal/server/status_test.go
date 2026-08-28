package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/resilience"
)

func statusApp(t *testing.T) *Server {
	t.Helper()
	snap, err := config.Build(config.Config{
		Version:     1,
		Secret:      "s",
		VirtualKeys: []config.VirtualKey{{Name: "k", Key: "sk-status-123456"}},
		Accounts:    []config.Account{{Name: "a", BaseURL: "http://x", APIKey: "ak", Enabled: true}},
		Models:      []config.Model{{Alias: "m", UpstreamModel: "u", Accounts: []string{"a"}}},
		MCPServers:  []config.McpServer{{Name: "tools", URL: "http://127.0.0.1:1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	gw := NewRuntimeServer(snap, resilience.New())
	gw.Version = "test-1.2.3"
	return gw
}

func TestStatusEndpointIsPublicAndSecretFree(t *testing.T) {
	gw := statusApp(t)
	res := httptest.NewRecorder()
	gw.Routes().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/status", nil))
	if res.Code != 200 {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var parsed map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("not json: %v", err)
	}
	if parsed["version"] != "test-1.2.3" {
		t.Fatalf("version missing: %v", parsed["version"])
	}
	if _, ok := parsed["uptime_seconds"].(float64); !ok {
		t.Fatalf("uptime_seconds missing: %v", parsed)
	}
	accts, ok := parsed["accounts"].(map[string]any)
	if !ok || accts["total"] != float64(1) || accts["enabled"] != float64(1) {
		t.Fatalf("account counters wrong: %v", parsed["accounts"])
	}
	if parsed["mcp_servers"] != float64(1) {
		t.Fatalf("mcp counter wrong: %v", parsed["mcp_servers"])
	}
	body := res.Body.String()
	for _, secret := range []string{"sk-status-123456", "\"secret\"", "api_key"} {
		if strings.Contains(body, secret) {
			t.Fatalf("status must not leak %q: %s", secret, body)
		}
	}

	// Unknown routes stay 404; method enforcement on POST.
	post := httptest.NewRecorder()
	gw.Routes().ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/status", nil))
	if post.Code != 405 {
		t.Fatalf("POST /status should be 405, got %d", post.Code)
	}
}

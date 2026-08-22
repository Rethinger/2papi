package mcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/policy"
	"github.com/Rethinger/2papi/internal/telemetry"
)

func testSnapshot(t *testing.T, keys []config.VirtualKey, servers []config.McpServer) *config.Snapshot {
	t.Helper()
	snap, err := config.Build(config.Config{
		Version:     1,
		Secret:      "s",
		VirtualKeys: keys,
		MCPServers:  servers,
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

type recordingRecorder struct {
	mu     sync.Mutex
	events []telemetry.Event
}

func (r *recordingRecorder) Record(e telemetry.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

const upstreamBody = `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`

func postMCP(server, key, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp/"+server, io.NopCloser(strings.NewReader(body)))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return req
}

func TestForwardsJSONRPCWithConfiguredHeaders(t *testing.T) {
	var gotPath, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	rec := &recordingRecorder{}
	gw := &Gateway{
		Snapshot: func() *config.Snapshot {
			return testSnapshot(t, []config.VirtualKey{{Name: "k1", Key: "sk-mcp-test-key"}}, []config.McpServer{
				{Name: "tools", URL: upstream.URL + "/mcp", Headers: map[string]string{"Authorization": "Bearer upstream-secret"}},
			})
		},
		Auth: policy.New(func() *config.Snapshot {
			return testSnapshot(t, []config.VirtualKey{{Name: "k1", Key: "sk-mcp-test-key"}}, []config.McpServer{
				{Name: "tools", URL: upstream.URL + "/mcp"},
			})
		}()),
		Telemetry:     rec,
		ConfigVersion: 7,
	}

	res := httptest.NewRecorder()
	gw.Serve(res, postMCP("tools", "sk-mcp-test-key", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), "tools")

	if res.Code != 200 || res.Body.String() != upstreamBody {
		t.Fatalf("upstream response must pass through verbatim: %d %q", res.Code, res.Body.String())
	}
	if gotPath != "/mcp" {
		t.Fatalf("configured path must be honored, got %q", gotPath)
	}
	if gotAuth != "Bearer upstream-secret" {
		t.Fatalf("configured headers must override client ones, got %q", gotAuth)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.events) != 1 || !rec.events[0].Success || rec.events[0].Endpoint != "/v1/mcp/tools" || rec.events[0].VirtualKey != "k1" {
		t.Fatalf("tool call must be recorded: %+v", rec.events)
	}
	if rec.events[0].ConfigVersion != 7 {
		t.Fatalf("config version must ride on the event: %+v", rec.events[0])
	}
}

func TestRejectsUnknownDisabledAndUnauthenticated(t *testing.T) {
	disabled := false
	snap := testSnapshot(t,
		[]config.VirtualKey{{Name: "k1", Key: "sk-mcp-test-key"}},
		[]config.McpServer{
			{Name: "live", URL: "http://127.0.0.1:1"},
			{Name: "off", URL: "http://127.0.0.1:1", Enabled: &disabled},
		})
	gw := &Gateway{Snapshot: func() *config.Snapshot { return snap }, Auth: policy.New(snap)}

	cases := []struct {
		name   string
		server string
		key    string
		want   int
	}{
		{"unknown server", "ghost", "sk-mcp-test-key", 404},
		{"disabled server", "off", "sk-mcp-test-key", 404},
		{"missing key", "live", "", 401},
		{"wrong key", "live", "sk-nope", 401},
	}
	for _, tc := range cases {
		res := httptest.NewRecorder()
		gw.Serve(res, postMCP(tc.server, tc.key, `{"jsonrpc":"2.0","method":"ping"}`), tc.server)
		if res.Code != tc.want {
			t.Fatalf("%s: want %d got %d", tc.name, tc.want, res.Code)
		}
	}
}

func TestBudgetAppliesToToolCalls(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	snap := testSnapshot(t,
		[]config.VirtualKey{{Name: "cheap", Key: "sk-cheap", BudgetUSD: 1}},
		[]config.McpServer{{Name: "tools", URL: upstream.URL}})
	auth := policy.New(snap)
	gw := &Gateway{Snapshot: func() *config.Snapshot { return snap }, Auth: auth}

	vk, ok := auth.Authenticate(postMCP("tools", "sk-cheap", `{}`))
	if !ok {
		t.Fatal("key must authenticate")
	}
	auth.Finalize(vk, 0, 2, true) // drain the $1 daily budget

	res := httptest.NewRecorder()
	gw.Serve(res, postMCP("tools", "sk-cheap", `{"jsonrpc":"2.0","method":"ping"}`), "tools")
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("exhausted budget must block tool calls with 429, got %d", res.Code)
	}
}

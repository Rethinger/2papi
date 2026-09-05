package proxy_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/proxy"
	"github.com/Rethinger/2papi/internal/resilience"
	"github.com/Rethinger/2papi/internal/router"
	"github.com/Rethinger/2papi/internal/server"
)

// squozeStand is the gateway wired to a recording upstream, with the squoze
// mode on and an optional body cap. It returns the server plus a pointer to the
// last body the upstream saw, which is what "passed through untouched" is
// asserted against — the response headers say what the gateway decided, the
// captured body says what the provider actually received.
func squozeStand(t *testing.T, maxBody int) (*httptest.Server, *[]byte) {
	t.Helper()

	var captured []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		captured = buf.Bytes()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-limits",
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}}},
		})
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Config{
		Version: 1,
		Secret:  "test-secret-squoze-limits",
		Optimization: config.Optimization{
			Squoze:             true,
			SquozeMaxBodyBytes: maxBody,
		},
		VirtualKeys: []config.VirtualKey{{Name: "vk-test", Key: "sk-test", Models: []string{"gpt-4o"}}},
		Models:      []config.Model{{Alias: "gpt-4o", UpstreamModel: "gpt-4o", Accounts: []string{"acc1"}}},
		Accounts: []config.Account{{
			Name: "acc1", Adapter: "openai", BaseURL: upstream.URL, APIKey: "dummy-key", Enabled: true,
		}},
	}
	snap, err := config.Build(cfg)
	if err != nil {
		t.Fatalf("config.Build: %v", err)
	}
	st := resilience.New()
	srv := httptest.NewServer(server.New(snap, proxy.New(snap, st, router.New(snap, st))).Routes())
	t.Cleanup(srv.Close)
	return srv, &captured
}

// squozeBody is a chat request whose tool message is a liftable table, so squoze
// has real work to do when it is allowed to look. That matters for these tests:
// a body squoze would ignore anyway could not tell "the cap skipped it" from
// "there was nothing to do".
func squozeBody(t *testing.T, rows int) []byte {
	t.Helper()
	type rec struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	var records []rec
	for i := 0; i < rows; i++ {
		records = append(records, rec{ID: 100 + i, Name: "worker-" + strconv.Itoa(i), Status: "HEALTHY"})
	}
	table, _ := json.MarshalIndent(records, "", "  ")
	body, err := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{"role": "user", "content": "check cluster status"},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": string(table)},
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return body
}

func squozePost(t *testing.T, srv *httptest.Server, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d", resp.StatusCode)
	}
	return resp
}

// TestSquozeConfigCapSkipsAndSaysSo covers the config half of FR-1: over the
// cap, the provider gets the original bytes and the response says why.
func TestSquozeConfigCapSkipsAndSaysSo(t *testing.T) {
	body := squozeBody(t, 25)
	srv, captured := squozeStand(t, len(body)-1) // one byte under this request

	resp := squozePost(t, srv, body, nil)

	if got := resp.Header.Get("X-Gateway-Squoze-Skip"); got != "body_too_large" {
		t.Fatalf("X-Gateway-Squoze-Skip = %q, want body_too_large", got)
	}
	if got := resp.Header.Get("X-Gateway-Squoze"); got != "false" {
		t.Fatalf("X-Gateway-Squoze = %q, want false on a skip", got)
	}
	if !bytes.Equal(*captured, body) {
		t.Fatalf("upstream body was modified on a skipped request: %d bytes in, %d out",
			len(body), len(*captured))
	}
}

// TestSquozeUnderCapStillCompresses is the other side of the same threshold:
// the cap must be a bound and not a mode. One byte of headroom and the same
// request compresses as it always did.
func TestSquozeUnderCapStillCompresses(t *testing.T) {
	body := squozeBody(t, 25)
	srv, captured := squozeStand(t, len(body)+1)

	resp := squozePost(t, srv, body, nil)

	if got := resp.Header.Get("X-Gateway-Squoze-Skip"); got != "" {
		t.Fatalf("X-Gateway-Squoze-Skip = %q, want empty under the cap", got)
	}
	if got := resp.Header.Get("X-Gateway-Squoze"); got != "true" {
		t.Fatalf("X-Gateway-Squoze = %q, want true under the cap", got)
	}
	if len(*captured) >= len(body) {
		t.Fatalf("expected the upstream body to shrink: %d in, %d out", len(body), len(*captured))
	}
}

// TestSquozeHeaderTightensButNeverLoosens is FR-1's request-scoped half. The
// header may ask for less work than the config allows; asking for more is
// ignored, because a guard a caller can switch off is not a guard.
func TestSquozeHeaderTightensButNeverLoosens(t *testing.T) {
	body := squozeBody(t, 25)

	t.Run("tightens", func(t *testing.T) {
		srv, captured := squozeStand(t, 0) // unbounded by config
		resp := squozePost(t, srv, body, map[string]string{
			"X-Gateway-Squoze-Max-Body": strconv.Itoa(len(body) - 1),
		})
		if got := resp.Header.Get("X-Gateway-Squoze-Skip"); got != "body_too_large" {
			t.Fatalf("X-Gateway-Squoze-Skip = %q, want body_too_large", got)
		}
		if !bytes.Equal(*captured, body) {
			t.Fatal("upstream body was modified although the header skipped the request")
		}
	})

	t.Run("cannot loosen", func(t *testing.T) {
		srv, captured := squozeStand(t, len(body)-1) // config says skip
		resp := squozePost(t, srv, body, map[string]string{
			"X-Gateway-Squoze-Max-Body": strconv.Itoa(len(body) * 100),
		})
		if got := resp.Header.Get("X-Gateway-Squoze-Skip"); got != "body_too_large" {
			t.Fatalf("header loosened the config bound: skip header = %q", got)
		}
		if !bytes.Equal(*captured, body) {
			t.Fatal("header loosened the config bound: upstream body was compressed")
		}
	})

	t.Run("garbage is ignored", func(t *testing.T) {
		srv, captured := squozeStand(t, 0)
		resp := squozePost(t, srv, body, map[string]string{
			"X-Gateway-Squoze-Max-Body": "not-a-number",
		})
		if got := resp.Header.Get("X-Gateway-Squoze-Skip"); got != "" {
			t.Fatalf("malformed header changed behaviour: skip header = %q", got)
		}
		if len(*captured) >= len(body) {
			t.Fatalf("malformed header suppressed compression: %d in, %d out", len(body), len(*captured))
		}
	})
}

// TestSquozeEngineIsPooledAcrossRequests is the cache-stability property the
// engine pool exists for, tested through the public path rather than by
// comparing pointers: the decision memo is per-engine, so a second identical
// request can only report a memo hit if both requests reached the same engine.
func TestSquozeEngineIsPooledAcrossRequests(t *testing.T) {
	body := squozeBody(t, 25)
	srv, _ := squozeStand(t, 0)

	first := squozePost(t, srv, body, nil)
	second := squozePost(t, srv, body, nil)

	if got := first.Header.Get("X-Gateway-Squoze"); got != "true" {
		t.Fatalf("first request did not compress: X-Gateway-Squoze = %q", got)
	}
	hits := second.Header.Get("X-Gateway-Squoze-Memo-Hits")
	if hits == "" || hits == "0" {
		t.Fatalf("second identical request reported no memo hit (%q): the engine was not reused", hits)
	}
}

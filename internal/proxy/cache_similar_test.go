package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/proxy"
	"github.com/Rethinger/2papi/internal/resilience"
	"github.com/Rethinger/2papi/internal/router"
	"github.com/Rethinger/2papi/internal/server"
)

// Two wordings of one question, ten and eleven significant words, so the gate
// (>= 8) is clear on both and it is the overlap that decides: 10 of 11 shared,
// Jaccard 0.909. The third question shares nothing.
const (
	simAsk     = "what is the capital city of france and its total resident population"
	simAskMore = "what is the capital city of france and its total resident population today"
	simOther   = "explain how quantum entanglement affects modern distributed computing systems please"
)

func simBody(alias, ask string) string {
	return `{"model":"` + alias + `","stream":false,"messages":[{"role":"user","content":"` + ask + `"}]}`
}

// TSK-406 end to end: a near-duplicate question is answered from the cache with
// HIT-SIMILAR and the overlap that earned it, the upstream is not called, and an
// exact-mode model next to it refuses the same near-duplicate (AC-1.1).
func TestSimilarCacheServesNearDuplicate(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// A distinct body per call, so the assertion can name which answer came back.
		_, _ = io.WriteString(w, `{"id":"chat-`+strconv.Itoa(int(n))+`","object":"chat.completion","model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"answer `+strconv.Itoa(int(n))+`"}}]}`)
	}))
	defer up.Close()

	threshold := 0.8
	snap, err := config.Build(config.Config{
		Version: 1,
		Secret:  "s",
		VirtualKeys: []config.VirtualKey{
			{Name: "vk", Key: "sk", Models: []string{"sim", "ex"}, RPM: 100},
		},
		Models: []config.Model{
			{Alias: "sim", UpstreamModel: "up", Accounts: []string{"acct-0"}, Cache: "similar", CacheSimilarThreshold: &threshold},
			{Alias: "ex", UpstreamModel: "up", Accounts: []string{"acct-0"}, Cache: "exact"},
		},
		Accounts: []config.Account{
			{Name: "acct-0", BaseURL: up.URL, APIKey: "k", Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	st := resilience.New()
	rt := router.New(snap, st)
	px := proxy.New(snap, st, rt)
	ts := httptest.NewServer(server.New(snap, px).Routes())
	defer ts.Close()

	post := func(t *testing.T, raw string, extra map[string]string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(raw))
		req.Header.Set("Authorization", "Bearer sk")
		req.Header.Set("Content-Type", "application/json")
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("status=%d", resp.StatusCode)
		}
		return resp
	}
	// 1. Cold: nothing cached, upstream answers.
	first := post(t, simBody("sim", simAsk), nil)
	defer first.Body.Close()
	if got := first.Header.Get("X-Gateway-Cache"); got != "MISS" {
		t.Fatalf("first request: X-Gateway-Cache=%q, want MISS", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("first request: upstream calls=%d, want 1", calls.Load())
	}

	// 2. One extra word: served from the cache as a similar hit, with the score.
	near := post(t, simBody("sim", simAskMore), nil)
	defer near.Body.Close()
	if got := near.Header.Get("X-Gateway-Cache"); got != "HIT-SIMILAR" {
		t.Fatalf("near-duplicate: X-Gateway-Cache=%q, want HIT-SIMILAR", got)
	}
	score, err := strconv.ParseFloat(near.Header.Get("X-Gateway-Cache-Score"), 64)
	if err != nil {
		t.Fatalf("near-duplicate: unparseable score %q: %v", near.Header.Get("X-Gateway-Cache-Score"), err)
	}
	if score <= 0 || score > 1 {
		t.Fatalf("near-duplicate: score %v outside (0, 1]", score)
	}
	if calls.Load() != 1 {
		t.Fatalf("near-duplicate reached upstream: calls=%d, want 1", calls.Load())
	}
	// The answer to the FIRST question is what came back -- that is the whole
	// trade of this mode, and the test states it rather than implying it.
	nearBody, _ := io.ReadAll(near.Body)
	if !strings.Contains(string(nearBody), "answer 1") {
		t.Fatalf("near-duplicate served the wrong body: %s", nearBody)
	}

	// 3. A question that shares no words is a miss, not a loose match.
	other := post(t, simBody("sim", simOther), nil)
	defer other.Body.Close()
	if got := other.Header.Get("X-Gateway-Cache"); got != "MISS" {
		t.Fatalf("disjoint question: X-Gateway-Cache=%q, want MISS", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("disjoint question: upstream calls=%d, want 2", calls.Load())
	}

	// 4. FR-6: the opt-out header disables the similar path too.
	off := post(t, simBody("sim", simAskMore), map[string]string{"X-Gateway-Cache": "false"})
	defer off.Body.Close()
	if got := off.Header.Get("X-Gateway-Cache"); got != "" {
		t.Fatalf("cache:false still consulted the cache: %q", got)
	}
	if calls.Load() != 3 {
		t.Fatalf("cache:false must reach upstream: calls=%d, want 3", calls.Load())
	}

	// 5. AC-1.1: exact mode is untouched. Its own namespace starts cold, and the
	// near-duplicate that HIT-SIMILAR on `sim` is a plain miss here.
	exFirst := post(t, simBody("ex", simAsk), nil)
	defer exFirst.Body.Close()
	if got := exFirst.Header.Get("X-Gateway-Cache"); got != "MISS" {
		t.Fatalf("exact model, first request: %q, want MISS", got)
	}
	exNear := post(t, simBody("ex", simAskMore), nil)
	defer exNear.Body.Close()
	if got := exNear.Header.Get("X-Gateway-Cache"); got != "MISS" {
		t.Fatalf("exact model served a near-duplicate: %q, want MISS", got)
	}
	exSame := post(t, simBody("ex", simAsk), nil)
	defer exSame.Body.Close()
	if got := exSame.Header.Get("X-Gateway-Cache"); got != "HIT" {
		t.Fatalf("exact model, repeated request: %q, want HIT", got)
	}
	if calls.Load() != 5 {
		t.Fatalf("exact model: upstream calls=%d, want 5", calls.Load())
	}
}

// AC-3.1 on the wire: the single-turn gate is not advice the proxy may skip. A
// tool-carrying request and a mid-conversation one both miss even though a
// near-duplicate answer is sitting in the cache -- and a client cannot reach the
// mode with a header on a model that did not opt in.
func TestSimilarCacheRefusesAgenticAndHeaderOnly(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c","object":"chat.completion","model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer up.Close()

	snap, err := config.Build(config.Config{
		Version: 1,
		Secret:  "s",
		VirtualKeys: []config.VirtualKey{
			{Name: "vk", Key: "sk", Models: []string{"sim", "plain"}, RPM: 100},
		},
		Models: []config.Model{
			{Alias: "sim", UpstreamModel: "up", Accounts: []string{"acct-0"}, Cache: "similar"},
			{Alias: "plain", UpstreamModel: "up", Accounts: []string{"acct-0"}},
		},
		Accounts: []config.Account{
			{Name: "acct-0", BaseURL: up.URL, APIKey: "k", Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	st := resilience.New()
	rt := router.New(snap, st)
	px := proxy.New(snap, st, rt)
	ts := httptest.NewServer(server.New(snap, px).Routes())
	defer ts.Close()

	post := func(t *testing.T, raw string, extra map[string]string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(raw))
		req.Header.Set("Authorization", "Bearer sk")
		req.Header.Set("Content-Type", "application/json")
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("status=%d", resp.StatusCode)
		}
		return resp
	}

	// Seed the cache with the plain question on the similar model.
	seed := post(t, simBody("sim", simAsk), nil)
	defer seed.Body.Close()
	if got := seed.Header.Get("X-Gateway-Cache"); got != "MISS" {
		t.Fatalf("seed: %q, want MISS", got)
	}

	// Default threshold (no cache_similar_threshold set) is 0.95, and the +today
	// wording scores 0.909 -- so it must NOT be served. This is the default the
	// docs promise, checked instead of described.
	underDefault := post(t, simBody("sim", simAskMore), nil)
	defer underDefault.Body.Close()
	if got := underDefault.Header.Get("X-Gateway-Cache"); got != "MISS" {
		t.Fatalf("0.909 overlap under the 0.95 default: %q, want MISS", got)
	}

	// Tools present: the answer depends on tool results, not on the wording.
	withTools := `{"model":"sim","stream":false,"tools":[{"type":"function","function":{"name":"f"}}],"messages":[{"role":"user","content":"` + simAsk + `"}]}`
	toolResp := post(t, withTools, nil)
	defer toolResp.Body.Close()
	if got := toolResp.Header.Get("X-Gateway-Cache"); got == "HIT-SIMILAR" {
		t.Fatal("a tool-carrying request was served from the similar cache")
	}

	// Mid-conversation: more than two messages besides system.
	midConv := `{"model":"sim","stream":false,"messages":[{"role":"user","content":"` + simAsk + `"},{"role":"assistant","content":"paris"},{"role":"user","content":"continue"}]}`
	midResp := post(t, midConv, nil)
	defer midResp.Body.Close()
	if got := midResp.Header.Get("X-Gateway-Cache"); got == "HIT-SIMILAR" {
		t.Fatal("an agent-loop turn was served from the similar cache")
	}

	// A model that never opted in: X-Gateway-Cache: true buys the exact cache
	// only, however similar the next question reads.
	hdr := map[string]string{"X-Gateway-Cache": "true"}
	plainSeed := post(t, simBody("plain", simAsk), hdr)
	defer plainSeed.Body.Close()
	plainNear := post(t, simBody("plain", simAskMore), hdr)
	defer plainNear.Body.Close()
	if got := plainNear.Header.Get("X-Gateway-Cache"); got != "MISS" {
		t.Fatalf("header-only caching served a near-duplicate: %q, want MISS", got)
	}
}

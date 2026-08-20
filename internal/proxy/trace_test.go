package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/proxy"
	"github.com/Rethinger/2papi/internal/resilience"
	"github.com/Rethinger/2papi/internal/router"
	"github.com/Rethinger/2papi/internal/server"
	"github.com/Rethinger/2papi/internal/telemetry"
)

type captureRecorder struct{ events chan telemetry.Event }

func (c captureRecorder) Record(event telemetry.Event) { c.events <- event }

func TestTraceRecordsFallbackWithoutRequestContent(t *testing.T) {
	up1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "limited", http.StatusTooManyRequests)
	}))
	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"response","model":"up","usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`))
	}))
	defer up1.Close()
	defer up2.Close()

	snap, err := config.Build(config.Config{
		Version:     1,
		Secret:      "s",
		VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"public"}, RPM: 10}},
		Models:      []config.Model{{Alias: "public", UpstreamModel: "up", Accounts: []string{"primary", "secondary"}}},
		Accounts: []config.Account{
			{Name: "primary", BaseURL: up1.URL, APIKey: "primary-secret", Enabled: true, Priority: 1, Weight: 1, MaxConcurrency: 10},
			{Name: "secondary", BaseURL: up2.URL, APIKey: "secondary-secret", Enabled: true, Priority: 2, Weight: 1, MaxConcurrency: 10},
		},
		Routing:    config.Routing{Strategy: "priority", MaxAttempts: 2},
		Resilience: config.Resilience{CircuitFailures: 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	recorder := captureRecorder{events: make(chan telemetry.Event, 1)}
	state := resilience.New()
	px := proxy.New(snap, state, router.New(snap, state))
	px.Telemetry = recorder
	gateway := server.New(snap, px)
	gateway.SetConfigVersion(17)
	ts := httptest.NewServer(gateway.Routes())
	defer ts.Close()

	requestBody := `{"model":"public","user":"private-user","metadata":{"gateway_session":"private-session"},"messages":[{"role":"user","content":"top-secret-prompt"}]}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/v1/chat/completions", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer sk")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	select {
	case event := <-recorder.events:
		if event.RequestID == "" || resp.Header.Get("X-Request-ID") != event.RequestID {
			t.Fatalf("request id header=%q event=%q", resp.Header.Get("X-Request-ID"), event.RequestID)
		}
		if event.Endpoint != "/v1/chat/completions" || event.PublicModel != "public" || event.UpstreamModel != "up" || event.VirtualKey != "vk" {
			t.Fatalf("unexpected event identity: %+v", event)
		}
		if event.ConfigVersion != 17 || !event.Success || event.FinalStatus != http.StatusOK || event.InputTokens != 3 || event.OutputTokens != 5 || event.TotalTokens != 8 {
			t.Fatalf("unexpected event result: %+v", event)
		}
		if len(event.Attempts) != 2 || event.Attempts[0].Account != "primary" || event.Attempts[0].Outcome != "rate_limited" || event.Attempts[1].Account != "secondary" || event.Attempts[1].Outcome != "success" {
			t.Fatalf("unexpected attempts: %+v", event.Attempts)
		}
		serialized := event.String()
		for _, forbidden := range []string{"top-secret-prompt", "private-user", "private-session", "primary-secret", "secondary-secret", "Bearer sk"} {
			if strings.Contains(serialized, forbidden) {
				t.Fatalf("trace leaked %q: %s", forbidden, serialized)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for trace event")
	}
}

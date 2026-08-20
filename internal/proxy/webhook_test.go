package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/resilience"
	"github.com/Rethinger/2papi/internal/router"
)

func TestNotifySendsSignedWebhook(t *testing.T) {
	var mu sync.Mutex
	var received []byte
	var event, signature string
	delivered := make(chan struct{}, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = body
		event = r.Header.Get("X-Gateway-Event")
		signature = r.Header.Get("X-Gateway-Signature")
		mu.Unlock()
		select {
		case delivered <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	snap, err := config.Build(config.Config{
		Version: 1,
		Secret:  "s",
		VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 100}},
		Models:   []config.Model{{Alias: "m", UpstreamModel: "up", Accounts: []string{"acct-0"}}},
		Accounts: []config.Account{{Name: "acct-0", BaseURL: "http://127.0.0.1:1", APIKey: "k", Enabled: true}},
		Webhook:  config.Webhook{Enabled: true, URL: ts.URL, Secret: "wh-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	px := New(snap, resilience.New(), router.New(snap, resilience.New()))
	px.notify("account_lockout", map[string]any{"account": "acct-0", "alias": "m"})

	select {
	case <-delivered:
	case <-time.After(3 * time.Second):
		t.Fatal("webhook not delivered")
	}
	mu.Lock()
	defer mu.Unlock()
	if event != "account_lockout" {
		t.Fatalf("event=%q", event)
	}
	mac := hmac.New(sha256.New, []byte("wh-secret"))
	mac.Write(received)
	if signature != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("signature mismatch: %q", signature)
	}
}

func TestNotifySkipsWhenDisabled(t *testing.T) {
	hit := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	snap, err := config.Build(config.Config{
		Version: 1, Secret: "s",
		VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 100}},
		Models:      []config.Model{{Alias: "m", UpstreamModel: "up", Accounts: []string{"acct-0"}}},
		Accounts:    []config.Account{{Name: "acct-0", BaseURL: "http://127.0.0.1:1", APIKey: "k", Enabled: true}},
		Webhook:     config.Webhook{Enabled: false, URL: ts.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	px := New(snap, resilience.New(), router.New(snap, resilience.New()))
	px.notify("budget_exceeded", map[string]any{})
	time.Sleep(100 * time.Millisecond)
	if hit {
		t.Fatal("webhook sent while disabled")
	}
}

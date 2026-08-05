package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/controlplane"
	"github.com/1jehuang/2papi/internal/resilience"
	"github.com/1jehuang/2papi/internal/server"
)

func TestAdoptOnceSkipsUnchangedSnapshotAck(t *testing.T) {
	cfg := config.Config{
		Version:     1,
		Secret:      "secret",
		VirtualKeys: []config.VirtualKey{{Name: "dev", Key: "sk-dev", Models: []string{"gpt-dev"}, RPM: 60}},
		Models:      []config.Model{{Alias: "gpt-dev", UpstreamModel: "upstream", Accounts: []string{"primary"}}},
		Accounts:    []config.Account{{Name: "primary", BaseURL: "http://upstream", APIKey: "upstream-key", Enabled: true}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	checksum := hex.EncodeToString(sum[:])
	var acknowledgements atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/internal/v1/snapshot":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": 7, "checksum": checksum, "snapshot": json.RawMessage(raw)})
		case "/api/internal/v1/gateway-acks":
			acknowledgements.Add(1)
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	fallback, err := config.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	gateway := server.NewRuntimeServer(fallback, resilience.New())
	client := controlplane.New(upstream.URL, "internal-token", "gateway-test")

	version, gotChecksum := adoptOnce(context.Background(), client, gateway, 0, "")
	if version != 7 || gotChecksum != checksum {
		t.Fatalf("adopted version/checksum = %d/%s", version, gotChecksum)
	}
	version, gotChecksum = adoptOnce(context.Background(), client, gateway, version, gotChecksum)
	if acknowledgements.Load() != 1 {
		t.Fatalf("acknowledgements = %d, want 1", acknowledgements.Load())
	}
}

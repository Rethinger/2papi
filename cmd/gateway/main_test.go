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

func TestAdoptOnceSkipsUnchangedSnapshotAckButSendsHeartbeat(t *testing.T) {
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
	var heartbeats atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/internal/v1/gateway-heartbeats":
			heartbeats.Add(1)
			var body struct {
				GatewayID        string `json:"gateway_id"`
				SupportedSchemas []int  `json:"supported_schemas"`
				EnvelopeVersion  int    `json:"envelope_version"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.GatewayID != "gateway-test" || body.EnvelopeVersion != 2 || len(body.SupportedSchemas) != 2 {
				t.Fatalf("heartbeat body=%+v", body)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/internal/v1/snapshot":
			_ = json.NewEncoder(w).Encode(map[string]any{"config_version": 7, "schema_version": 2, "config_checksum": "config-digest", "credential_digest": "cred", "runtime_checksum": checksum, "snapshot": json.RawMessage(raw)})
		case "/api/internal/v1/gateway-acks":
			acknowledgements.Add(1)
			var ack controlplane.Ack
			if err := json.NewDecoder(r.Body).Decode(&ack); err != nil {
				t.Fatal(err)
			}
			if ack.ConfigVersion != 7 || ack.SchemaVersion != 2 || ack.ConfigChecksum != "config-digest" || ack.CredentialDigest != "cred" || ack.RuntimeChecksum != checksum || ack.EnvelopeVersion != 2 || !ack.Success {
				t.Fatalf("ack=%+v", ack)
			}
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

	identity := adoptOnce(context.Background(), client, gateway, controlplane.SnapshotIdentity{})
	want := controlplane.SnapshotIdentity{ConfigVersion: 7, SchemaVersion: 2, ConfigChecksum: "config-digest", CredentialDigest: "cred", RuntimeChecksum: checksum, EnvelopeVersion: 2}
	if !identity.Equal(want) {
		t.Fatalf("identity=%+v want %+v", identity, want)
	}
	identity = adoptOnce(context.Background(), client, gateway, identity)
	if !identity.Equal(want) {
		t.Fatalf("second identity=%+v want %+v", identity, want)
	}
	if acknowledgements.Load() != 1 {
		t.Fatalf("acknowledgements = %d, want 1", acknowledgements.Load())
	}
	if heartbeats.Load() != 2 {
		t.Fatalf("heartbeats = %d, want 2", heartbeats.Load())
	}
}

func TestAdoptOnceAdoptsWhenIdentityFieldChanges(t *testing.T) {
	cfg := config.Config{Version: 1, Secret: "secret", VirtualKeys: []config.VirtualKey{{Name: "dev", Key: "sk-dev", Models: []string{"gpt-dev"}, RPM: 60}}, Models: []config.Model{{Alias: "gpt-dev", UpstreamModel: "upstream", Accounts: []string{"primary"}}}, Accounts: []config.Account{{Name: "primary", BaseURL: "http://upstream", APIKey: "upstream-key", Enabled: true}}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	checksum := hex.EncodeToString(sum[:])
	var acknowledgements atomic.Int32
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/internal/v1/gateway-heartbeats":
			w.WriteHeader(http.StatusNoContent)
		case "/api/internal/v1/snapshot":
			cred := "cred-a"
			if requests.Add(1) > 1 {
				cred = "cred-b"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"version": 2, "config_version": 7, "schema_version": 2, "config_checksum": checksum, "credential_digest": cred, "runtime_checksum": checksum, "snapshot": json.RawMessage(raw)})
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
	identity := adoptOnce(context.Background(), client, gateway, controlplane.SnapshotIdentity{})
	identity = adoptOnce(context.Background(), client, gateway, identity)
	if acknowledgements.Load() != 2 {
		t.Fatalf("acknowledgements = %d, want 2", acknowledgements.Load())
	}
	if identity.CredentialDigest != "cred-b" {
		t.Fatalf("identity=%+v", identity)
	}
}

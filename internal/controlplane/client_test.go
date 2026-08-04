package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/1jehuang/2papi/internal/config"
)

func validConfig() config.Config {
	return config.Config{Version: 1, Secret: "s", VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "secret", Models: []string{"m"}, RPM: 100}}, Models: []config.Model{{Alias: "m", UpstreamModel: "u", Accounts: []string{"a"}}}, Accounts: []config.Account{{Name: "a", BaseURL: "http://upstream", APIKey: "ak", Enabled: true}}}
}

func TestFetchValidatesTokenChecksumAndBuildsSnapshot(t *testing.T) {
	raw, _ := json.Marshal(validConfig())
	sum := sha256.Sum256(raw)
	seenAck := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("X-Internal-Service-Token") != "token" {
			t.Fatalf("missing auth headers")
		}
		switch r.URL.Path {
		case "/api/internal/v1/snapshot":
			_ = json.NewEncoder(w).Encode(SnapshotEnvelope{Version: 7, Checksum: hex.EncodeToString(sum[:]), Snapshot: raw})
		case "/api/internal/v1/gateway-acks":
			seenAck = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	c := New(ts.URL, "token", "gw")
	snap, version, checksum, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != 7 || checksum == "" || snap.ModelsByAlias["m"].UpstreamModel != "u" {
		t.Fatalf("unexpected snapshot")
	}
	if err := c.Ack(context.Background(), Ack{Version: version, Checksum: checksum, Success: true}); err != nil {
		t.Fatal(err)
	}
	if !seenAck {
		t.Fatal("ack not sent")
	}
}

func TestFetchRejectsChecksumMismatch(t *testing.T) {
	raw, _ := json.Marshal(validConfig())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(SnapshotEnvelope{Version: 1, Checksum: "sha256:deadbeef", Snapshot: raw})
	}))
	defer ts.Close()
	_, _, _, err := New(ts.URL, "token", "gw").Fetch(context.Background())
	if err == nil {
		t.Fatal("want checksum error")
	}
}

package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/1jehuang/2papi/internal/config"
)

func validConfig() config.Config {
	return config.Config{Version: 1, Secret: "s", VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "secret", Models: []string{"m"}, RPM: 100}}, Models: []config.Model{{Alias: "m", UpstreamModel: "u", Accounts: []string{"a"}}}, Accounts: []config.Account{{Name: "a", BaseURL: "http://upstream", APIKey: "ak", Enabled: true}}}
}

func rawValidConfig(t *testing.T) ([]byte, string) {
	t.Helper()
	raw, err := json.Marshal(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:])
}

func TestSnapshotIdentityEqualComparesEveryField(t *testing.T) {
	base := SnapshotIdentity{ConfigVersion: 42, SchemaVersion: 2, ConfigChecksum: "cfg", CredentialDigest: "cred", RuntimeChecksum: "rt", EnvelopeVersion: 2}
	if !base.Equal(base) {
		t.Fatal("identical identity should be equal")
	}
	cases := map[string]SnapshotIdentity{
		"config version":    {ConfigVersion: 43, SchemaVersion: 2, ConfigChecksum: "cfg", CredentialDigest: "cred", RuntimeChecksum: "rt", EnvelopeVersion: 2},
		"schema version":    {ConfigVersion: 42, SchemaVersion: 1, ConfigChecksum: "cfg", CredentialDigest: "cred", RuntimeChecksum: "rt", EnvelopeVersion: 2},
		"config checksum":   {ConfigVersion: 42, SchemaVersion: 2, ConfigChecksum: "other", CredentialDigest: "cred", RuntimeChecksum: "rt", EnvelopeVersion: 2},
		"credential digest": {ConfigVersion: 42, SchemaVersion: 2, ConfigChecksum: "cfg", CredentialDigest: "other", RuntimeChecksum: "rt", EnvelopeVersion: 2},
		"runtime checksum":  {ConfigVersion: 42, SchemaVersion: 2, ConfigChecksum: "cfg", CredentialDigest: "cred", RuntimeChecksum: "other", EnvelopeVersion: 2},
		"envelope version":  {ConfigVersion: 42, SchemaVersion: 2, ConfigChecksum: "cfg", CredentialDigest: "cred", RuntimeChecksum: "rt", EnvelopeVersion: 1},
	}
	for name, other := range cases {
		if base.Equal(other) {
			t.Fatalf("%s change should not be equal", name)
		}
	}
}

func TestFetchV2EnvelopeIdentityHeadersAndRawChecksum(t *testing.T) {
	raw, checksum := rawValidConfig(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("X-Internal-Service-Token") != "token" || r.Header.Get("X-Gateway-ID") != "gw" {
			t.Fatalf("missing auth headers")
		}
		if r.Header.Get("X-Gateway-Snapshot-Schemas") != "1,2" || r.Header.Get("X-Gateway-Envelope-Version") != "2" {
			t.Fatalf("missing capability headers: %q %q", r.Header.Get("X-Gateway-Snapshot-Schemas"), r.Header.Get("X-Gateway-Envelope-Version"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"config_version": 42, "schema_version": 2, "config_checksum": "config-digest", "credential_digest": "cred", "runtime_checksum": checksum, "snapshot": json.RawMessage(raw)})
	}))
	defer ts.Close()
	snap, id, err := New(ts.URL, "token", "gw").Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if id.ConfigVersion != 42 || id.SchemaVersion != 2 || id.ConfigChecksum != "config-digest" || id.CredentialDigest != "cred" || id.RuntimeChecksum != checksum || id.EnvelopeVersion != 2 {
		t.Fatalf("identity=%+v", id)
	}
	if snap.ModelsByAlias["m"].UpstreamModel != "u" {
		t.Fatalf("unexpected snapshot")
	}
}

func TestFetchLegacyEnvelopeIdentity(t *testing.T) {
	raw, checksum := rawValidConfig(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(SnapshotEnvelope{Version: 7, Checksum: checksum, Snapshot: raw})
	}))
	defer ts.Close()
	_, id, err := New(ts.URL, "token", "gw").Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if id.ConfigVersion != 7 || id.SchemaVersion != 1 || id.ConfigChecksum != checksum || id.RuntimeChecksum != checksum || id.EnvelopeVersion != 1 {
		t.Fatalf("legacy identity=%+v", id)
	}
}

func TestFetchRejectsChecksumMismatch(t *testing.T) {
	raw, _ := rawValidConfig(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(SnapshotEnvelope{Version: 1, Checksum: "sha256:deadbeef", Snapshot: raw})
	}))
	defer ts.Close()
	_, id, err := New(ts.URL, "token", "gw").Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err=%v", err)
	}
	if id.ConfigVersion != 1 || id.ConfigChecksum == "" {
		t.Fatalf("identity=%+v", id)
	}
}

func TestFetchV2RequiresAllIdentityFields(t *testing.T) {
	raw, checksum := rawValidConfig(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"config_version": 42, "schema_version": 2, "config_checksum": "config-digest", "runtime_checksum": checksum, "snapshot": json.RawMessage(raw)})
	}))
	defer ts.Close()
	_, id, err := New(ts.URL, "token", "gw").Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "v2 snapshot identity fields required") {
		t.Fatalf("err=%v", err)
	}
	if id.EnvelopeVersion != 2 {
		t.Fatalf("identity=%+v", id)
	}
}

func TestFetchV2RejectsRuntimeChecksumMismatch(t *testing.T) {
	raw, _ := rawValidConfig(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"config_version": 42, "schema_version": 2, "config_checksum": "config-digest", "credential_digest": "cred", "runtime_checksum": "sha256:deadbeef", "snapshot": json.RawMessage(raw)})
	}))
	defer ts.Close()
	_, id, err := New(ts.URL, "token", "gw").Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "runtime checksum mismatch") {
		t.Fatalf("err=%v", err)
	}
	if id.ConfigChecksum != "config-digest" || id.EnvelopeVersion != 2 {
		t.Fatalf("identity=%+v", id)
	}
}

func TestFetchUpgradeRequired426(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upgrade", http.StatusUpgradeRequired)
	}))
	defer ts.Close()
	_, _, err := New(ts.URL, "token", "gw").Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "upgrade required") {
		t.Fatalf("err=%v", err)
	}
}

func TestHeartbeatHeadersAndBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/internal/v1/gateway-heartbeats" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Gateway-ID") != "gw" || r.Header.Get("X-Gateway-Snapshot-Schemas") != "1,2" || r.Header.Get("X-Gateway-Envelope-Version") != "2" {
			t.Fatalf("headers=%v", r.Header)
		}
		var body struct {
			GatewayID        string `json:"gateway_id"`
			SupportedSchemas []int  `json:"supported_schemas"`
			EnvelopeVersion  int    `json:"envelope_version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.GatewayID != "gw" || body.EnvelopeVersion != 2 || len(body.SupportedSchemas) != 2 || body.SupportedSchemas[0] != 1 || body.SupportedSchemas[1] != 2 {
			t.Fatalf("body=%+v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	if err := New(ts.URL, "token", "gw").Heartbeat(context.Background(), []int{1, 2}, 2); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateCredentialsUsesBoundedAuthenticatedCAS(t *testing.T) {
	credential := config.Credential{Kind: "oauth", AccessToken: "new-access-token", RefreshToken: "new-refresh-token", IDToken: "new-id-token", Revision: 7}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/internal/v1/accounts/account-1/credentials" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("X-Gateway-ID") != "gw" {
			t.Fatalf("missing authenticated gateway headers: %v", r.Header)
		}
		var body struct {
			ExpectedRevision int64             `json:"expected_revision"`
			Credential       config.Credential `json:"credential"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.ExpectedRevision != 7 || body.Credential.AccessToken != credential.AccessToken {
			t.Fatalf("body=%+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"credential_revision": 8, "credential_digest": "sha256:digest"}})
	}))
	defer ts.Close()

	result, err := New(ts.URL, "token", "gw").UpdateCredentials(context.Background(), "account-1", 7, credential)
	if err != nil {
		t.Fatal(err)
	}
	if result.CredentialRevision != 8 || result.CredentialDigest != "sha256:digest" {
		t.Fatalf("result=%+v", result)
	}
}

func TestUpdateCredentialsMapsConflictAndBoundsResponseWithoutLeakingBody(t *testing.T) {
	credential := config.Credential{Kind: "oauth", AccessToken: "access-token-must-not-leak", Revision: 7}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		check   func(error) bool
	}{
		{
			name: "conflict",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "access-token-must-not-leak", http.StatusConflict)
			},
			check: func(err error) bool { return errors.Is(err, ErrCredentialRevisionConflict) },
		},
		{
			name: "oversized",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(bytes.Repeat([]byte("x"), (256<<10)+1))
			},
			check: func(err error) bool { return err != nil && strings.Contains(err.Error(), "too large") },
		},
	} {
		ts := httptest.NewServer(tc.handler)
		_, err := New(ts.URL, "token", "gw").UpdateCredentials(context.Background(), "account-1", 7, credential)
		ts.Close()
		if !tc.check(err) {
			t.Fatalf("%s err=%v", tc.name, err)
		}
		if err != nil && strings.Contains(err.Error(), credential.AccessToken) {
			t.Fatalf("%s leaked credential in error: %v", tc.name, err)
		}
	}
}

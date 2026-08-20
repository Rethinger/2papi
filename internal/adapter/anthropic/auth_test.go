package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rethinger/2papi/internal/adapter/oauthrefresh"
	"github.com/Rethinger/2papi/internal/config"
)

type fakeSink struct {
	res   CredentialPersistResult
	err   error
	calls atomic.Int32
}

func (s *fakeSink) Persist(_ context.Context, _ string, _ int64, _ config.Credential) (CredentialPersistResult, error) {
	s.calls.Add(1)
	return s.res, s.err
}

func TestAccessTokenRefreshesExpiredOAuth(t *testing.T) {
	var refreshCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/oauth/token" {
			http.NotFound(w, r)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "refresh_token" || body["refresh_token"] != "old-refresh" {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		refreshCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh-token", "refresh_token": "new-refresh", "expires_in": 3600})
	}))
	defer ts.Close()

	sink := &fakeSink{res: CredentialPersistResult{Revision: 9, Digest: "digest-9"}}
	m := newTokenManagerWithURL(ts.Client(), sink, nil, ts.URL+"/v1/oauth/token")

	acct := config.Account{ID: "acct-1", Credential: config.Credential{
		Kind:         "oauth",
		AccessToken:  "old-token",
		RefreshToken: "old-refresh",
		ExpiresAt:    time.Now().Add(-time.Hour).Format(time.RFC3339),
		Revision:     7,
	}}

	cred, rev, warning, err := m.AccessToken(context.Background(), acct, false)
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken != "fresh-token" {
		t.Fatalf("access_token=%q", cred.AccessToken)
	}
	if cred.RefreshToken != "new-refresh" {
		t.Fatalf("refresh_token=%q", cred.RefreshToken)
	}
	if rev != 9 {
		t.Fatalf("revision=%d, want 9", rev)
	}
	if warning != "" {
		t.Fatalf("warning=%q", warning)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls=%d", refreshCalls.Load())
	}
	if sink.calls.Load() != 1 {
		t.Fatalf("persist calls=%d", sink.calls.Load())
	}

	// Second call must use the cached fresh credential, no refresh.
	cred2, rev2, _, err := m.AccessToken(context.Background(), acct, false)
	if err != nil {
		t.Fatal(err)
	}
	if cred2.AccessToken != "fresh-token" || rev2 != 9 {
		t.Fatalf("cached cred=%q rev=%d", cred2.AccessToken, rev2)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("unexpected second refresh: %d", refreshCalls.Load())
	}
}

func TestAccessTokenDoesNotRefreshNonOAuth(t *testing.T) {
	var tokenCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls.Add(1)
	}))
	defer ts.Close()

	m := newTokenManagerWithURL(ts.Client(), &fakeSink{res: CredentialPersistResult{Revision: 1}}, nil, ts.URL+"/v1/oauth/token")

	acct := config.Account{ID: "acct-2", Credential: config.Credential{Kind: "api_key", APIKey: "sk-ant-x", Revision: 1}}
	cred, rev, _, err := m.AccessToken(context.Background(), acct, true)
	if err != nil {
		t.Fatal(err)
	}
	if cred.APIKey != "sk-ant-x" || rev != 1 {
		t.Fatalf("api_key passthrough broken: %+v rev=%d", cred, rev)
	}
	if tokenCalls.Load() != 0 {
		t.Fatalf("token endpoint should not be called for api_key, got %d", tokenCalls.Load())
	}
}

func TestShouldRefresh(t *testing.T) {
	now := time.Now()
	if !oauthrefresh.ShouldRefresh(config.Credential{Kind: "oauth", AccessToken: "", ExpiresAt: "2099-01-01T00:00:00Z"}, now) {
		t.Fatal("missing access token must refresh")
	}

	if oauthrefresh.ShouldRefresh(config.Credential{Kind: "oauth", AccessToken: "a", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}, now) {
	}
	if !oauthrefresh.ShouldRefresh(config.Credential{Kind: "oauth", AccessToken: "a", ExpiresAt: now.Add(time.Minute).Format(time.RFC3339)}, now) {
		t.Fatal("expiring token must refresh")
	}
	if !oauthrefresh.ShouldRefresh(config.Credential{Kind: "oauth", AccessToken: "a", ExpiresAt: now.Add(-time.Hour).Format(time.RFC3339)}, now) {
		t.Fatal("expired token must refresh")
	}
}

func TestAccessTokenForceRefreshPersistsDegradedWarningOnSinkFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh", "expires_in": 3600})
	}))
	defer ts.Close()

	sink := &fakeSink{err: &degradedSinkError{}}
	m := newTokenManagerWithURL(ts.Client(), sink, nil, ts.URL+"/v1/oauth/token")

	acct := config.Account{ID: "acct-3", Credential: config.Credential{
		Kind: "oauth", AccessToken: "old", RefreshToken: "rt", ExpiresAt: time.Now().Add(-time.Hour).Format(time.RFC3339), Revision: 3,
	}}
	cred, _, warning, err := m.AccessToken(context.Background(), acct, false)
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken != "fresh" {
		t.Fatalf("access_token=%q", cred.AccessToken)
	}
	if warning != "credential_persistence_degraded" {
		t.Fatalf("warning=%q", warning)
	}
}

type degradedSinkError struct{}

func (e *degradedSinkError) Error() string { return "sink down" }

func TestControlPlaneSinkUnavailableWithoutClient(t *testing.T) {
	var sink ControlPlaneSink
	_, err := sink.Persist(context.Background(), "a", 1, config.Credential{})
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("err=%v", err)
	}
}

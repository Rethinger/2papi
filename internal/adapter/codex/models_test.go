package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1jehuang/2papi/internal/adapter"
	"github.com/1jehuang/2papi/internal/config"
)

func TestDiscoverModelsHeadersAndPayload(t *testing.T) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/backend-api/codex/models" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Errorf("auth=%q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "acct" {
			t.Errorf("account=%q", got)
		}
		if got := r.URL.Query().Get("client_version"); got != "test-version" {
			t.Errorf("client_version=%q", got)
		}
		_, _ = w.Write([]byte(`{"models":[{"slug":"codex-mini","visibility":"allow","supported_in_api":true,"context_window":128000,"capabilities":{"tools":true}}]}`))
	}))
	defer ts.Close()
	a := New(ts.Client(), nil, nil, Options{TestMode: true, AuthBaseURL: ts.URL, BackendBaseURL: ts.URL, ClientVersion: "test-version", Now: time.Now})
	out, err := a.Operate(context.Background(), adapter.Operation{Kind: adapter.OperationDiscoverModels, Account: config.Account{ID: "id", Credential: config.Credential{AccessToken: "access", ChatGPTAccountID: "acct", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339), Revision: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Data, &got); err != nil {
		t.Fatal(err)
	}
	if got["models"] == nil {
		t.Fatalf("models missing: %s", out.Data)
	}
	if out.CredentialRevision != 3 {
		t.Fatalf("revision=%d", out.CredentialRevision)
	}
}

func TestDefaultClientVersionIsSemanticVersion(t *testing.T) {
	options := normalizeOptions(Options{TestMode: true})
	if options.ClientVersion != "1.0.0" {
		t.Fatalf("client version=%q", options.ClientVersion)
	}
}

func TestDiscoverRefreshesOnceOn401(t *testing.T) {
	var modelCalls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			_, _ = w.Write([]byte(`{"access_token":"fresh","expires_in":3600}`))
		case "/backend-api/codex/models":
			if atomic.AddInt32(&modelCalls, 1) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") != "Bearer fresh" {
				t.Errorf("auth=%q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"models":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()
	a := New(ts.Client(), &fakeSink{res: CredentialPersistResult{Revision: 2}}, nil, Options{TestMode: true, AuthBaseURL: ts.URL, BackendBaseURL: ts.URL, Now: time.Now})
	_, err := a.Operate(context.Background(), adapter.Operation{Kind: adapter.OperationDiscoverModels, Account: config.Account{ID: "id", Credential: config.Credential{AccessToken: "stale", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339), Revision: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if modelCalls != 2 {
		t.Fatalf("model calls=%d", modelCalls)
	}
}

func TestDiscoverReturnsPersistenceDegradedWarning(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			_, _ = w.Write([]byte(`{"access_token":"fresh","expires_in":3600}`))
		case "/backend-api/codex/models":
			if r.Header.Get("Authorization") != "Bearer fresh" {
				t.Errorf("auth=%q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"models":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()
	a := New(ts.Client(), &fakeSink{fails: 10}, nil, Options{TestMode: true, AuthBaseURL: ts.URL, BackendBaseURL: ts.URL, Now: time.Now})
	out, err := a.Operate(context.Background(), adapter.Operation{Kind: adapter.OperationDiscoverModels, Account: config.Account{ID: "id", Credential: config.Credential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Hour).Format(time.RFC3339), Revision: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if out.WarningCode != "credential_persistence_degraded" || out.CredentialRevision != 1 {
		t.Fatalf("out=%+v", out)
	}
}

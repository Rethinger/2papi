package codex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/controlplane"
)

type fakeSink struct {
	calls       int32
	err         error
	res         CredentialPersistResult
	fails       int32
	conflictAt  int32
	successSeen chan CredentialPersistResult
}

func (s *fakeSink) Persist(context.Context, string, int64, config.Credential) (CredentialPersistResult, error) {
	call := atomic.AddInt32(&s.calls, 1)
	if s.conflictAt > 0 && call >= s.conflictAt {
		return CredentialPersistResult{}, controlplane.ErrCredentialRevisionConflict
	}
	if remaining := atomic.LoadInt32(&s.fails); remaining > 0 && atomic.CompareAndSwapInt32(&s.fails, remaining, remaining-1) {
		return CredentialPersistResult{}, fmt.Errorf("temporary persistence failure")
	}
	if s.err != nil {
		return CredentialPersistResult{}, s.err
	}
	if s.res.Revision == 0 {
		s.res.Revision = 8
	}
	if s.successSeen != nil {
		select {
		case s.successSeen <- s.res:
		default:
		}
	}
	return s.res, nil
}

type fakeRefresh struct{ calls int32 }

func (f *fakeRefresh) TriggerSnapshotRefresh(string) { atomic.AddInt32(&f.calls, 1) }

func TestRefreshSingleflightAndPreservesRefreshToken(t *testing.T) {
	var tokenCalls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","expires_in":3600}`))
	}))
	defer ts.Close()
	sink := &fakeSink{res: CredentialPersistResult{Revision: 9, Digest: "digest"}}
	m := newTokenManager(ts.Client(), sink, nil, normalizeOptions(Options{TestMode: true, AuthBaseURL: ts.URL, BackendBaseURL: ts.URL, Now: func() time.Time { return time.Unix(100, 0) }}))
	acct := config.Account{ID: "acct", Credential: config.Credential{Kind: "oauth", AccessToken: "old", RefreshToken: "keep-me", ClientID: "client", ChatGPTAccountID: "chat", ExpiresAt: time.Unix(100, 0).Format(time.RFC3339), Revision: 7}}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cred, rev, warning, err := m.accessToken(context.Background(), acct, false)
			if err != nil {
				t.Errorf("refresh: %v", err)
				return
			}
			if warning != "" {
				t.Errorf("warning=%q", warning)
			}
			if cred.AccessToken != "new-access" || cred.RefreshToken != "keep-me" || rev != 9 {
				t.Errorf("got cred=%+v rev=%d", cred, rev)
			}
		}()
	}
	wg.Wait()
	if tokenCalls != 1 {
		t.Fatalf("token calls=%d", tokenCalls)
	}
	if sink.calls != 1 {
		t.Fatalf("sink calls=%d", sink.calls)
	}
}

func TestRevisionConflictTriggersSingleSnapshotRefresh(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"new","expires_in":3600}`))
	}))
	defer ts.Close()
	sink := &fakeSink{err: controlplane.ErrCredentialRevisionConflict}
	trigger := &fakeRefresh{}
	m := newTokenManager(ts.Client(), sink, trigger, normalizeOptions(Options{TestMode: true, AuthBaseURL: ts.URL, BackendBaseURL: ts.URL, Now: time.Now}))
	acct := config.Account{ID: "acct", Credential: config.Credential{RefreshToken: "r", ExpiresAt: time.Now().Add(-time.Hour).Format(time.RFC3339), Revision: 7}}
	_, _, _, err := m.accessToken(context.Background(), acct, false)
	if !errors.Is(err, ErrSnapshotRefreshRequired) {
		t.Fatalf("got err %v", err)
	}
	if trigger.calls != 1 {
		t.Fatalf("trigger calls=%d", trigger.calls)
	}
}

func TestTransientPersistenceFailureReturnsWarningAndRetries(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"fresh","expires_in":3600}`))
	}))
	defer ts.Close()
	sink := &fakeSink{fails: 1, res: CredentialPersistResult{Revision: 8, Digest: "digest-8"}, successSeen: make(chan CredentialPersistResult, 1)}
	m := newTokenManager(ts.Client(), sink, nil, normalizeOptions(Options{TestMode: true, AuthBaseURL: ts.URL, BackendBaseURL: ts.URL, Now: time.Now}))
	acct := config.Account{ID: "acct", Credential: config.Credential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Hour).Format(time.RFC3339), Revision: 7}}
	cred, rev, warning, err := m.accessToken(context.Background(), acct, false)
	if err != nil {
		t.Fatal(err)
	}
	if warning != "credential_persistence_degraded" || cred.AccessToken != "fresh" || rev != 7 {
		t.Fatalf("cred=%+v rev=%d warning=%q", cred, rev, warning)
	}
	select {
	case res := <-sink.successSeen:
		if res.Revision != 8 || res.Digest != "digest-8" {
			t.Fatalf("res=%+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("background retry did not succeed")
	}
	deadline := time.After(time.Second)
	for {
		cred, rev, warning, err = m.accessToken(context.Background(), acct, false)
		if err == nil && warning == "" && rev == 8 && cred.Revision == 8 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("after retry cred=%+v rev=%d warning=%q err=%v", cred, rev, warning, err)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err != nil || warning != "" || rev != 8 || cred.Revision != 8 {
		t.Fatalf("after retry cred=%+v rev=%d warning=%q err=%v", cred, rev, warning, err)
	}
}

func TestRetryCoalescesAndConflictTriggersOneRefresh(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"fresh","expires_in":3600}`))
	}))
	defer ts.Close()
	sink := &fakeSink{fails: 1, conflictAt: 2}
	trigger := &fakeRefresh{}
	m := newTokenManager(ts.Client(), sink, trigger, normalizeOptions(Options{TestMode: true, AuthBaseURL: ts.URL, BackendBaseURL: ts.URL, Now: time.Now}))
	acct := config.Account{ID: "acct", Credential: config.Credential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Hour).Format(time.RFC3339), Revision: 7}}
	for i := 0; i < 3; i++ {
		_, _, warning, err := m.accessToken(context.Background(), acct, false)
		if err != nil || warning != "credential_persistence_degraded" {
			t.Fatalf("iteration %d warning=%q err=%v", i, warning, err)
		}
	}
	deadline := time.After(time.Second)
	for atomic.LoadInt32(&trigger.calls) < 1 {
		select {
		case <-deadline:
			t.Fatalf("trigger calls=%d sink calls=%d", trigger.calls, sink.calls)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if trigger.calls != 1 {
		t.Fatalf("trigger calls=%d", trigger.calls)
	}
	if sink.calls != 2 {
		t.Fatalf("expected initial persist plus one coalesced retry, got %d", sink.calls)
	}
}

func TestStaleSnapshotDoesNotResetNewerLocalCredential(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"fresh","expires_in":3600}`))
	}))
	defer ts.Close()
	sink := &fakeSink{res: CredentialPersistResult{Revision: 8, Digest: "digest-8"}}
	m := newTokenManager(ts.Client(), sink, nil, normalizeOptions(Options{TestMode: true, AuthBaseURL: ts.URL, BackendBaseURL: ts.URL, Now: time.Now}))
	stale := config.Account{ID: "acct", Credential: config.Credential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Hour).Format(time.RFC3339), Revision: 7}}
	cred, rev, _, err := m.accessToken(context.Background(), stale, false)
	if err != nil || cred.AccessToken != "fresh" || rev != 8 {
		t.Fatalf("first cred=%+v rev=%d err=%v", cred, rev, err)
	}
	cred, rev, warning, err := m.accessToken(context.Background(), stale, false)
	if err != nil || warning != "" || cred.AccessToken != "fresh" || rev != 8 {
		t.Fatalf("stale reset cred=%+v rev=%d warning=%q err=%v", cred, rev, warning, err)
	}
}

package codex

import (
	"context"
	"errors"
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
	calls int32
	err   error
	res   CredentialPersistResult
}

func (s *fakeSink) Persist(context.Context, string, int64, config.Credential) (CredentialPersistResult, error) {
	atomic.AddInt32(&s.calls, 1)
	if s.err != nil {
		return CredentialPersistResult{}, s.err
	}
	if s.res.Revision == 0 {
		s.res.Revision = 8
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
			cred, rev, err := m.accessToken(context.Background(), acct, false)
			if err != nil {
				t.Errorf("refresh: %v", err)
				return
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
	_, _, err := m.accessToken(context.Background(), acct, false)
	if !errors.Is(err, ErrSnapshotRefreshRequired) {
		t.Fatalf("got err %v", err)
	}
	if trigger.calls != 1 {
		t.Fatalf("trigger calls=%d", trigger.calls)
	}
}

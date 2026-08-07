package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/controlplane"
)

type tokenManager struct {
	client  *http.Client
	sink    CredentialSink
	refresh SnapshotRefreshTrigger
	options Options
	mu      sync.Mutex
	creds   map[string]*credentialState
	calls   map[string]*refreshCall
}

type credentialState struct {
	cred     config.Credential
	revision int64
	digest   string
	degraded bool
}
type refreshCall struct {
	done     chan struct{}
	cred     config.Credential
	revision int64
	err      error
	warning  string
}

type degradedError struct{ err error }

func (e degradedError) Error() string { return ErrCredentialPersistenceDegraded.Error() }
func (e degradedError) Unwrap() error { return e.err }

func newTokenManager(client *http.Client, sink CredentialSink, refresh SnapshotRefreshTrigger, options Options) *tokenManager {
	return &tokenManager{client: client, sink: sink, refresh: refresh, options: options, creds: map[string]*credentialState{}, calls: map[string]*refreshCall{}}
}

func (m *tokenManager) accessToken(ctx context.Context, account config.Account, force bool) (config.Credential, int64, error) {
	cred := account.Credential
	rev := cred.Revision
	if rev == 0 {
		rev = account.Credential.Revision
	}
	key := account.ID
	if key == "" {
		key = account.Name
	}
	m.mu.Lock()
	st := m.creds[key]
	if st == nil || st.revision != rev {
		c := cred
		st = &credentialState{cred: c, revision: rev}
		m.creds[key] = st
	}
	need := force || shouldRefresh(st.cred, m.options.Now())
	if !need {
		out, r := st.cred, st.revision
		m.mu.Unlock()
		return out, r, nil
	}
	callKey := fmt.Sprintf("%s:%d", key, st.revision)
	if c := m.calls[callKey]; c != nil {
		m.mu.Unlock()
		<-c.done
		return c.cred, c.revision, c.err
	}
	c := &refreshCall{done: make(chan struct{})}
	m.calls[callKey] = c
	base := st.cred
	expected := st.revision
	m.mu.Unlock()
	c.cred, c.revision, c.err = m.refreshToken(ctx, key, expected, base)
	m.mu.Lock()
	delete(m.calls, callKey)
	m.mu.Unlock()
	close(c.done)
	return c.cred, c.revision, c.err
}

func shouldRefresh(c config.Credential, now time.Time) bool {
	if c.AccessToken == "" {
		return true
	}
	if c.ExpiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, c.ExpiresAt)
	if err != nil {
		return true
	}
	return !t.After(now.Add(5 * time.Minute))
}

func (m *tokenManager) refreshToken(ctx context.Context, accountID string, expected int64, old config.Credential) (config.Credential, int64, error) {
	if old.RefreshToken == "" {
		return old, expected, errors.New("codex refresh token required")
	}
	body, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": old.RefreshToken, "client_id": old.ClientID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.options.AuthBaseURL, "/")+"/oauth/token", bytes.NewReader(body))
	if err != nil {
		return old, expected, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return old, expected, err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return old, expected, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return old, expected, fmt.Errorf("codex token refresh status %d", resp.StatusCode)
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
		ExpiresAt    string `json:"expires_at"`
	}
	if err := json.Unmarshal(rb, &tr); err != nil {
		return old, expected, err
	}
	if tr.AccessToken == "" {
		return old, expected, errors.New("codex token refresh response missing access token")
	}
	fresh := old
	fresh.AccessToken = tr.AccessToken
	if tr.RefreshToken != "" {
		fresh.RefreshToken = tr.RefreshToken
	}
	if tr.IDToken != "" {
		fresh.IDToken = tr.IDToken
	}
	if tr.ExpiresAt != "" {
		fresh.ExpiresAt = tr.ExpiresAt
	} else if tr.ExpiresIn > 0 {
		fresh.ExpiresAt = m.options.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	m.mu.Lock()
	if st := m.creds[accountID]; st != nil {
		st.cred = fresh
	}
	m.mu.Unlock()
	if m.sink == nil {
		return fresh, expected, nil
	}
	res, err := m.persistWithRetry(ctx, accountID, expected, fresh)
	if err != nil {
		if errors.Is(err, controlplane.ErrCredentialRevisionConflict) {
			if m.refresh != nil {
				m.refresh.TriggerSnapshotRefresh("credential_revision_conflict")
			}
			return fresh, expected, ErrSnapshotRefreshRequired
		}
		return fresh, expected, degradedError{err: err}
	}
	fresh.Revision = res.Revision
	m.mu.Lock()
	if st := m.creds[accountID]; st != nil {
		st.cred = fresh
		st.revision = res.Revision
		st.digest = res.Digest
	}
	m.mu.Unlock()
	return fresh, res.Revision, nil
}

func (m *tokenManager) persistWithRetry(ctx context.Context, accountID string, expected int64, fresh config.Credential) (CredentialPersistResult, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		res, err := m.sink.Persist(ctx, accountID, expected, fresh)
		if err == nil || errors.Is(err, controlplane.ErrCredentialRevisionConflict) {
			return res, err
		}
		last = err
		select {
		case <-ctx.Done():
			return CredentialPersistResult{}, ctx.Err()
		case <-time.After(time.Duration(10*(1<<attempt)) * time.Millisecond):
		}
	}
	return CredentialPersistResult{}, last
}

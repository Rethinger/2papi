// Package oauthrefresh holds the generic OAuth token-refresh machinery shared
// by adapters that mint short-lived access tokens (anthropic): expiry
// checks, single-flight refresh, control-plane persistence with retry, and
// snapshot re-adoption on revision conflicts. The adapter-specific HTTP
// exchange lives in a Refresher implementation supplied by each adapter.
package oauthrefresh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/controlplane"
)

var (
	// ErrSnapshotRefreshRequired signals that the control plane rejected a
	// credential persist due to a revision conflict; the gateway must re-fetch
	// the snapshot before using the refreshed credential.
	ErrSnapshotRefreshRequired = errors.New("snapshot refresh required")
	// ErrCredentialPersistenceDegraded reports that a refreshed credential is
	// in use locally but could not be persisted yet.
	ErrCredentialPersistenceDegraded = errors.New("credential persistence degraded")
)

type CredentialPersistResult struct {
	Revision int64
	Digest   string
}

// CredentialSink persists refreshed OAuth credentials back to the control
// plane. A nil sink disables persistence (in-memory refresh only).
type CredentialSink interface {
	Persist(context.Context, string, int64, config.Credential) (CredentialPersistResult, error)
}

// SnapshotRefreshTrigger asks the gateway to re-adopt the control-plane
// snapshot (used when a credential revision conflict is detected).
type SnapshotRefreshTrigger interface {
	TriggerSnapshotRefresh(reason string)
}

// Refresher performs the adapter-specific token exchange (POST to the
// provider's OAuth token endpoint) and returns the fresh credential.
type Refresher interface {
	Refresh(ctx context.Context, old config.Credential) (config.Credential, error)
}

// ControlPlaneSink persists credentials through the control-plane internal
// credentials API.
type ControlPlaneSink struct{ Client *controlplane.Client }

func (s ControlPlaneSink) Persist(ctx context.Context, accountID string, expectedRevision int64, credential config.Credential) (CredentialPersistResult, error) {
	if s.Client == nil {
		return CredentialPersistResult{}, errors.New("control-plane credential sink unavailable")
	}
	res, err := s.Client.UpdateCredentials(ctx, accountID, expectedRevision, credential)
	if err != nil {
		return CredentialPersistResult{}, err
	}
	return CredentialPersistResult{Revision: res.CredentialRevision, Digest: res.CredentialDigest}, nil
}

type Manager struct {
	client    *http.Client
	sink      CredentialSink
	refresh   SnapshotRefreshTrigger
	refresher Refresher
	now       func() time.Time
	mu        sync.Mutex
	creds     map[string]*credentialState
	calls     map[string]*refreshCall
	retries   map[string]struct{}
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

// NewManager returns a Manager that refreshes via r. The client is shared
// with adapters that need it (e.g. Refresher HTTP calls); it is retained on
// the Manager for adapter convenience.
func NewManager(client *http.Client, sink CredentialSink, refresh SnapshotRefreshTrigger, r Refresher) *Manager {
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	return &Manager{
		client:    client,
		sink:      sink,
		refresh:   refresh,
		refresher: r,
		now:       time.Now,
		creds:     map[string]*credentialState{},
		calls:     map[string]*refreshCall{},
		retries:   map[string]struct{}{},
	}
}

// Client exposes the shared HTTP client for adapters that build a Refresher
// around it.
func (m *Manager) Client() *http.Client { return m.client }

// AccessToken returns a usable OAuth credential, refreshing it when expired.
// force=true refreshes even when the stored token looks valid. Non-OAuth
// credentials are returned unchanged.
func (m *Manager) AccessToken(ctx context.Context, account config.Account, force bool) (config.Credential, int64, string, error) {
	cred := account.Credential
	if cred.Kind != "oauth" {
		return cred, cred.Revision, "", nil
	}
	rev := cred.Revision
	key := account.ID
	if key == "" {
		key = account.Name
	}
	m.mu.Lock()
	st := m.creds[key]
	if st == nil || rev > st.revision {
		c := cred
		st = &credentialState{cred: c, revision: rev}
		m.creds[key] = st
	}
	need := force || ShouldRefresh(st.cred, m.now())
	if !need {
		out, r, warning := st.cred, st.revision, ""
		if st.degraded {
			warning = "credential_persistence_degraded"
		}
		m.mu.Unlock()
		return out, r, warning, nil
	}
	callKey := fmt.Sprintf("%s:%d", key, st.revision)
	if c := m.calls[callKey]; c != nil {
		m.mu.Unlock()
		<-c.done
		return c.cred, c.revision, c.warning, c.err
	}
	c := &refreshCall{done: make(chan struct{})}
	m.calls[callKey] = c
	base := st.cred
	expected := st.revision
	m.mu.Unlock()
	c.cred, c.revision, c.warning, c.err = m.refreshToken(ctx, key, expected, base)
	m.mu.Lock()
	delete(m.calls, callKey)
	m.mu.Unlock()
	close(c.done)
	return c.cred, c.revision, c.warning, c.err
}

// ShouldRefresh reports whether an OAuth credential needs refreshing: empty
// access token, unparseable expiry, or expiry within 5 minutes.
func ShouldRefresh(c config.Credential, now time.Time) bool {
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

func (m *Manager) refreshToken(ctx context.Context, accountID string, expected int64, old config.Credential) (config.Credential, int64, string, error) {
	if m.refresher == nil {
		return old, expected, "", errors.New("oauth refresher unavailable")
	}
	fresh, err := m.refresher.Refresh(ctx, old)
	if err != nil {
		return old, expected, "", err
	}
	m.mu.Lock()
	if st := m.creds[accountID]; st != nil {
		st.cred = fresh
		st.degraded = false
	}
	m.mu.Unlock()
	if m.sink == nil {
		return fresh, expected, "", nil
	}
	res, err := m.sink.Persist(ctx, accountID, expected, fresh)
	if err != nil {
		if errors.Is(err, controlplane.ErrCredentialRevisionConflict) {
			if m.refresh != nil {
				m.refresh.TriggerSnapshotRefresh("credential_revision_conflict")
			}
			return fresh, expected, "", ErrSnapshotRefreshRequired
		}
		m.markDegraded(accountID)
		m.schedulePersistRetry(accountID, expected, fresh)
		return fresh, expected, "credential_persistence_degraded", nil
	}
	fresh.Revision = res.Revision
	m.mu.Lock()
	if st := m.creds[accountID]; st != nil {
		st.cred = fresh
		st.revision = res.Revision
		st.digest = res.Digest
		st.degraded = false
	}
	m.mu.Unlock()
	return fresh, res.Revision, "", nil
}

func (m *Manager) markDegraded(accountID string) {
	m.mu.Lock()
	if st := m.creds[accountID]; st != nil {
		st.degraded = true
	}
	m.mu.Unlock()
}

func (m *Manager) schedulePersistRetry(accountID string, expected int64, fresh config.Credential) {
	if m.sink == nil {
		return
	}
	key := fmt.Sprintf("%s:%d", accountID, expected)
	m.mu.Lock()
	if _, ok := m.retries[key]; ok {
		m.mu.Unlock()
		return
	}
	m.retries[key] = struct{}{}
	m.mu.Unlock()
	go m.persistRetryLoop(key, accountID, expected, fresh)
}

func (m *Manager) persistRetryLoop(key, accountID string, expected int64, fresh config.Credential) {
	defer func() {
		m.mu.Lock()
		delete(m.retries, key)
		m.mu.Unlock()
	}()
	for attempt := range 6 {
		time.Sleep(time.Duration(10*(1<<attempt)) * time.Millisecond)
		res, err := m.sink.Persist(context.Background(), accountID, expected, fresh)
		if err == nil {
			fresh.Revision = res.Revision
			m.mu.Lock()
			if st := m.creds[accountID]; st != nil && st.revision == expected && st.cred.AccessToken == fresh.AccessToken {
				st.cred = fresh
				st.revision = res.Revision
				st.digest = res.Digest
				st.degraded = false
			}
			m.mu.Unlock()
			return
		}
		if errors.Is(err, controlplane.ErrCredentialRevisionConflict) {
			if m.refresh != nil {
				m.refresh.TriggerSnapshotRefresh("credential_revision_conflict")
			}
			return
		}
	}
}

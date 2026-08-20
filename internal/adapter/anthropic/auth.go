package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Rethinger/2papi/internal/adapter/oauthrefresh"
	"github.com/Rethinger/2papi/internal/config"
)

// Claude CLI OAuth client registration used for the claude.ai browser login
// and token refresh (same client_id the official Claude Code CLI uses).
const oauthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

type (
	CredentialSink          = oauthrefresh.CredentialSink
	CredentialPersistResult = oauthrefresh.CredentialPersistResult
	SnapshotRefreshTrigger  = oauthrefresh.SnapshotRefreshTrigger
	ControlPlaneSink        = oauthrefresh.ControlPlaneSink
	tokenManager            = oauthrefresh.Manager
)

var (
	ErrSnapshotRefreshRequired       = oauthrefresh.ErrSnapshotRefreshRequired
	ErrCredentialPersistenceDegraded = oauthrefresh.ErrCredentialPersistenceDegraded
)

// refresher performs the Anthropic OAuth token exchange: POST
// {tokenURL} with grant_type=refresh_token.
type refresher struct {
	client   *http.Client
	tokenURL string
}

func (r *refresher) Refresh(ctx context.Context, old config.Credential) (config.Credential, error) {
	if old.RefreshToken == "" {
		return old, errors.New("anthropic refresh token required")
	}
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": old.RefreshToken,
		"client_id":     oauthClientID,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.tokenURL, bytes.NewReader(body))
	if err != nil {
		return old, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return old, err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return old, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return old, fmt.Errorf("anthropic token refresh status %d", resp.StatusCode)
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(rb, &tr); err != nil {
		return old, err
	}
	if tr.AccessToken == "" {
		return old, errors.New("anthropic token refresh response missing access token")
	}
	fresh := old
	fresh.AccessToken = tr.AccessToken
	if tr.RefreshToken != "" {
		fresh.RefreshToken = tr.RefreshToken
	}
	if tr.ExpiresIn > 0 {
		fresh.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	return fresh, nil
}

func newTokenManager(client *http.Client, sink CredentialSink, refresh SnapshotRefreshTrigger) *tokenManager {
	return newTokenManagerWithURL(client, sink, refresh, DefaultAnthropicURL+"/v1/oauth/token")
}

func newTokenManagerWithURL(client *http.Client, sink CredentialSink, refresh SnapshotRefreshTrigger, tokenURL string) *tokenManager {
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	r := &refresher{client: client, tokenURL: tokenURL}
	return oauthrefresh.NewManager(client, sink, refresh, r)
}

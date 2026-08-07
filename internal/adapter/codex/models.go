package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/1jehuang/2papi/internal/config"
)

const maxModelDiscoveryBody = 1 << 20

type modelClient struct {
	client  *http.Client
	options Options
}

func newModelClient(client *http.Client, options Options) *modelClient {
	return &modelClient{client: client, options: options}
}

type ModelDiscovery struct {
	Models []CodexModel `json:"models"`
}
type CodexModel struct {
	Slug           string          `json:"slug"`
	Visibility     string          `json:"visibility,omitempty"`
	SupportedInAPI bool            `json:"supported_in_api"`
	ContextWindow  int64           `json:"context_window,omitempty"`
	Capabilities   json.RawMessage `json:"capabilities,omitempty"`
}

func (m *modelClient) discover(ctx context.Context, cred config.Credential) (json.RawMessage, error) {
	raw, err := m.getModels(ctx, cred)
	if err != nil {
		return nil, err
	}
	var env struct {
		Models []CodexModel `json:"models"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	if env.Models == nil {
		env.Models = []CodexModel{}
	}
	return json.Marshal(ModelDiscovery{Models: env.Models})
}

func (m *modelClient) validate(ctx context.Context, cred config.Credential) error {
	_, err := m.getModels(ctx, cred)
	return err
}

func (m *modelClient) getModels(ctx context.Context, cred config.Credential) ([]byte, error) {
	base := strings.TrimRight(m.options.BackendBaseURL, "/")
	u, err := url.Parse(base + "/backend-api/codex/models")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("client_version", m.options.ClientVersion)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("ChatGPT-Account-ID", cred.ChatGPTAccountID)
	req.Header.Set("client_version", m.options.ClientVersion)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := readLimited(resp.Body, maxModelDiscoveryBody)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, unauthorizedError{}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("codex model discovery status %d", resp.StatusCode)
	}
	return body, nil
}

type unauthorizedError struct{}

func (unauthorizedError) Error() string { return "codex unauthorized" }
func isUnauthorized(err error) bool     { var e unauthorizedError; return errors.As(err, &e) }

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("codex response body too large")
	}
	return b, nil
}

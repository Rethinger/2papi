// Package thirdparty hosts lightweight OpenAI-compatible provider adapters
// for subscription (OAuth/cookie) and free-tier accounts: Cursor, GitHub
// Copilot, Kimi, OpenCode Free, Felo, Qoder. They reuse the generic
// oauthrefresh.Manager for single-flight OAuth token refresh and forward
// provider quota headers (X-Provider-Quota-* / codex-style credits) to the
// gateway quota tracker via the standard result headers.
package thirdparty

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/adapter/oauthrefresh"
)

type Spec struct {
	// Name is the registered adapter name (openai-compatible style).
	Name string
	// DefaultBaseURL is used when account.BaseURL is empty.
	DefaultBaseURL string
	// Headers applied to every request (subscription spoof / family hints).
	Headers map[string]string
	// SupportsOAuth enables token refresh for Kind=oauth via oauthrefresh.
	SupportsOAuth bool
	// FreeByDefault marks providers whose credential may be Kind=free.
	FreeByDefault bool
}

type Adapter struct {
	Client *http.Client
	spec   Spec
	auth   *oauthrefresh.Manager
}

func New(client *http.Client, spec Spec) *Adapter {
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	ad := &Adapter{Client: client, spec: spec}
	if spec.SupportsOAuth {
		// In-memory refresh only; the gateway re-registers with a control-plane
		// sink when available. Refresher is nil until RegisterWithAuth.
	}
	return ad
}

// RegisterWithAuth attaches OAuth refresh (control-plane sink + snapshot
// trigger) to an OAuth-capable provider.
func RegisterWithAuth(reg *adapter.Registry, spec Spec, client *http.Client, sink oauthrefresh.CredentialSink, trigger oauthrefresh.SnapshotRefreshTrigger) error {
	ad := New(client, spec)
	if spec.SupportsOAuth {
		ad.auth = oauthrefresh.NewManager(client, sink, trigger, nil)
	}
	if _, exists := reg.Get(spec.Name); exists {
		return nil // already registered (e.g. via NewWithClient default set)
	}
	return reg.Register(spec.Name, ad)
}

func (a *Adapter) Execute(ctx context.Context, ex adapter.Execution) (*adapter.Result, error) {
	// Only chat completions + responses are useful for these providers.
	if ex.Endpoint != adapter.EndpointChatCompletions && ex.Endpoint != adapter.EndpointResponses {
		return nil, &adapter.CapabilityError{Kind: adapter.OperationKind(ex.Endpoint)}
	}
	base := ex.Account.BaseURL
	if base == "" {
		base = a.spec.DefaultBaseURL
	}
	path := "/v1/chat/completions"
	if ex.Endpoint == adapter.EndpointResponses {
		path = "/v1/responses"
	}
	body := ex.Body
	if ex.Endpoint == adapter.EndpointChatCompletions {
		rw, err := rewriteModel(ex.Body, ex.Model.UpstreamModel)
		if err != nil {
			return nil, err
		}
		body = rw
	}
	u, err := joinBaseURL(base, path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if ex.Request != nil {
		copySafeHeaders(req.Header, ex.Request.Header)
	}
	req.Header.Set("Content-Type", "application/json")
	cred := ex.Account.Credential
	if cred.Kind == "oauth" && a.auth != nil {
		fresh, _, _, err := a.auth.AccessToken(ctx, ex.Account, false)
		if err == nil {
			cred = fresh
		}
	}
	switch cred.Kind {
	case "oauth":
		req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	case "api_key":
		req.Header.Set("Authorization", "Bearer "+cred.APIKey)
	case "cookie":
		if cred.Cookies != "" {
			req.Header.Set("Cookie", cred.Cookies)
		}
	case "free", "":
		// no auth
	}
	for k, v := range a.spec.Headers {
		req.Header.Set(k, v)
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}
	// 401 on oauth → force refresh, retry once.
	if resp.StatusCode == http.StatusUnauthorized && cred.Kind == "oauth" && a.auth != nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		fresh, _, _, err := a.auth.AccessToken(ctx, ex.Account, true)
		if err != nil {
			return nil, err
		}
		req2, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if ex.Request != nil {
			copySafeHeaders(req2.Header, ex.Request.Header)
		}
		req2.Header.Set("Authorization", "Bearer "+fresh.AccessToken)
		req2.Header.Set("Content-Type", "application/json")
		for k, v := range a.spec.Headers {
			req2.Header.Set(k, v)
		}
		resp, err = a.Client.Do(req2)
		if err != nil {
			return nil, err
		}
	}
	return &adapter.Result{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: resp.Body}, nil
}

func (a *Adapter) Operate(_ context.Context, op adapter.Operation) (adapter.OperationResult, error) {
	if op.Kind != adapter.OperationDiscoverModels {
		return adapter.OperationResult{}, &adapter.CapabilityError{Kind: op.Kind}
	}
	base := op.Account.BaseURL
	if base == "" {
		base = a.spec.DefaultBaseURL
	}
	u, err := joinBaseURL(base, "/v1/models")
	if err != nil {
		return adapter.OperationResult{}, err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
	if err != nil {
		return adapter.OperationResult{}, err
	}
	switch op.Account.Credential.Kind {
	case "oauth":
		req.Header.Set("Authorization", "Bearer "+op.Account.Credential.AccessToken)
	case "api_key":
		req.Header.Set("Authorization", "Bearer "+op.Account.Credential.APIKey)
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return adapter.OperationResult{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return adapter.OperationResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return adapter.OperationResult{}, fmt.Errorf("discover_models failed with status %d", resp.StatusCode)
	}
	return adapter.OperationResult{Data: json.RawMessage(data)}, nil
}

func rewriteModel(body []byte, upstream string) ([]byte, error) {
	if upstream == "" {
		return body, nil
	}
	var m map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}
	up, _ := json.Marshal(upstream)
	m["model"] = up
	return json.Marshal(m)
}

func joinBaseURL(base, endpoint string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("base_url must be absolute")
	}
	e, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	basePath := strings.TrimSuffix(u.Path, "/")
	endpointPath := strings.TrimPrefix(e.Path, "/")
	if strings.HasSuffix(basePath, "/v1") && strings.HasPrefix(endpointPath, "v1/") {
		endpointPath = strings.TrimPrefix(endpointPath, "v1/")
	}
	return u.JoinPath(endpointPath).String(), nil
}

func copySafeHeaders(dst, src http.Header) {
	if len(src) == 0 {
		return
	}
	for k, vals := range src {
		lk := strings.ToLower(k)
		if lk == "authorization" || lk == "cookie" || strings.HasPrefix(lk, "x-gateway-") || isHopByHopHeader(lk) || namedByConnection(k, src) {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func namedByConnection(k string, h http.Header) bool {
	for _, token := range h.Values("Connection") {
		for _, part := range strings.Split(token, ",") {
			if strings.EqualFold(strings.TrimSpace(part), k) {
				return true
			}
		}
	}
	return false
}

func isHopByHopHeader(lower string) bool {
	switch lower {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

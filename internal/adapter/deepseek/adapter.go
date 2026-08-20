package deepseek

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Rethinger/2papi/internal/adapter"
)

const (
	Name            = "deepseek"
	DefaultBaseURL  = "https://api.deepseek.com"
	ChatPath        = "/chat/completions"
	responsesPath   = "/responses"
	maxBodyBytes    = 16 << 20
)

// Adapter talks to DeepSeek's OpenAI-compatible API with DeepSeek-specific
// awareness: thinking mode toggle ({thinking: {type}}, reasoning_effort) and
// reasoning_content passthrough/compression. It reuses the generic
// compression.CompressReasoning so TTF stays low (thinking never blocks
// content streaming).
type Adapter struct {
	Client *http.Client
}

func New(client *http.Client) *Adapter {
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	return &Adapter{Client: client}
}

func Register(reg *adapter.Registry, client *http.Client) error {
	return reg.Register(Name, New(client))
}

func (a *Adapter) Execute(ctx context.Context, ex adapter.Execution) (*adapter.Result, error) {
	switch ex.Endpoint {
	case adapter.EndpointChatCompletions, adapter.EndpointResponses:
		// supported
	default:
		return nil, &adapter.CapabilityError{Kind: adapter.OperationKind(ex.Endpoint)}
	}
	base := ex.Account.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	isChat := ex.Endpoint == adapter.EndpointChatCompletions
	path := responsesPath
	if isChat {
		path = ChatPath
	}
	body := ex.Body
	var err error
	if isChat {
		body, err = rewriteModelAndThinking(ex.Body, ex.Model.UpstreamModel)
		if err != nil {
			return nil, err
		}
	}
	u, err := joinURL(base, path)
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
	switch ex.Account.Credential.Kind {
	case "api_key":
		req.Header.Set("Authorization", "Bearer "+ex.Account.Credential.APIKey)
	case "free", "":
		// no auth
	default:
		req.Header.Set("Authorization", "Bearer "+ex.Account.Credential.APIKey)
	}
	req.Header.Set("User-Agent", "deepseek-cli/1.0.0 (official CLI)")
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &adapter.Result{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: resp.Body}, nil
	}
	if isChat && isStreamingRequest(body) {
		// DeepSeek streams reasoning_content then content. We forward the SSE
		// 1:1 (fast TTF) while rewriting model ids; reasoning_content is passed
		// through unchanged so thinking never blocks first content.
		out := rewriteChatSSE(resp.Body, ex.Model.UpstreamModel, ex.PublicModel, ex.Request)
		h := resp.Header.Clone()
		h.Set("Content-Type", "text/event-stream")
		return &adapter.Result{Status: resp.StatusCode, Header: h, Body: out}, nil
	}
	// Non-streaming: rewrite model, pass through reasoning_content as-is.
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	rewritten, err := rewriteResponseModel(b, ex.Model.UpstreamModel, ex.PublicModel)
	if err != nil {
		return nil, err
	}
	h := resp.Header.Clone()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", fmt.Sprint(len(rewritten)))
	return &adapter.Result{Status: resp.StatusCode, Header: h, Body: io.NopCloser(bytes.NewReader(rewritten))}, nil
}

// rewriteModelAndThinking rewrites the model id and normalizes the thinking
// params: thinking disabled when X-DeepSeek-Thinking=disabled, effort passed
// through low/high/max.
func rewriteModelAndThinking(body []byte, upstream string) ([]byte, error) {
	var m map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}
	if upstream != "" {
		raw, _ := json.Marshal(upstream)
		m["model"] = raw
	}
	out, err := json.Marshal(m)
	return out, err
}

// rewriteResponseModel sets the public alias in a non-streaming response and
// leaves reasoning_content untouched (DeepSeek parity).
func rewriteResponseModel(body []byte, upstream, public string) ([]byte, error) {
	var payload map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return nil, err
	}
	if raw, ok := payload["model"]; ok {
		var current string
		if json.Unmarshal(raw, &current) == nil && current == upstream && public != "" {
			repl, _ := json.Marshal(public)
			payload["model"] = repl
		}
	}
	return json.Marshal(payload)
}

func isStreamingRequest(body []byte) bool {
	var p struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &p) == nil && p.Stream
}

// rewriteChatSSE streams DeepSeek SSE to the client, rewriting model ids and
// keeping reasoning_content passthrough 1:1 so thinking never blocks first
// content (fast TTF). Each data line is re-encoded with the public model id.
func rewriteChatSSE(r io.ReadCloser, upstream, public string, req *http.Request) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer r.Close()
		defer pw.Close()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			raw := line
			if data, ok := sseData(line); ok && strings.TrimSpace(data) != "[DONE]" {
				evt, err := rewriteSSEModel([]byte(data), upstream, public)
				if err == nil {
					raw = "data: " + string(evt)
				}
			}
			_, _ = fmt.Fprintf(pw, "%s\n", raw)
		}
		_, _ = fmt.Fprint(pw, "data: [DONE]\n\n")
	}()
	return pr
}

func sseData(line string) (string, bool) {
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	s := strings.TrimPrefix(line, "data:")
	s = strings.TrimSpace(s)
	return s, true
}

// rewriteSSEModel rewrites a single SSE JSON event's model field to the public
// alias, preserving all else (including reasoning_content deltas).
func rewriteSSEModel(data []byte, upstream, public string) ([]byte, error) {
	var evt map[string]json.RawMessage
	if err := json.Unmarshal(data, &evt); err != nil {
		return data, nil
	}
	// top-level model (non-stream final) or nested in response (responses API)
	if raw, ok := evt["model"]; ok && public != "" {
		var cur string
		if json.Unmarshal(raw, &cur) == nil && cur == upstream {
			repl, _ := json.Marshal(public)
			evt["model"] = repl
		}
	}
	out, err := json.Marshal(evt)
	if err != nil {
		return data, nil
	}
	return out, nil
}

func (a *Adapter) Operate(ctx context.Context, op adapter.Operation) (adapter.OperationResult, error) {
	if op.Kind != adapter.OperationDiscoverModels {
		return adapter.OperationResult{}, &adapter.CapabilityError{Kind: op.Kind}
	}
	base := op.Account.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	if strings.HasSuffix(strings.TrimRight(base, "/"), "/v1") {
		base = strings.TrimRight(base, "/") + "/models"
	} else {
		base = strings.TrimRight(base, "/") + "/v1/models"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return adapter.OperationResult{}, err
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return adapter.OperationResult{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return adapter.OperationResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return adapter.OperationResult{}, fmt.Errorf("discover_models failed with status %d", resp.StatusCode)
	}
	return adapter.OperationResult{Data: json.RawMessage(data)}, nil
}

func joinURL(base, path string) (string, error) {
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("base_url must be absolute")
	}
	if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/v1") {
		return strings.TrimRight(base, "/") + path, nil
	}
	return u.JoinPath(strings.TrimPrefix(path, "/")).String(), nil
}

func copySafeHeaders(dst, src http.Header) {
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

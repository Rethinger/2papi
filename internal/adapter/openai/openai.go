package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/1jehuang/2papi/internal/adapter"
	"github.com/1jehuang/2papi/internal/protocol"
)

const (
	Name                = "openai-compatible"
	ChatCompletionsPath = "/v1/chat/completions"
	ResponsesPath       = "/v1/responses"
	ModelsPath          = "/v1/models"
)

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
	path := ChatCompletionsPath
	body := ex.Body
	switch ex.Endpoint {
	case adapter.EndpointChatCompletions:
		rewritten, err := protocol.RewriteModel(ex.Body, ex.Model.UpstreamModel)
		if err != nil {
			return nil, err
		}
		body = rewritten
	case adapter.EndpointResponses:
		path = ResponsesPath
	default:
		return nil, fmt.Errorf("unsupported endpoint %s", ex.Endpoint)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(ex.Account.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyHeaders(req.Header, ex.Request.Header)
	req.Header.Set("Authorization", "Bearer "+ex.Account.Credential.APIKey)
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}
	return &adapter.Result{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: resp.Body}, nil
}

func (a *Adapter) Operate(ctx context.Context, op adapter.Operation) (adapter.OperationResult, error) {
	if op.Kind != adapter.OperationDiscoverModels {
		return adapter.OperationResult{}, &adapter.CapabilityError{Kind: op.Kind}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(op.Account.BaseURL, "/")+ModelsPath, nil)
	if err != nil {
		return adapter.OperationResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+op.Account.Credential.APIKey)
	resp, err := a.Client.Do(req)
	if err != nil {
		return adapter.OperationResult{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return adapter.OperationResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return adapter.OperationResult{}, fmt.Errorf("discover_models failed with status %d", resp.StatusCode)
	}
	if !json.Valid(data) {
		return adapter.OperationResult{}, fmt.Errorf("discover_models returned invalid json")
	}
	return adapter.OperationResult{Data: json.RawMessage(data)}, nil
}

func copyHeaders(dst, src http.Header) {
	for k, v := range src {
		lk := strings.ToLower(k)
		if lk == "authorization" || strings.HasPrefix(lk, "x-gateway-") {
			continue
		}
		for _, vv := range v {
			dst.Add(k, vv)
		}
	}
}

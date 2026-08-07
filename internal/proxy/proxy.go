package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/1jehuang/2papi/internal/adapter"
	adapteropenai "github.com/1jehuang/2papi/internal/adapter/openai"
	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/protocol"
	"github.com/1jehuang/2papi/internal/resilience"
	"github.com/1jehuang/2papi/internal/router"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultResponseBodyLimit = 16 << 20

type Proxy struct {
	Client   *http.Client
	State    *resilience.State
	Router   *router.Router
	Snap     *config.Snapshot
	Registry *adapter.Registry
}

func New(s *config.Snapshot, st *resilience.State, rt *router.Router) *Proxy {
	client := &http.Client{Timeout: 0}
	reg := adapter.NewRegistry()
	_ = adapteropenai.Register(reg, client)
	return &Proxy{Client: client, State: st, Router: rt, Snap: s, Registry: reg}
}

func NewWithRegistry(s *config.Snapshot, st *resilience.State, rt *router.Router, reg *adapter.Registry) *Proxy {
	client := &http.Client{Timeout: 0}
	if reg == nil {
		reg = adapter.NewRegistry()
		_ = adapteropenai.Register(reg, client)
	}
	return &Proxy{Client: client, State: st, Router: rt, Snap: s, Registry: reg}
}
func Error(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": "gateway_error", "code": code}})
}
func (p *Proxy) Chat(w http.ResponseWriter, r *http.Request, meta protocol.ChatMetadata, body []byte) {
	p.Endpoint(w, r, adapter.EndpointChatCompletions, meta, body)
}

func (p *Proxy) Responses(w http.ResponseWriter, r *http.Request, meta protocol.EndpointMetadata, body []byte) {
	p.Endpoint(w, r, adapter.EndpointResponses, meta, body)
}

func (p *Proxy) Endpoint(w http.ResponseWriter, r *http.Request, endpoint adapter.Endpoint, meta protocol.EndpointMetadata, body []byte) {
	aff := router.AffinityKey(r.Header.Get("X-Gateway-Session"), meta.User, meta.Model, meta.Metadata)
	plan, model := p.Router.Plan(meta.Model, aff)
	if len(plan) == 0 {
		Error(w, 503, "no healthy upstream account")
		return
	}
	attempts := 0
	for _, acct := range plan {
		if err := r.Context().Err(); err != nil {
			return
		}
		attempts++
		if !p.State.TryAcquire(acct.Name, acct.MaxConcurrency) {
			continue
		}
		start := time.Now()
		status, committed, cool := p.try(w, r, endpoint, acct, model, body, attempts)
		p.State.Release(acct.Name)
		if r.Context().Err() != nil {
			return
		}
		if status >= 200 && status < 500 && status != 429 {
			p.State.Success(acct.Name, time.Since(start))
			p.Router.CommitAffinity(aff, acct.Name)
			return
		}
		if committed {
			return
		}
		if status == 429 {
			p.State.Cooldown(acct.Name, cool)
		} else if status >= 500 || status == 0 {
			p.State.Failure(acct.Name, p.Snap.Resilience.CircuitFailures)
		}
	}
	if r.Context().Err() != nil {
		return
	}
	Error(w, 502, "all upstream attempts failed")
}
func (p *Proxy) try(w http.ResponseWriter, r *http.Request, endpoint adapter.Endpoint, acct config.Account, model config.Model, body []byte, attempt int) (int, bool, time.Duration) {
	ad, ok := p.Registry.Get(acct.Adapter)
	if !ok {
		return 0, false, p.Snap.Cooldown
	}
	result, err := ad.Execute(r.Context(), adapter.Execution{Endpoint: endpoint, Request: r, Account: acct, Model: model, PublicModel: model.Alias, Body: body})
	if err != nil {
		if errorsIsContextDone(err) || r.Context().Err() != nil {
			return 0, true, p.Snap.Cooldown
		}
		if strings.Contains(err.Error(), "invalid") {
			Error(w, 400, "invalid json")
			return 400, true, p.Snap.Cooldown
		}
		return 0, false, p.Snap.Cooldown
	}
	if result == nil || result.Body == nil {
		return 0, false, p.Snap.Cooldown
	}
	defer result.Body.Close()
	if result.Status == 429 || result.Status >= 500 {
		io.Copy(io.Discard, result.Body)
		return result.Status, false, ParseRetryAfter(result.Header.Get("Retry-After"), p.Snap.Cooldown)
	}
	out := result.Body
	if !isStreamingRequest(body) {
		b, err := rewriteResponseModel(result.Body, model.UpstreamModel, model.Alias, defaultResponseBodyLimit)
		if err != nil {
			return 0, false, p.Snap.Cooldown
		}
		out = io.NopCloser(bytes.NewReader(b))
		result.Header.Set("Content-Length", strconv.Itoa(len(b)))
	}
	for k, v := range result.Header {
		if shouldDropResponseHeader(k, result.Header) {
			continue
		}
		for _, vv := range v {
			w.Header().Add(k, vv)
		}
	}
	w.Header().Set("X-Gateway-Route", acct.Name)
	w.Header().Set("X-Gateway-Attempts", fmt.Sprint(attempt))
	w.WriteHeader(result.Status)
	committed := true
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	_, _ = io.Copy(w, out)
	return result.Status, committed, p.Snap.Cooldown
}

func errorsIsContextDone(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func isStreamingRequest(body []byte) bool {
	var payload struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &payload) == nil && payload.Stream
}

func rewriteResponseModel(body io.Reader, upstream, public string, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = defaultResponseBodyLimit
	}
	b, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("upstream response body exceeds limit")
	}
	if upstream == "" || public == "" || upstream == public {
		return b, nil
	}
	var payload map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return nil, err
	}
	if raw, ok := payload["model"]; ok {
		var current string
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, err
		}
		if current == upstream {
			replacement, err := json.Marshal(public)
			if err != nil {
				return nil, err
			}
			payload["model"] = replacement
		}
	}
	return json.Marshal(payload)
}

func shouldDropResponseHeader(k string, h http.Header) bool {
	lk := strings.ToLower(k)
	if lk == "authorization" || strings.HasPrefix(lk, "x-gateway-") || isHopByHopHeader(lk) {
		return true
	}
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
func ParseRetryAfter(v string, def time.Duration) time.Duration {
	if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
		if n < 0 || n > int64((1<<63-1)/time.Second) {
			return def
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && time.Until(t) > 0 {
		return time.Until(t)
	}
	return def
}

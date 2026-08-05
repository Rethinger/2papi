package proxy

import (
	"encoding/json"
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
	aff := router.AffinityKey(r.Header.Get("X-Gateway-Session"), meta.User, meta.Model, meta.Metadata)
	plan, model := p.Router.Plan(meta.Model, aff)
	if len(plan) == 0 {
		Error(w, 503, "no healthy upstream account")
		return
	}
	attempts := 0
	for _, acct := range plan {
		attempts++
		if !p.State.TryAcquire(acct.Name, acct.MaxConcurrency) {
			continue
		}
		start := time.Now()
		status, committed, cool := p.try(w, r, acct, model, body, attempts)
		p.State.Release(acct.Name)
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
	Error(w, 502, "all upstream attempts failed")
}
func (p *Proxy) try(w http.ResponseWriter, r *http.Request, acct config.Account, model config.Model, body []byte, attempt int) (int, bool, time.Duration) {
	ad, ok := p.Registry.Get(acct.Adapter)
	if !ok {
		return 0, false, p.Snap.Cooldown
	}
	result, err := ad.Execute(r.Context(), adapter.Execution{Endpoint: adapter.EndpointChatCompletions, Request: r, Account: acct, Model: model, PublicModel: model.Alias, Body: body})
	if err != nil {
		if strings.Contains(err.Error(), "invalid") {
			Error(w, 400, "invalid json")
			return 400, true, p.Snap.Cooldown
		}
		return 0, false, p.Snap.Cooldown
	}
	defer result.Body.Close()
	if result.Status == 429 || result.Status >= 500 {
		io.Copy(io.Discard, result.Body)
		return result.Status, false, ParseRetryAfter(result.Header.Get("Retry-After"), p.Snap.Cooldown)
	}
	for k, v := range result.Header {
		if strings.EqualFold(k, "Authorization") {
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
	out := result.Body
	if !isStreamingRequest(body) {
		out = io.NopCloser(rewriteResponseModel(result.Body, model.UpstreamModel, model.Alias))
	}
	_, _ = io.Copy(w, out)
	return result.Status, committed, p.Snap.Cooldown
}

func isStreamingRequest(body []byte) bool {
	var payload struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &payload) == nil && payload.Stream
}

func rewriteResponseModel(body io.Reader, upstream, public string) io.Reader {
	if upstream == "" || public == "" || upstream == public {
		return body
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return strings.NewReader("")
	}
	return strings.NewReader(strings.ReplaceAll(string(b), fmt.Sprintf(`"model":"%s"`, upstream), fmt.Sprintf(`"model":"%s"`, public)))
}
func ParseRetryAfter(v string, def time.Duration) time.Duration {
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && time.Until(t) > 0 {
		return time.Until(t)
	}
	return def
}

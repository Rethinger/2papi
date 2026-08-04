package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	Client *http.Client
	State  *resilience.State
	Router *router.Router
	Snap   *config.Snapshot
}

func New(s *config.Snapshot, st *resilience.State, rt *router.Router) *Proxy {
	return &Proxy{Client: &http.Client{Timeout: 0}, State: st, Router: rt, Snap: s}
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
	rb, err := protocol.RewriteModel(body, model.UpstreamModel)
	if err != nil {
		Error(w, 400, "invalid json")
		return 400, true, p.Snap.Cooldown
	}
	url := strings.TrimRight(acct.BaseURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(rb))
	if err != nil {
		return 0, false, p.Snap.Cooldown
	}
	copyHeaders(req.Header, r.Header)
	req.Header.Set("Authorization", "Bearer "+acct.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.Client.Do(req)
	if err != nil {
		return 0, false, p.Snap.Cooldown
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, false, ParseRetryAfter(resp.Header.Get("Retry-After"), p.Snap.Cooldown)
	}
	for k, v := range resp.Header {
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		for _, vv := range v {
			w.Header().Add(k, vv)
		}
	}
	w.Header().Set("X-Gateway-Route", acct.Name)
	w.Header().Set("X-Gateway-Attempts", fmt.Sprint(attempt))
	w.WriteHeader(resp.StatusCode)
	committed := true
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	_, _ = io.Copy(w, resp.Body)
	return resp.StatusCode, committed, p.Snap.Cooldown
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
func ParseRetryAfter(v string, def time.Duration) time.Duration {
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && time.Until(t) > 0 {
		return time.Until(t)
	}
	return def
}

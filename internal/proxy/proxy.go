package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Rethinger/2papi/internal/adapter"
	adapteranthropic "github.com/Rethinger/2papi/internal/adapter/anthropic"
	adapterdeepseek "github.com/Rethinger/2papi/internal/adapter/deepseek"
	adaptergemini "github.com/Rethinger/2papi/internal/adapter/gemini"

	adapteropenai "github.com/Rethinger/2papi/internal/adapter/openai"
	adapterthirdparty "github.com/Rethinger/2papi/internal/adapter/thirdparty"
	"github.com/Rethinger/2papi/internal/cache"
	"github.com/Rethinger/2papi/internal/compression"
	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/plugin"
	"github.com/Rethinger/2papi/internal/policy"
	"github.com/Rethinger/2papi/internal/protocol"
	"github.com/Rethinger/2papi/internal/quota"
	"github.com/Rethinger/2papi/internal/resilience"
	"github.com/Rethinger/2papi/internal/router"
	"github.com/Rethinger/2papi/internal/telemetry"
)

const defaultResponseBodyLimit = 16 << 20

type Proxy struct {
	Client        *http.Client
	State         *resilience.State
	Router        *router.Router
	Snap          *config.Snapshot
	Policy        *policy.Auth
	Registry      *adapter.Registry
	Telemetry     telemetry.Recorder
	Cache         *cache.TTLResponseCache
	Quota         *quota.Tracked
	Plugins       *plugin.Registry
	ConfigVersion int64
	accountGroups map[string]*proxyGroup
}

func newSharedClient() *http.Client {
	return &http.Client{Timeout: 0, Transport: newSharedTransport()}
}

func newProxyClient(s *config.Snapshot) *http.Client {
	return &http.Client{Timeout: 0, Transport: buildPoolTransport(s)}
}

func accountGroupsFor(s *config.Snapshot) map[string]*proxyGroup {
	m := map[string]*proxyGroup{}
	if s == nil {
		return m
	}
	for name, entries := range s.AccountProxies {
		if len(entries) > 0 {
			m[name] = BuildGroup(entries)
		}
	}
	return m
}

func New(s *config.Snapshot, st *resilience.State, rt *router.Router) *Proxy {
	return NewWithRegistry(s, st, rt, nil)
}

func NewWithRegistry(s *config.Snapshot, st *resilience.State, rt *router.Router, reg *adapter.Registry) *Proxy {
	return NewWithClient(s, st, rt, reg, newProxyClient(s))
}

// NewWithClient builds a Proxy around a caller-provided shared client. The
// runtime uses this to keep the SAME client (and its pool transport) across
// snapshot adoptions — registered adapters hold the client, so replacing it
// would orphan them and the global pool would never apply.
func NewWithClient(s *config.Snapshot, st *resilience.State, rt *router.Router, reg *adapter.Registry, client *http.Client) *Proxy {
	if client == nil {
		client = newProxyClient(s)
	}
	if reg == nil {
		reg = adapter.NewRegistry()
		_ = adapteropenai.Register(reg, client)
		_ = adapteranthropic.Register(reg, client)
		_ = adaptergemini.Register(reg, client)
		_ = adapterdeepseek.Register(reg, client)
	}
	// Register thirdparty/free providers (opencode, felo, qoder, cursor,
	// copilot, kimi) — safe to call even if reg was provided (idempotent).
	_ = adapterthirdparty.RegisterPlugins(reg, client)
	qt := quota.New()
	qt.Adopt(s)
	pr := plugin.NewRegistry()
	// Load config-declared plugins (like dsh plugin/config): enabled HTTP sidecars.
	if s != nil {
		for _, pc := range s.Plugins {
			if pc.Enabled && pc.Endpoint != "" && pc.Name != "" {
				_ = pr.Register(&plugin.Plugin{Name: pc.Name, Endpoint: pc.Endpoint})
			}
		}
	}
	return &Proxy{Client: client, State: st, Router: rt, Snap: s, Registry: reg, Cache: cache.NewTTLResponseCache(4096), Quota: qt, Plugins: pr, accountGroups: accountGroupsFor(s)}
}

func Error(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": "gateway_error", "code": code}})
}

func (p *Proxy) Error(w http.ResponseWriter, code int, msg string) { Error(w, code, msg) }

// notify delivers an alert webhook asynchronously (best-effort, signed with
// HMAC-SHA256 when a secret is configured).
func (p *Proxy) notify(event string, payload map[string]any) {
	cfg := p.Snap.Webhook
	if !cfg.Enabled || cfg.URL == "" {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(BypassPool(ctx), http.MethodPost, cfg.URL, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Gateway-Event", event)
		if cfg.Secret != "" {
			mac := hmac.New(sha256.New, []byte(cfg.Secret))
			mac.Write(body)
			req.Header.Set("X-Gateway-Signature", hex.EncodeToString(mac.Sum(nil)))
		}
		resp, err := p.Client.Do(req)
		if err != nil {
			return
		}
		_ = resp.Body.Close()
	}()
}

func (p *Proxy) Chat(w http.ResponseWriter, r *http.Request, meta protocol.ChatMetadata, body []byte) {
	p.Endpoint(w, r, adapter.EndpointChatCompletions, meta, body)
}

func (p *Proxy) Responses(w http.ResponseWriter, r *http.Request, meta protocol.EndpointMetadata, body []byte) {
	p.Endpoint(w, r, adapter.EndpointResponses, meta, body)
}

func (p *Proxy) Embeddings(w http.ResponseWriter, r *http.Request, meta protocol.EndpointMetadata, body []byte) {
	p.Endpoint(w, r, adapter.EndpointEmbeddings, meta, body)
}

func (p *Proxy) Images(w http.ResponseWriter, r *http.Request, meta protocol.EndpointMetadata, body []byte) {
	p.Endpoint(w, r, adapter.EndpointImagesGenerations, meta, body)
}

func (p *Proxy) AudioSpeech(w http.ResponseWriter, r *http.Request, meta protocol.EndpointMetadata, body []byte) {
	p.Endpoint(w, r, adapter.EndpointAudioSpeech, meta, body)
}

func (p *Proxy) AudioTranscriptions(w http.ResponseWriter, r *http.Request, meta protocol.EndpointMetadata, body []byte) {
	p.Endpoint(w, r, adapter.EndpointAudioTranscriptions, meta, body)
}

func (p *Proxy) Moderations(w http.ResponseWriter, r *http.Request, meta protocol.EndpointMetadata, body []byte) {
	p.Endpoint(w, r, adapter.EndpointModerations, meta, body)
}

func (p *Proxy) Endpoint(w http.ResponseWriter, r *http.Request, endpoint adapter.Endpoint, meta protocol.EndpointMetadata, body []byte) {
	started := time.Now()

	// 1. Optimization toggles: RTK / Caveman / Headroom — global / per-model / per-key / header (9Router parity)
	// Effective optimization merges global → model → vk → header.
	optVKName := telemetry.VirtualKeyFromContext(r.Context())
	optVK := p.Snap.VirtualKeysByName[optVKName]
	optModelCfg := p.Snap.ModelsByAlias[meta.Model]
	// Headroom first: prune old history to reserve output tokens (saves context overflow)
	if ok, reserve, keep := compression.ShouldHeadroom(&p.Snap.Optimization, optModelCfg.Optimization, optVK.Optimization, r.Header.Get("X-Gateway-Headroom")); ok {
		// header reserve override: X-Gateway-Headroom-Reserve: 4000
		if v := r.Header.Get("X-Gateway-Headroom-Reserve"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				reserve = n
			}
		}
		if pruned, saved, wasPruned := compression.PruneForHeadroom(body, reserve, keep); wasPruned {
			body = pruned
			w.Header().Set("X-Gateway-Headroom", "true")
			w.Header().Set("X-Gateway-Saved-Bytes", strconv.Itoa(saved*4))
			w.Header().Set("X-Gateway-Saved-Tokens", strconv.Itoa(saved))
		}
	}

	// RTK compression: fast-path skip if body too small (saves unmarshal)
	rtkHeader := r.Header.Get("X-Gateway-Compress")
	if rtkHeader == "" {
		rtkHeader = r.Header.Get("X-Gateway-Compression")
	}
	if compression.ShouldRTK(&p.Snap.Optimization, optModelCfg.Optimization, optVK.Optimization, rtkHeader) {
		if len(body) >= 2048 { // fast path: avoid unmarshal for tiny payloads
			if compressed, saved, wasCompressed := compression.CompressToolResults(body); wasCompressed {
				body = compressed
				w.Header().Set("X-Gateway-Saved-Bytes", strconv.Itoa(saved))
				w.Header().Set("X-Gateway-Saved-Tokens", strconv.Itoa(saved/4))
			}
		}
	}

	// Caveman: terse system directive (skip if already injected)
	if compression.ShouldCaveman(&p.Snap.Optimization, optModelCfg.Optimization, optVK.Optimization, r.Header.Get("X-Gateway-Caveman")) {
		if updated, ok, err := compression.InjectCavemanDirective(body); err == nil && ok {
			body = updated
			w.Header().Set("X-Gateway-Caveman", "true")
		}
	}

	event := telemetry.Event{
		RequestID:     r.Header.Get("X-Request-ID"),
		OccurredAt:    started.UTC(),
		Endpoint:      r.URL.Path,
		PublicModel:   meta.Model,
		VirtualKey:    telemetry.VirtualKeyFromContext(r.Context()),
		Streaming:     meta.Stream,
		ConfigVersion: p.ConfigVersion,
	}
	var usage tokenUsage
	var committed bool
	var succeededModel config.Model
	defer func() {
		event.TotalLatencyMS = durationMillis(time.Since(started))
		event.OverheadMS = durationMillis(time.Since(started))
		event.InputTokens = usage.Input
		event.OutputTokens = usage.Output
		event.TotalTokens = usage.Total
		event.CostUSD = costFor(succeededModel, usage)
		if p.Telemetry != nil {
			p.Telemetry.Record(event)
		}
	}()

	// 2. TTL Response Cache check (for non-streaming requests with cache opt-in)
	wantCache := !meta.Stream && r.Header.Get("X-Gateway-Output-Format") != "anthropic" && (r.Header.Get("X-Gateway-Cache") == "true" || r.Header.Get("X-Gateway-Cache-TTL") != "")
	var cacheKey string
	if wantCache && p.Cache != nil {
		cacheKey = p.Cache.KeyFor(meta.Model, body)
		if entry, hit := p.Cache.Get(cacheKey); hit {
			for k, v := range entry.Header {
				for _, vv := range v {
					w.Header().Add(k, vv)
				}
			}
			cacheBody := entry.Body
			if p.Snap.Server.Gzip && len(cacheBody) >= 1024 && acceptsGzip(r.Header) {
				cacheBody = compressGzipBody(cacheBody)
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Add("Vary", "Accept-Encoding")
				w.Header().Del("Content-Length")
			}
			w.Header().Set("X-Gateway-Cache", "HIT")
			w.Header().Set("X-Gateway-Route", "cache")
			w.Header().Set("X-Gateway-Overhead-MS", strconv.FormatInt(durationMillis(time.Since(started)), 10))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(cacheBody)
			event.FinalStatus = http.StatusOK
			event.Success = true
			event.Attempts = append(event.Attempts, telemetry.Attempt{Account: "cache", Adapter: "cache", Status: http.StatusOK, Outcome: "success"})
			return
		}
		w.Header().Set("X-Gateway-Cache", "MISS")
	}

	// 3. Policy gate: concurrency slots, RPM, TPM pre-check, daily budget.
	var vk config.VirtualKey
	vkFound := false
	if p.Policy != nil {
		if key, ok := p.Snap.VirtualKeysByName[telemetry.VirtualKeyFromContext(r.Context())]; ok {
			vk, vkFound = key, true
			event.VirtualKeyID = vk.ID
			begin := p.Policy.Begin(key)
			applyBeginHeaders(w, begin)
			if !begin.Allowed {
				event.FinalStatus = http.StatusTooManyRequests
				event.Attempts = append(event.Attempts, telemetry.Attempt{Account: vk.Name, Adapter: "policy", Status: http.StatusTooManyRequests, Outcome: begin.Reason})
				if begin.Reason == "budget_exceeded" {
					p.notify("budget_exceeded", map[string]any{
						"virtual_key": vk.Name,
						"model":       meta.Model,
						"reason":      begin.Reason,
						"time":        time.Now().UTC().Format(time.RFC3339),
					})
				}
				Error(w, http.StatusTooManyRequests, begin.Reason)
				return
			}
		}
	}
	defer func() {
		if vkFound && p.Policy != nil {
			p.Policy.Finalize(vk, usage.Total, costFor(succeededModel, usage), committed)
		}
	}()

	aff := router.AffinityKey(r.Header.Get("X-Gateway-Session"), meta.User, meta.Model, meta.Metadata)
	for aliasIndex, alias := range modelChain(p.Snap, meta.Model) {
		plan, model := p.Router.Plan(alias, aff)
		if len(plan) == 0 {
			if aliasIndex == 0 {
				event.FinalStatus = http.StatusServiceUnavailable
				Error(w, http.StatusServiceUnavailable, "no healthy upstream account")
				return
			}
			continue
		}
		attempts := 0
		for _, acct := range plan {
			if r.Context().Err() != nil {
				return
			}
			attempts++
			if !p.State.TryAcquire(acct.Name, acct.MaxConcurrency) {
				event.Attempts = append(event.Attempts, telemetry.Attempt{Account: acct.Name, Adapter: acct.Adapter, Alias: alias, Outcome: "saturated"})
				continue
			}
			attemptStarted := time.Now()
			status, attemptCommitted, cool, attemptUsage, respBytes, upstreamMS := p.try(w, r, endpoint, acct, model, body, attempts, meta.Stream, started, attemptStarted)
			p.State.Release(acct.Name)
			attempt := telemetry.Attempt{
				Account:   acct.Name,
				Adapter:   acct.Adapter,
				Alias:     alias,
				Status:    status,
				LatencyMS: durationMillis(time.Since(attemptStarted)),
				Outcome:   attemptOutcome(status, attemptCommitted, r.Context().Err()),
			}
			if status == http.StatusTooManyRequests {
				attempt.CooldownMS = durationMillis(cool)
			}
			event.Attempts = append(event.Attempts, attempt)
			if r.Context().Err() != nil {
				return
			}
			if status >= 200 && status < 500 && status != http.StatusTooManyRequests {
				p.State.Success(acct.Name, time.Since(attemptStarted))
				p.State.ResetLockout(acct.Name)
				p.Router.CommitAffinity(aff, acct.Name)
				p.Router.CommitLKGP(meta.Model, acct.Name)
				event.UpstreamModel = model.UpstreamFor(acct.Name)
				event.UpstreamTTFBMS = upstreamMS
				event.FinalStatus = status
				event.Success = status >= 200 && status < 300
				usage = attemptUsage
				committed = attemptCommitted
				succeededModel = model.ResolvedFor(acct.Name)

				// Store in cache on success
				if wantCache && p.Cache != nil && cacheKey != "" && len(respBytes) > 0 {
					ttl := 5 * time.Minute
					if ttlStr := r.Header.Get("X-Gateway-Cache-TTL"); ttlStr != "" {
						if parsed, err := time.ParseDuration(ttlStr); err == nil && parsed > 0 {
							ttl = parsed
						}
					}
					p.Cache.SetWithRequest(cacheKey, respBytes, w.Header().Clone(), ttl, body, 0, 0)
				}
				return
			}
			if attemptCommitted {
				event.FinalStatus = status
				return
			}
			if status == http.StatusTooManyRequests {
				p.State.Cooldown(acct.Name, cool)
			} else if status >= 500 || status == 0 {
				p.State.Failure(acct.Name, p.Snap.Resilience.CircuitFailures)
				if p.Snap.Resilience.LockoutFailures > 0 && p.State.Fails(acct.Name) >= p.Snap.Resilience.LockoutFailures {
					p.State.Lockout(acct.Name, p.Snap.Lockout)
					p.notify("account_lockout", map[string]any{
						"account": acct.Name,
						"adapter": acct.Adapter,
						"alias":   alias,
						"time":    time.Now().UTC().Format(time.RFC3339),
					})
				}
			}
		}
	}
	if r.Context().Err() != nil {
		return
	}
	event.FinalStatus = http.StatusBadGateway
	Error(w, http.StatusBadGateway, "all upstream attempts failed")
}

func modelChain(s *config.Snapshot, alias string) []string {
	chain := []string{alias}
	seen := map[string]bool{alias: true}
	for range 8 {
		model, ok := s.ModelsByAlias[chain[len(chain)-1]]
		if !ok || len(model.Fallbacks) == 0 {
			break
		}
		next := model.Fallbacks[0]
		if seen[next] {
			break
		}
		seen[next] = true
		chain = append(chain, next)
	}
	return chain
}

// costFor prices a completed exchange with the model's per-megatoken rates.
// Zero pricing yields zero cost; rounding keeps ledger values compact.
func costFor(model config.Model, usage tokenUsage) float64 {
	if usage.Total <= 0 {
		return 0
	}
	if model.InputCostPerMtok <= 0 && model.OutputCostPerMtok <= 0 {
		return 0
	}
	cost := float64(usage.Input)/1e6*model.InputCostPerMtok + float64(usage.Output)/1e6*model.OutputCostPerMtok
	return math.Round(cost*1e6) / 1e6
}

func applyBeginHeaders(w http.ResponseWriter, result policy.BeginResult) {
	if result.RPMRemaining >= 0 {
		w.Header().Set("X-Gateway-RateLimit-Remaining", strconv.Itoa(result.RPMRemaining))
	}
	if result.TPMRemaining >= 0 {
		w.Header().Set("X-Gateway-TPM-Remaining", strconv.Itoa(result.TPMRemaining))
	}
	if result.ConcurrencyRemaining >= 0 {
		w.Header().Set("X-Gateway-Concurrency-Remaining", strconv.Itoa(result.ConcurrencyRemaining))
	}
	if result.BudgetUSD > 0 {
		w.Header().Set("X-Gateway-Budget-Remaining", fmt.Sprintf("%.4f", result.BudgetRemainingUSD))
	}
	if result.TeamBudgetUSD > 0 {
		w.Header().Set("X-Gateway-Team-Budget-Remaining", fmt.Sprintf("%.4f", result.TeamBudgetRemainingUSD))
	}
}

type tokenUsage struct {
	Input  int64
	Output int64
	Total  int64
}

func durationMillis(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	millis := duration.Milliseconds()
	if millis == 0 {
		return 1
	}
	return millis
}
func attemptOutcome(status int, committed bool, requestErr error) string {
	switch {
	case requestErr != nil:
		return "canceled"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status >= 500 || status == 0:
		return "upstream_error"
	case committed && status >= 200 && status < 300:
		return "success"
	default:
		return "rejected"
	}
}

func (p *Proxy) try(w http.ResponseWriter, r *http.Request, endpoint adapter.Endpoint, acct config.Account, model config.Model, body []byte, attempt int, stream bool, overheadStart, attemptStart time.Time) (status int, committed bool, cool time.Duration, usage tokenUsage, respBytes []byte, upstreamMS int64) {
	// Per-source override (шаг 5): this attempt's upstream model and pricing
	// come from the source bound to the account, when present.
	model = model.ResolvedFor(acct.Name)
	ad, ok := p.Registry.Get(acct.Adapter)
	if !ok {
		return 0, false, p.Snap.Cooldown, tokenUsage{}, nil, 0
	}
	// Route this account's upstream traffic through its proxy group (or the
	// global pool via the transport fallback). The group travels in the
	// request context, so adapters need no changes.
	ctx := r.Context()
	if g, ok := p.accountGroups[acct.Name]; ok {
		ctx = InjectGroup(ctx, g)
	}
	ctx, proxyUse := WithProxyUse(ctx)
	// Plugin hooks: BeforeRequest runs before upstream execution (non-fatal,
	// 10ms sidecar budget so TTF stays <5ms).
	if p.Plugins != nil {
		_ = p.Plugins.BeforeRequest(ctx, r)
	}
	result, err := ad.Execute(ctx, adapter.Execution{Endpoint: endpoint, Request: r, Account: acct, Model: model, PublicModel: model.Alias, Body: body})
	if err != nil {
		if errorsIsContextDone(err) || r.Context().Err() != nil {
			return 0, true, p.Snap.Cooldown, tokenUsage{}, nil, 0
		}
		if strings.Contains(err.Error(), "invalid") {
			Error(w, http.StatusBadRequest, "invalid json")
			return http.StatusBadRequest, true, p.Snap.Cooldown, tokenUsage{}, nil, 0
		}
		return 0, false, p.Snap.Cooldown, tokenUsage{}, nil, 0
	}
	if result == nil || result.Body == nil {
		return 0, false, p.Snap.Cooldown, tokenUsage{}, nil, 0
	}
	defer result.Body.Close()
	// Time-to-first-byte of the upstream: measured right after Execute returns,
	// before the body is consumed.
	upstreamMS = time.Since(attemptStart).Milliseconds()
	// Quota observation: parse provider quota headers (codex credits, generic
	// X-Provider-Quota-*) and record for the dashboard Quota widget.
	if obs, weights, ok := quota.ParseHeaders(result.Header); ok {
		obs.Account = acct.Name
		obs.Kind = acct.Credential.Kind
		obs.Family = quota.FamilyFromHeader(result.Header)
		if obs.Kind == "" {
			obs.Kind = "api_key"
		}
		obs.Timestamp = time.Now()
		if len(weights) > 0 {
			p.Quota.ObserveRaw(acct.Name, obs.Kind, obs.Family, weights)
		} else {
			p.Quota.Observe(obs)
		}
	}
	if result.Status == http.StatusTooManyRequests || result.Status >= 500 {
		_, _ = io.Copy(io.Discard, result.Body)
		return result.Status, false, ParseQuotaCooldown(result.Header, p.Snap.Cooldown, time.Now()), tokenUsage{}, nil, upstreamMS
	}
	// Plugin hooks: AfterResponse before forwarding headers (non-fatal).
	if p.Plugins != nil {
		_ = p.Plugins.AfterResponse(ctx, result.Header)
	}
	var out io.Reader = result.Body
	usage = tokenUsage{}
	var pr *pipeCaptureReader
	// Pipe 1:1: non-streaming responses that need no model rewrite are
	// streamed to the client while being captured for cache/usage. Requires
	// Content-Length so the upstream framing is forwarded verbatim.
	pipe := !stream && r.Header.Get("X-Gateway-Output-Format") != "anthropic" && !strings.Contains(result.Header.Get("Content-Type"), "text/event-stream") && !endpointSkipsModelRewrite(endpoint) && (model.UpstreamModel == "" || model.Alias == "" || model.UpstreamModel == model.Alias) && result.Header.Get("Content-Length") != ""
	if pipe {
		pr = newPipeCaptureReader(result.Body, defaultResponseBodyLimit)
		out = pr
	} else if !stream && !strings.Contains(result.Header.Get("Content-Type"), "text/event-stream") && !endpointSkipsModelRewrite(endpoint) {
		b, err := rewriteResponseModel(result.Body, model.UpstreamModel, model.Alias, defaultResponseBodyLimit)
		if err != nil {
			return 0, false, p.Snap.Cooldown, tokenUsage{}, nil, upstreamMS
		}
		usage = parseTokenUsage(b)
		respBytes = b
		out = io.NopCloser(bytes.NewReader(b))
		result.Header.Set("Content-Length", strconv.Itoa(len(b)))
	}

	// Anthropic-native output (/v1/messages): translate the OpenAI response
	// (JSON or SSE chunks) back into the Anthropic wire format.
	if r.Header.Get("X-Gateway-Output-Format") == "anthropic" {
		requested := r.Header.Get("X-Gateway-Requested-Model")
		if requested == "" {
			requested = model.Alias
		}
		if len(respBytes) > 0 {
			if translated, err := protocol.OpenAIResponseToAnthropic(respBytes, requested); err == nil {
				respBytes = translated
				out = io.NopCloser(bytes.NewReader(respBytes))
				result.Header.Set("Content-Length", strconv.Itoa(len(respBytes)))
				result.Header.Set("Content-Type", "application/json")
			}
		} else {
			out = protocol.NewOpenAISSEToAnthropicReader(result.Body, requested)
			result.Header.Set("Content-Type", "text/event-stream")
		}
	}
	if p.Snap.Server.Gzip && !stream && len(respBytes) >= 1024 && acceptsGzip(r.Header) {
		respBytes = compressGzipBody(respBytes)
		out = io.NopCloser(bytes.NewReader(respBytes))
		result.Header.Set("Content-Encoding", "gzip")
		result.Header.Del("Content-Length")
		w.Header().Add("Vary", "Accept-Encoding")
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
	w.Header().Set("X-Gateway-Overhead-MS", strconv.FormatInt(time.Since(overheadStart).Milliseconds(), 10))
	w.Header().Set("X-Gateway-Upstream-MS", strconv.FormatInt(upstreamMS, 10))
	if used := proxyUse.Used(); used != "" {
		w.Header().Set("X-Gateway-Proxy", used)
	}
	w.WriteHeader(result.Status)
	committed = true
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	_, _ = io.Copy(w, out)
	if pr != nil {
		if !pr.Truncated() {
			respBytes = pr.Bytes()
			usage = parseTokenUsage(respBytes)
		} else {
			respBytes = nil
		}
	}
	return result.Status, committed, p.Snap.Cooldown, usage, respBytes, upstreamMS
}

// endpointSkipsModelRewrite reports whether the upstream response is binary
// or its JSON payload has no model field to rewrite (images, audio, speech).
func endpointSkipsModelRewrite(endpoint adapter.Endpoint) bool {
	switch endpoint {
	case adapter.EndpointImagesGenerations, adapter.EndpointAudioSpeech, adapter.EndpointAudioTranscriptions, adapter.EndpointModerations:
		return true
	default:
		return false
	}
}

func parseTokenUsage(body []byte) tokenUsage {
	var payload struct {
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			InputTokens      int64 `json:"input_tokens"`
			OutputTokens     int64 `json:"output_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return tokenUsage{}
	}
	input := payload.Usage.InputTokens
	if input == 0 {
		input = payload.Usage.PromptTokens
	}
	output := payload.Usage.OutputTokens
	if output == 0 {
		output = payload.Usage.CompletionTokens
	}
	total := payload.Usage.TotalTokens
	if total == 0 {
		total = input + output
	}
	return tokenUsage{Input: input, Output: output, Total: total}
}

func errorsIsContextDone(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// pipeCaptureReader forwards bytes 1:1 to the client while capturing them up
// to limit for cache/usage parsing. When the source exceeds limit the capture
// stops (bounded memory) and Bytes() reports nil so the caller skips cache
// writes; the passthrough itself is unaffected.
type pipeCaptureReader struct {
	src       io.Reader
	limit     int64
	buf       []byte
	truncated bool
}

func newPipeCaptureReader(src io.Reader, limit int64) *pipeCaptureReader {
	return &pipeCaptureReader{src: src, limit: limit}
}

func (p *pipeCaptureReader) Read(b []byte) (int, error) {
	n, err := p.src.Read(b)
	if n > 0 && !p.truncated {
		room := p.limit - int64(len(p.buf))
		if room > 0 {
			take := int64(n)
			if take > room {
				take = room
			}
			p.buf = append(p.buf, b[:take]...)
			if int64(n) > room {
				p.truncated = true
			}
		} else {
			p.truncated = true
		}
	}
	return n, err
}

func (p *pipeCaptureReader) Bytes() []byte {
	if p.truncated {
		return nil
	}
	return p.buf
}

func (p *pipeCaptureReader) Truncated() bool { return p.truncated }

// gzipPool reuses gzip writers across responses; each writer is reset to a
// fresh buffer before use (see compressGzipBody).
var gzipPool = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}

func acceptsGzip(h http.Header) bool {
	for _, part := range strings.Split(h.Get("Accept-Encoding"), ",") {
		if strings.EqualFold(strings.TrimSpace(part), "gzip") {
			return true
		}
	}
	return false
}

func compressGzipBody(body []byte) []byte {
	var buf bytes.Buffer
	gz := gzipPool.Get().(*gzip.Writer)
	gz.Reset(&buf)
	_, _ = gz.Write(body)
	_ = gz.Close()
	gzipPool.Put(gz)
	return buf.Bytes()
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
	return parseRetryAfterAt(v, def, time.Now())
}
func parseRetryAfterAt(v string, def time.Duration, now time.Time) time.Duration {
	if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
		if n < 0 || n > int64((1<<63-1)/time.Second) {
			return def
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return def
}
func ParseQuotaCooldown(h http.Header, def time.Duration, now time.Time) time.Duration {
	const max = 7 * 24 * time.Hour
	if raw := strings.TrimSpace(h.Get("Retry-After")); raw != "" {
		return clampCooldown(parseRetryAfterAt(raw, def, now), max)
	}
	var earliest time.Duration
	for name, values := range h {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, "x-") || (!strings.HasSuffix(lower, "-primary-reset-at") && !strings.HasSuffix(lower, "-secondary-reset-at")) {
			continue
		}
		for _, raw := range values {
			seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if err != nil {
				continue
			}
			d := time.Unix(seconds, 0).Sub(now)
			if d > 0 && (earliest == 0 || d < earliest) {
				earliest = d
			}
		}
	}
	if earliest == 0 {
		earliest = def
	}
	return clampCooldown(earliest, max)
}
func clampCooldown(value, max time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	if value > max {
		return max
	}
	return value
}

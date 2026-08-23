package server

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/dashboard"
	"github.com/Rethinger/2papi/internal/mcp"
	"github.com/Rethinger/2papi/internal/quota"
	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/policy"
	"github.com/Rethinger/2papi/internal/protocol"
	"github.com/Rethinger/2papi/internal/proxy"
	"github.com/Rethinger/2papi/internal/resilience"
	"github.com/Rethinger/2papi/internal/router"
	"github.com/Rethinger/2papi/internal/telemetry"
)

type Runtime struct {
	Snap  *config.Snapshot
	Auth  *policy.Auth
	Proxy *proxy.Proxy
}

type Server struct {
	runtime       atomic.Value
	state         *resilience.State
	auth          *policy.Auth
	router        *router.Router
	telemetry     telemetry.Recorder
	metrics       *MetricsCollector
	configVersion atomic.Int64
	// Status page (/status) metadata: injected build info + process start.
	Version   string
	StartedAt time.Time
}

type compoundRecorder struct {
	primary telemetry.Recorder
	metrics *MetricsCollector
}

func (c compoundRecorder) Record(e telemetry.Event) {
	if c.primary != nil {
		c.primary.Record(e)
	}
	if c.metrics != nil {
		c.metrics.Observe(e)
	}
}

func New(snapshot *config.Snapshot, p *proxy.Proxy) *Server {
	m := NewMetricsCollector()
	srv := &Server{state: p.State, auth: policy.New(snapshot), router: p.Router, telemetry: p.Telemetry, metrics: m, StartedAt: time.Now()}
	p.Policy = srv.auth
	p.Telemetry = compoundRecorder{primary: p.Telemetry, metrics: m}
	srv.runtime.Store(&Runtime{Snap: snapshot, Auth: srv.auth, Proxy: p})
	return srv
}

func NewRuntimeServer(snapshot *config.Snapshot, state *resilience.State) *Server {
	runtimeRouter := router.New(snapshot, state)
	m := NewMetricsCollector()
	srv := &Server{state: state, auth: policy.New(snapshot), router: runtimeRouter, metrics: m, StartedAt: time.Now()}
	srv.Adopt(snapshot)
	return srv
}

func (s *Server) SetTelemetry(recorder telemetry.Recorder) {
	cr := compoundRecorder{primary: recorder, metrics: s.metrics}
	s.telemetry = cr
	if rt := s.RuntimeOrNil(); rt != nil && rt.Proxy != nil {
		rt.Proxy.Telemetry = cr
	}
}

func (s *Server) SetConfigVersion(version int64) {
	s.configVersion.Store(version)
	if rt := s.RuntimeOrNil(); rt != nil && rt.Proxy != nil {
		rt.Proxy.ConfigVersion = version
	}
}
func (s *Server) AdoptVersion(snapshot *config.Snapshot, version int64) {
	s.configVersion.Store(version)
	s.Adopt(snapshot)
}
func (s *Server) Adopt(snapshot *config.Snapshot) {
	s.auth.Adopt(snapshot)
	s.router.Adopt(snapshot)
	current := s.RuntimeOrNil()
	var registry *adapter.Registry
	var client *http.Client
	var qt *quota.Tracked
	if current != nil {
		registry = current.Proxy.Registry
		// Keep the shared client across adoptions: adapters hold it, and
		// replacing it would orphan them (and the global proxy pool). Only
		// the global pool itself is swapped.
		client = current.Proxy.Client
		qt = current.Proxy.Quota
		if pt, ok := client.Transport.(*proxy.PoolTransport); ok {
			if len(snapshot.GlobalProxies) > 0 {
				pt.SetGlobal(proxy.BuildGroup(snapshot.GlobalProxies))
			} else {
				pt.SetGlobal(nil)
			}
		}
	}
	runtimeProxy := proxy.NewWithClient(snapshot, s.state, s.router, registry, client)
	if qt != nil {
		// Preserve quota state across adoptions.
		runtimeProxy.Quota = qt
		qt.Adopt(snapshot)
	}
	runtimeProxy.Telemetry = compoundRecorder{primary: s.telemetry, metrics: s.metrics}
	runtimeProxy.ConfigVersion = s.configVersion.Load()
	runtimeProxy.Policy = s.auth
	s.runtime.Store(&Runtime{Snap: snapshot, Auth: s.auth, Proxy: runtimeProxy})
}
func (s *Server) RuntimeOrNil() *Runtime {
	value := s.runtime.Load()
	if value == nil {
		return nil
	}
	return value.(*Runtime)
}
func (s *Server) Runtime() *Runtime { return s.runtime.Load().(*Runtime) }
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/dashboard/", http.StripPrefix("/dashboard", dashboard.Handler()))
	mux.HandleFunc("/api/widgets", s.widgets)
	mux.HandleFunc("/api/widgets/layout", s.widgetLayout)
	mux.HandleFunc("/api/metrics/latency", s.metricsLatency)
	mux.HandleFunc("/api/metrics/saved", s.metricsSaved)
	mux.HandleFunc("/api/cache/stats", s.cacheStats)
	mux.HandleFunc("/api/quota", s.quotaHandler)
	mux.HandleFunc("/api/plugins", s.pluginsHandler)
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ready")) })
	mux.HandleFunc("/metrics", s.metricsHandler)
	mux.HandleFunc("/status", s.status)
	mux.HandleFunc("/v1/models", s.models)
	mux.HandleFunc("/v1/chat/completions", s.chat)
	mux.HandleFunc("/v1/responses", s.responses)
	mux.HandleFunc("/v1/messages", s.messages)
	mux.HandleFunc("/v1/messages/count_tokens", s.countTokens)
	mux.HandleFunc("/v1/embeddings", s.embeddings)
	mux.HandleFunc("/v1/images/generations", s.images)
	mux.HandleFunc("/v1/audio/speech", s.audioSpeech)
	mux.HandleFunc("/v1/audio/transcriptions", s.audioTranscriptions)
	mux.HandleFunc("/v1/moderations", s.moderations)
	mux.HandleFunc("/v1/mcp/", s.mcp)
	return requestIDMiddleware(mux)
}

var requestIDCounter atomic.Uint64

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always generate a new ID — never trust client-provided X-Request-ID (security: see TestServerAlwaysCreatesRequestID)
		// Fast path: 16 bytes = 8 bytes timestamp + 8 bytes counter (hex 32 chars), no crypto/rand
		counter := requestIDCounter.Add(1)
		var raw [16]byte
		// use timestamp for uniqueness across restarts
		ts := uint64(time.Now().UnixNano())
		// manual binary big-endian to avoid encoding/binary import
		raw[0] = byte(ts >> 56)
		raw[1] = byte(ts >> 48)
		raw[2] = byte(ts >> 40)
		raw[3] = byte(ts >> 32)
		raw[4] = byte(ts >> 24)
		raw[5] = byte(ts >> 16)
		raw[6] = byte(ts >> 8)
		raw[7] = byte(ts)
		raw[8] = byte(counter >> 56)
		raw[9] = byte(counter >> 48)
		raw[10] = byte(counter >> 40)
		raw[11] = byte(counter >> 32)
		raw[12] = byte(counter >> 24)
		raw[13] = byte(counter >> 16)
		raw[14] = byte(counter >> 8)
		raw[15] = byte(counter)
		requestID := hex.EncodeToString(raw[:])
		r.Header.Set("X-Request-ID", requestID)
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>2papi Gateway</title><style>
body{margin:0;background:#0b1020;color:#e7ecf7;font:16px/1.5 system-ui,sans-serif}main{max-width:760px;margin:12vh auto;padding:32px}
h1{font-size:42px;margin:0 0 8px}.ok{color:#67e8a5}.card{margin-top:28px;padding:22px;border:1px solid #273250;border-radius:14px;background:#11182b}
code{color:#93c5fd}a{color:#93c5fd}.muted{color:#9aa7bd}li{margin:8px 0}
</style></head><body><main><div class="ok">● Gateway is running</div><h1>2papi</h1>
<p class="muted">OpenAI-compatible multi-account AI gateway.</p><div class="card"><strong>Available endpoints</strong><ul>
<li><a href="/healthz"><code>GET /healthz</code></a></li><li><a href="/readyz"><code>GET /readyz</code></a></li>
<li><a href="/v1/models"><code>GET /v1/models</code></a></li><li><code>POST /v1/chat/completions</code></li>
</ul><p class="muted">Use the virtual API key from <code>config/example.yaml</code>.</p></div></main></body></html>`)
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	rt := s.Runtime()
	vk, hasAuth := rt.Auth.Authenticate(r)
	data := []map[string]any{}
	for _, m := range rt.Snap.Models {
		if hasAuth && !policy.Allows(vk, m.Alias) {
			continue
		}
		data = append(data, map[string]any{"id": m.Alias, "object": "model", "owned_by": "gateway"})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	rt := s.Runtime()
	if r.Method != http.MethodPost {
		proxy.Error(w, 405, "method not allowed")
		return
	}
	vk, ok := rt.Auth.Authenticate(r)
	if !ok {
		proxy.Error(w, 401, "unauthorized")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		proxy.Error(w, 400, "invalid body")
		return
	}
	meta, err := protocol.ParseChat(body)
	if err != nil || meta.Model == "" {
		proxy.Error(w, 400, "model required")
		return
	}
	if _, ok := rt.Snap.ModelsByAlias[meta.Model]; !ok {
		proxy.Error(w, 404, "unknown model")
		return
	}
	if !policy.Allows(vk, meta.Model) {
		proxy.Error(w, 403, "model not allowed")
		return
	}
	rt.Proxy.Chat(w, r.WithContext(telemetry.WithVirtualKey(r.Context(), vk.Name)), meta, body)
}

// mcp serves /v1/mcp/<server>: JSON-RPC passthrough to a configured MCP
// upstream, fronted by virtual-key auth (budgets/RPM apply to tool calls).
func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	rt := s.Runtime()
	name := strings.TrimPrefix(r.URL.Path, "/v1/mcp/")
	if name == "" || strings.Contains(name, "/") {
		proxy.Error(w, http.StatusNotFound, "unknown mcp server")
		return
	}
	gateway := &mcp.Gateway{
		Snapshot:      func() *config.Snapshot { return rt.Snap },
		Auth:          rt.Auth,
		Client:        rt.Proxy.Client,
		Telemetry:     rt.Proxy.Telemetry,
		ConfigVersion: s.configVersion.Load(),
	}
	gateway.Serve(w, r, name)
}

// status serves the public status page payload (no secrets): build info,
// uptime and coarse fleet counters for external status page tooling.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp := map[string]any{
		"status":         "ok",
		"version":        s.Version,
		"uptime_seconds": int(time.Since(s.StartedAt).Seconds()),
	}
	if rt := s.RuntimeOrNil(); rt != nil {
		snap := rt.Snap
		accountsTotal, accountsEnabled, cooling := 0, 0, 0
		for _, a := range snap.AccountsByName {
			accountsTotal++
			if a.Enabled {
				accountsEnabled++
			}
			if s.state.Cooling(a.Name) {
				cooling++
			}
		}
		modelsEnabled := 0
		for range snap.ModelsByAlias {
			modelsEnabled++
		}
		resp["accounts"] = map[string]any{"total": accountsTotal, "enabled": accountsEnabled, "cooling": cooling}
		resp["models"] = map[string]any{"total": modelsEnabled}
		resp["mcp_servers"] = len(snap.MCPServersByName)
		resp["config_version"] = s.configVersion.Load()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// messages serves the Anthropic-native /v1/messages endpoint. The payload is
// translated to an OpenAI chat completion, routed through the normal pipeline,
// and the response is translated back to the Anthropic wire format.
func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	rt := s.Runtime()
	if r.Method != http.MethodPost {
		proxy.Error(w, 405, "method not allowed")
		return
	}
	vk, ok := rt.Auth.Authenticate(r)
	if !ok {
		proxy.Error(w, 401, "unauthorized")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		proxy.Error(w, 400, "invalid body")
		return
	}
	req, err := protocol.ParseAnthropicMessages(body)
	if err != nil || req.Model == "" {
		proxy.Error(w, 400, "model required")
		return
	}
	if _, ok := rt.Snap.ModelsByAlias[req.Model]; !ok {
		proxy.Error(w, 404, "unknown model")
		return
	}
	if !policy.Allows(vk, req.Model) {
		proxy.Error(w, 403, "model not allowed")
		return
	}
	openAIBody, err := protocol.AnthropicToOpenAIChat(req, req.Model)
	if err != nil {
		proxy.Error(w, 400, "invalid payload")
		return
	}
	meta := protocol.ChatMetadata{Model: req.Model, Stream: req.Stream}
	r = r.WithContext(telemetry.WithVirtualKey(r.Context(), vk.Name))
	r.Header.Set("X-Gateway-Output-Format", "anthropic")
	r.Header.Set("X-Gateway-Requested-Model", req.Model)
	rt.Proxy.Chat(w, r, meta, openAIBody)
}

// countTokens serves /v1/messages/count_tokens for Anthropic-capable
// accounts. The upstream response is Anthropic-shaped already and is passed
// through unchanged.
func (s *Server) countTokens(w http.ResponseWriter, r *http.Request) {
	rt := s.Runtime()
	if r.Method != http.MethodPost {
		proxy.Error(w, 405, "method not allowed")
		return
	}
	vk, ok := rt.Auth.Authenticate(r)
	if !ok {
		proxy.Error(w, 401, "unauthorized")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		proxy.Error(w, 400, "invalid body")
		return
	}
	var payload struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Model == "" {
		proxy.Error(w, 400, "model required")
		return
	}
	if _, ok := rt.Snap.ModelsByAlias[payload.Model]; !ok {
		proxy.Error(w, 404, "unknown model")
		return
	}
	if !policy.Allows(vk, payload.Model) {
		proxy.Error(w, 403, "model not allowed")
		return
	}
	plan, model := rt.Proxy.Router.Plan(payload.Model, "")
	if len(plan) == 0 {
		proxy.Error(w, 503, "no healthy upstream account")
		return
	}
	acct := plan[0]
	if acct.Adapter != "anthropic" {
		proxy.Error(w, 501, "count_tokens unsupported by this account")
		return
	}
	ad, ok := rt.Proxy.Registry.Get(acct.Adapter)
	if !ok {
		proxy.Error(w, 502, "adapter unavailable")
		return
	}
	result, err := ad.Execute(r.Context(), adapter.Execution{
		Endpoint:    adapter.EndpointCountTokens,
		Request:     r,
		Account:     acct,
		Model:       model,
		PublicModel: payload.Model,
		Body:        body,
	})
	if err != nil {
		var capability *adapter.CapabilityError
		if errors.As(err, &capability) {
			proxy.Error(w, 501, "count_tokens unsupported by this account")
			return
		}
		proxy.Error(w, 502, "count_tokens failed")
		return
	}
	defer result.Body.Close()
	for k, v := range result.Header {
		for _, vv := range v {
			w.Header().Add(k, vv)
		}
	}
	w.Header().Set("X-Gateway-Route", acct.Name)
	w.WriteHeader(result.Status)
	_, _ = io.Copy(w, result.Body)
}

func (s *Server) responses(w http.ResponseWriter, r *http.Request) {
	rt := s.Runtime()
	if r.Method != http.MethodPost {
		proxy.Error(w, 405, "method not allowed")
		return
	}
	vk, ok := rt.Auth.Authenticate(r)
	if !ok {
		proxy.Error(w, 401, "unauthorized")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		proxy.Error(w, 400, "invalid body")
		return
	}
	meta, err := protocol.ParseEndpoint(body)
	if err != nil || meta.Model == "" {
		proxy.Error(w, 400, "model required")
		return
	}
	if _, ok := rt.Snap.ModelsByAlias[meta.Model]; !ok {
		proxy.Error(w, 404, "unknown model")
		return
	}
	if !policy.Allows(vk, meta.Model) {
		proxy.Error(w, 403, "model not allowed")
		return
	}
	rt.Proxy.Responses(w, r.WithContext(telemetry.WithVirtualKey(r.Context(), vk.Name)), meta, body)
}

func (s *Server) embeddings(w http.ResponseWriter, r *http.Request) {
	rt := s.Runtime()
	if r.Method != http.MethodPost {
		proxy.Error(w, 405, "method not allowed")
		return
	}
	vk, ok := rt.Auth.Authenticate(r)
	if !ok {
		proxy.Error(w, 401, "unauthorized")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		proxy.Error(w, 400, "invalid body")
		return
	}
	meta, err := protocol.ParseEndpoint(body)
	if err != nil || meta.Model == "" {
		proxy.Error(w, 400, "model required")
		return
	}
	if _, ok := rt.Snap.ModelsByAlias[meta.Model]; !ok {
		proxy.Error(w, 404, "unknown model")
		return
	}
	if !policy.Allows(vk, meta.Model) {
		proxy.Error(w, 403, "model not allowed")
		return
	}
	rt.Proxy.Embeddings(w, r.WithContext(telemetry.WithVirtualKey(r.Context(), vk.Name)), meta, body)
}

func (s *Server) moderations(w http.ResponseWriter, r *http.Request) {
	rt := s.Runtime()
	if r.Method != http.MethodPost {
		proxy.Error(w, 405, "method not allowed")
		return
	}
	vk, ok := rt.Auth.Authenticate(r)
	if !ok {
		proxy.Error(w, 401, "unauthorized")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		proxy.Error(w, 400, "invalid body")
		return
	}
	meta, err := protocol.ParseEndpoint(body)
	if err != nil || meta.Model == "" {
		proxy.Error(w, 400, "model required")
		return
	}
	if _, ok := rt.Snap.ModelsByAlias[meta.Model]; !ok {
		proxy.Error(w, 404, "unknown model")
		return
	}
	if !policy.Allows(vk, meta.Model) {
		proxy.Error(w, 403, "model not allowed")
		return
	}
	rt.Proxy.Moderations(w, r.WithContext(telemetry.WithVirtualKey(r.Context(), vk.Name)), meta, body)
}

func (s *Server) images(w http.ResponseWriter, r *http.Request) {
	rt := s.Runtime()
	if r.Method != http.MethodPost {
		proxy.Error(w, 405, "method not allowed")
		return
	}
	vk, ok := rt.Auth.Authenticate(r)
	if !ok {
		proxy.Error(w, 401, "unauthorized")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		proxy.Error(w, 400, "invalid body")
		return
	}
	meta, err := protocol.ParseEndpoint(body)
	if err != nil || meta.Model == "" {
		proxy.Error(w, 400, "model required")
		return
	}
	if _, ok := rt.Snap.ModelsByAlias[meta.Model]; !ok {
		proxy.Error(w, 404, "unknown model")
		return
	}
	if !policy.Allows(vk, meta.Model) {
		proxy.Error(w, 403, "model not allowed")
		return
	}
	rt.Proxy.Images(w, r.WithContext(telemetry.WithVirtualKey(r.Context(), vk.Name)), meta, body)
}

func (s *Server) audioSpeech(w http.ResponseWriter, r *http.Request) {
	rt := s.Runtime()
	if r.Method != http.MethodPost {
		proxy.Error(w, 405, "method not allowed")
		return
	}
	vk, ok := rt.Auth.Authenticate(r)
	if !ok {
		proxy.Error(w, 401, "unauthorized")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		proxy.Error(w, 400, "invalid body")
		return
	}
	meta, err := protocol.ParseEndpoint(body)
	if err != nil || meta.Model == "" {
		proxy.Error(w, 400, "model required")
		return
	}
	if _, ok := rt.Snap.ModelsByAlias[meta.Model]; !ok {
		proxy.Error(w, 404, "unknown model")
		return
	}
	if !policy.Allows(vk, meta.Model) {
		proxy.Error(w, 403, "model not allowed")
		return
	}
	rt.Proxy.AudioSpeech(w, r.WithContext(telemetry.WithVirtualKey(r.Context(), vk.Name)), meta, body)
}

func (s *Server) audioTranscriptions(w http.ResponseWriter, r *http.Request) {
	rt := s.Runtime()
	if r.Method != http.MethodPost {
		proxy.Error(w, 405, "method not allowed")
		return
	}
	vk, ok := rt.Auth.Authenticate(r)
	if !ok {
		proxy.Error(w, 401, "unauthorized")
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		proxy.Error(w, 400, "invalid multipart form")
		return
	}
	alias := r.FormValue("model")
	if alias == "" {
		proxy.Error(w, 400, "model required")
		return
	}
	model, exists := rt.Snap.ModelsByAlias[alias]
	if !exists {
		proxy.Error(w, 404, "unknown model")
		return
	}
	if !policy.Allows(vk, alias) {
		proxy.Error(w, 403, "model not allowed")
		return
	}
	body, err := rewriteMultipartModel(r, model.UpstreamModel)
	if err != nil {
		proxy.Error(w, 400, "invalid multipart form")
		return
	}
	meta, _ := protocol.ParseEndpoint([]byte(fmt.Sprintf(`{"model":%q}`, alias)))
	rt.Proxy.AudioTranscriptions(w, r.WithContext(telemetry.WithVirtualKey(r.Context(), vk.Name)), meta, body)
}

// rewriteMultipartModel rebuilds the multipart form with the model field
// pointing at the upstream model id, preserving every other field and file.
func rewriteMultipartModel(r *http.Request, upstreamModel string) ([]byte, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	defer writer.Close()

	for key, values := range r.MultipartForm.Value {
		for _, value := range values {
			if key == "model" {
				value = upstreamModel
			}
			if err := writer.WriteField(key, value); err != nil {
				return nil, err
			}
		}
	}
	for field, fileHeaders := range r.MultipartForm.File {
		for _, fileHeader := range fileHeaders {
			part, err := writer.CreateFormFile(field, fileHeader.Filename)
			if err != nil {
				return nil, err
			}
			file, err := fileHeader.Open()
			if err != nil {
				return nil, err
			}
			_, copyErr := io.Copy(part, file)
			_ = file.Close()
			if copyErr != nil {
				return nil, copyErr
			}
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	r.Header.Set("Content-Type", writer.FormDataContentType())
	return buf.Bytes(), nil
}

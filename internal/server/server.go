package server

import (
	"encoding/json"
	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/policy"
	"github.com/1jehuang/2papi/internal/protocol"
	"github.com/1jehuang/2papi/internal/proxy"
	"github.com/1jehuang/2papi/internal/resilience"
	"github.com/1jehuang/2papi/internal/router"
	"io"
	"net/http"
	"sync/atomic"
)

type Runtime struct {
	Snap  *config.Snapshot
	Auth  *policy.Auth
	Proxy *proxy.Proxy
}

type Server struct {
	runtime atomic.Value
	state   *resilience.State
}

func New(s *config.Snapshot, p *proxy.Proxy) *Server {
	srv := &Server{state: p.State}
	srv.runtime.Store(&Runtime{Snap: s, Auth: policy.New(s), Proxy: p})
	return srv
}
func NewRuntimeServer(s *config.Snapshot, st *resilience.State) *Server {
	srv := &Server{state: st}
	srv.Adopt(s)
	return srv
}
func BuildRuntime(s *config.Snapshot, st *resilience.State) *Runtime {
	rt := router.New(s, st)
	px := proxy.New(s, st, rt)
	return &Runtime{Snap: s, Auth: policy.New(s), Proxy: px}
}
func (s *Server) Adopt(snap *config.Snapshot) { s.runtime.Store(BuildRuntime(snap, s.state)) }
func (s *Server) Runtime() *Runtime           { return s.runtime.Load().(*Runtime) }
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); w.Write([]byte("ok")) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); w.Write([]byte("ready")) })
	mux.HandleFunc("/v1/models", s.models)
	mux.HandleFunc("/v1/chat/completions", s.chat)
	return mux
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
	data := []map[string]any{}
	for _, m := range rt.Snap.Models {
		data = append(data, map[string]any{"id": m.Alias, "object": "model", "owned_by": "gateway"})
	}
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
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
	if !rt.Auth.AllowRate(vk) {
		proxy.Error(w, 429, "rate limit exceeded")
		return
	}
	rt.Proxy.Chat(w, r, meta, body)
}

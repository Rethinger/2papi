package server

import (
	"encoding/json"
	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/policy"
	"github.com/1jehuang/2papi/internal/protocol"
	"github.com/1jehuang/2papi/internal/proxy"
	"io"
	"net/http"
)

type Server struct {
	Snap  *config.Snapshot
	Auth  *policy.Auth
	Proxy *proxy.Proxy
}

func New(s *config.Snapshot, p *proxy.Proxy) *Server {
	return &Server{Snap: s, Auth: policy.New(s), Proxy: p}
}
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); w.Write([]byte("ok")) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); w.Write([]byte("ready")) })
	mux.HandleFunc("/v1/models", s.models)
	mux.HandleFunc("/v1/chat/completions", s.chat)
	return mux
}
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	data := []map[string]any{}
	for _, m := range s.Snap.Models {
		data = append(data, map[string]any{"id": m.Alias, "object": "model", "owned_by": "gateway"})
	}
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		proxy.Error(w, 405, "method not allowed")
		return
	}
	vk, ok := s.Auth.Authenticate(r)
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
	if _, ok := s.Snap.ModelsByAlias[meta.Model]; !ok {
		proxy.Error(w, 404, "unknown model")
		return
	}
	if !policy.Allows(vk, meta.Model) {
		proxy.Error(w, 403, "model not allowed")
		return
	}
	if !s.Auth.AllowRate(vk) {
		proxy.Error(w, 429, "rate limit exceeded")
		return
	}
	s.Proxy.Chat(w, r, meta, body)
}

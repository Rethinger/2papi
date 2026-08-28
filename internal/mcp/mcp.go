// Package mcp implements the Model Context Protocol gateway (шаг хребта
// после 6): streamable-HTTP JSON-RPC passthrough to upstream MCP servers,
// fronted by virtual-key auth so budgets/RPM/concurrency apply to tool
// calls exactly like model traffic. Tool-call events land in the same
// request_events pipeline (endpoint /v1/mcp/<server>, no token accounting).
//
// v1 scope: one configured HTTP endpoint per server; stdio transports are
// out of scope (the gateway is a network process).
package mcp

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/policy"
	"github.com/Rethinger/2papi/internal/proxy"
	"github.com/Rethinger/2papi/internal/telemetry"
)

const maxBodyBytes = 16 << 20

type Gateway struct {
	Snapshot      func() *config.Snapshot
	Auth          *policy.Auth
	Client        *http.Client
	Telemetry     telemetry.Recorder
	ConfigVersion int64
}

// Serve handles POST /v1/mcp/<server>. name is the path segment after the
// prefix. Streaming responses (SSE from the upstream) are forwarded verbatim.
func (g *Gateway) Serve(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		proxy.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	snap := g.Snapshot()
	vk, ok := g.Auth.Authenticate(r)
	if !ok {
		proxy.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	server, exists := snap.MCPServersByName[name]
	if !exists || !server.IsEnabled() {
		proxy.Error(w, http.StatusNotFound, "unknown mcp server")
		return
	}
	begin := g.Auth.Begin(vk)
	if !begin.Allowed {
		proxy.Error(w, http.StatusTooManyRequests, begin.Reason)
		g.Auth.Finalize(vk, 0, 0, false)
		return
	}

	started := time.Now()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		proxy.Error(w, http.StatusBadRequest, "invalid body")
		g.Auth.Finalize(vk, 0, 0, false)
		return
	}
	status, forwardErr := g.forward(w, r, server, body)
	g.Auth.Finalize(vk, 0, 0, true)
	if g.Telemetry != nil {
		g.Telemetry.Record(telemetry.Event{
			RequestID:      r.Header.Get("X-Gateway-Request-ID"),
			OccurredAt:     started,
			Endpoint:       "/v1/mcp/" + name,
			PublicModel:    "mcp",
			UpstreamModel:  server.Name,
			VirtualKey:     vk.Name,
			FinalStatus:    status,
			Success:        status >= 200 && status < 400 && forwardErr == nil,
			TotalLatencyMS: time.Since(started).Milliseconds(),
			ConfigVersion:  g.ConfigVersion,
		})
	}
}

func (g *Gateway) forward(w http.ResponseWriter, r *http.Request, server config.McpServer, body []byte) (int, error) {
	ctx := r.Context()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(string(body)))
	if err != nil {
		proxy.Error(w, http.StatusBadGateway, "invalid mcp server url")
		return http.StatusBadGateway, err
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("MCP-Protocol-Version", r.Header.Get("MCP-Protocol-Version"))
	for k, v := range server.Headers {
		req.Header.Set(k, v)
	}
	resp, err := g.client().Do(req)
	if err != nil {
		proxy.Error(w, http.StatusBadGateway, "mcp upstream unreachable")
		return http.StatusBadGateway, err
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return resp.StatusCode, nil
}

func (g *Gateway) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return http.DefaultClient
}

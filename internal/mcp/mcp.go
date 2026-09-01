// Package mcp implements the Model Context Protocol gateway (шаг хребта
// после 6): streamable-HTTP JSON-RPC passthrough to upstream MCP servers,
// fronted by virtual-key auth so budgets/RPM/concurrency apply to tool
// calls exactly like model traffic. Tool-call events land in the same
// request_events pipeline (endpoint /v1/mcp/<server>, no token accounting).
//
// v1 scope: one configured HTTP endpoint per server; stdio transports are
// out of scope (the gateway is a network process).
//
// Tool-surface pinning (G2): the first tools/list response per server is
// pinned (sha256 + tool names); any later change is audited and, when the
// server declares `pin_tools: true`, the changed listing is BLOCKED (409)
// as a rug-pull guard. Pins are per-process (in-memory).
package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
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
	// pins holds the pinned tools/list signature per server name.
	pins sync.Map
}

// toolPin is the pinned tools/list signature: hash of the raw response
// bytes plus the sorted tool names for a human-readable audit trail.
type toolPin struct {
	hash  string
	tools []string
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
	status, forwardErr, pinOutcome := g.forward(w, r, server, body)
	g.Auth.Finalize(vk, 0, 0, true)
	if g.Telemetry != nil {
		event := telemetry.Event{
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
		}
		if pinOutcome != "" {
			event.Attempts = []telemetry.Attempt{{
				Account: server.Name,
				Adapter: "mcp-pin",
				Status:  status,
				Outcome: pinOutcome,
			}}
		}
		g.Telemetry.Record(event)
	}
}

func (g *Gateway) forward(w http.ResponseWriter, r *http.Request, server config.McpServer, body []byte) (int, error, string) {
	ctx := r.Context()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(string(body)))
	if err != nil {
		proxy.Error(w, http.StatusBadGateway, "invalid mcp server url")
		return http.StatusBadGateway, err, ""
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
		return http.StatusBadGateway, err, ""
	}
	defer resp.Body.Close()

	// tools/list is buffered so the tool surface can be pinned and compared
	// (G2). Everything else streams verbatim as before.
	if isToolsList(body) {
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
		if readErr != nil {
			proxy.Error(w, http.StatusBadGateway, "mcp upstream unreachable")
			return http.StatusBadGateway, readErr, ""
		}
		if blocked, outcome := g.checkToolsPin(server, payload); blocked {
			proxy.Error(w, http.StatusConflict, "mcp tools list changed and tool pinning is enabled")
			return http.StatusConflict, nil, outcome
		} else if outcome != "" {
			// registered / changed: forward verbatim, audit via outcome
			copyMCPHeaders(resp.Header, w)
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(payload)
			return resp.StatusCode, nil, outcome
		}
		copyMCPHeaders(resp.Header, w)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(payload)
		return resp.StatusCode, nil, ""
	}

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return resp.StatusCode, nil, ""
}

// copyMCPHeaders forwards upstream response headers to the client, dropping
// framing-sensitive ones that would conflict with a replaced body.
func copyMCPHeaders(src http.Header, w http.ResponseWriter) {
	for k, vs := range src {
		switch strings.ToLower(k) {
		case "content-length", "connection", "transfer-encoding":
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
}

// isToolsList reports whether the request is a JSON-RPC tools/list call.
func isToolsList(body []byte) bool {
	var payload struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	return payload.Method == "tools/list"
}

// toolsListSignature derives the pin (hash + sorted tool names) from a
// tools/list response body.
func toolsListSignature(body []byte) (string, []string) {
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	var payload struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	var names []string
	if json.Unmarshal(body, &payload) == nil {
		for _, t := range payload.Result.Tools {
			if t.Name != "" {
				names = append(names, t.Name)
			}
		}
	}
	sort.Strings(names)
	return hash, names
}

// checkToolsPin pins the first tools/list per server and audits/block tool-
// surface changes. Returns (blocked, outcome); outcome is the audit label:
// mcp_tools_registered | mcp_tools_changed | mcp_tools_blocked.
func (g *Gateway) checkToolsPin(server config.McpServer, body []byte) (bool, string) {
	hash, names := toolsListSignature(body)
	raw, ok := g.pins.Load(server.Name)
	if !ok {
		g.pins.Store(server.Name, &toolPin{hash: hash, tools: names})
		return false, "mcp_tools_registered"
	}
	pin := raw.(*toolPin)
	if pin.hash == hash {
		return false, ""
	}
	// Accept the new surface (the audit trail stays) so a one-off upstream
	// rename does not wedge the server forever.
	g.pins.Store(server.Name, &toolPin{hash: hash, tools: names})
	if server.PinTools {
		return true, "mcp_tools_blocked"
	}
	return false, "mcp_tools_changed"
}

func (g *Gateway) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return http.DefaultClient
}

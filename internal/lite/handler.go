package lite

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Rethinger/2papi/internal/config"
)

// Handler serves lite control-plane API at /api/control/v1/*
// It mimics the Next.js control-plane but backed by file.
type Handler struct {
	store *Store
}

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Strip prefix /api/control/v1/
	path := strings.TrimPrefix(r.URL.Path, "/api/control/v1/")
	if path == r.URL.Path {
		path = strings.TrimPrefix(r.URL.Path, "/api/control/v1")
	}
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		path = "overview"
	}
	switch {
	case path == "overview" && r.Method == http.MethodGet:
		respond(w, h.store.Overview())
	case path == "providers" && r.Method == http.MethodGet:
		respond(w, h.Providers())
	case path == "accounts" && r.Method == http.MethodGet:
		respond(w, h.Accounts())
	case strings.HasPrefix(path, "accounts") && r.Method == http.MethodPost:
		// Create account
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// For lite, just echo
		respondStatus(w, body, http.StatusCreated)
	case path == "models" && r.Method == http.MethodGet:
		respond(w, h.Models())
	case path == "models" && r.Method == http.MethodPost:
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		respondStatus(w, body, http.StatusCreated)
	case path == "virtual-keys" && r.Method == http.MethodGet:
		respond(w, h.VirtualKeys())
	case path == "teams" && r.Method == http.MethodGet:
		respond(w, []interface{}{})
	case path == "config-versions" && r.Method == http.MethodGet:
		respond(w, []interface{}{})
	case path == "audit-events" && r.Method == http.MethodGet:
		respond(w, []interface{}{})
	case path == "request-events" && r.Method == http.MethodGet:
		respond(w, []interface{}{})
	case path == "request-trends" && r.Method == http.MethodGet:
		respond(w, []interface{}{})
	case path == "routing" && r.Method == http.MethodGet:
		snap := h.store.Snapshot()
		if snap == nil {
			respond(w, map[string]interface{}{"strategy": "balanced", "sticky_ttl": "1h", "max_attempts": 2, "resilience": map[string]interface{}{"cooldown": "30s", "circuit_failures": 3, "circuit_reset": "1m"}, "optimization": snapOptimization(snap)})
			return
		}
		respond(w, map[string]interface{}{
			"strategy": snap.Routing.Strategy, "sticky_ttl": snap.Routing.StickyTTL, "max_attempts": snap.Routing.MaxAttempts,
			"resilience": map[string]interface{}{"cooldown": snap.Cooldown.String(), "circuit_failures": snap.Resilience.CircuitFailures, "circuit_reset": snap.CircuitReset.String()},
			"optimization": snapOptimization(snap),
		})
	case path == "routing" && r.Method == http.MethodPost:
		var body struct {
			Strategy     string                 `json:"strategy"`
			StickyTTL    string                 `json:"sticky_ttl"`
			MaxAttempts  int                    `json:"max_attempts"`
			Resilience   map[string]interface{} `json:"resilience"`
			Optimization map[string]interface{} `json:"optimization"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = h.store.Update(func(cfg *config.Config) error {
			if body.Strategy != "" {
				cfg.Routing.Strategy = body.Strategy
			}
			if body.StickyTTL != "" {
				cfg.Routing.StickyTTL = body.StickyTTL
			}
			if body.MaxAttempts != 0 {
				cfg.Routing.MaxAttempts = body.MaxAttempts
			}
			if body.Optimization != nil {
				if v, ok := body.Optimization["rtk_compression"].(bool); ok {
					cfg.Optimization.RTKCompression = v
				}
				if v, ok := body.Optimization["caveman"].(bool); ok {
					cfg.Optimization.Caveman = v
				}
				if v, ok := body.Optimization["headroom"].(bool); ok {
					cfg.Optimization.Headroom = v
				}
			}
			return nil
		})
		respond(w, map[string]interface{}{"ok": true})
	case path == "proxy-pool" && r.Method == http.MethodGet:
		snap := h.store.Snapshot()
		raw := ""
		if snap != nil && len(snap.GlobalProxies) > 0 {
			// reconstruct raw from proxies
			for i, p := range snap.GlobalProxies {
				if i > 0 {
					raw += "\n"
				}
				raw += p.String()
			}
		}
		respond(w, map[string]interface{}{"raw": raw})
	case path == "proxy-pool" && r.Method == http.MethodPost:
		var body struct{ Raw string `json:"raw"` }
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = h.store.Update(func(cfg *config.Config) error {
			if body.Raw == "" {
				cfg.Proxies = nil
			} else {
				// split by lines
				lines := strings.Split(body.Raw, "\n")
				cfg.Proxies = lines
			}
			return nil
		})
		respond(w, map[string]interface{}{"raw": body.Raw, "proxy_count": 1})
	case path == "webhook" && r.Method == http.MethodGet:
		respond(w, map[string]interface{}{"enabled": false, "url": "", "secret": ""})
	case path == "settings" && r.Method == http.MethodGet:
		respond(w, []interface{}{})
	case path == "quota" && r.Method == http.MethodGet:
		// Lite mode: no live provider quota headers yet; return empty aggregate
		// so the dashboard Quota widget renders without errors.
		respond(w, map[string]interface{}{
			"summary":   map[string]interface{}{"percent": 0, "used": 0, "limit": 0, "active": 0},
			"providers": []interface{}{},
		})
	default:
		respond(w, map[string]interface{}{"lite": true, "path": path, "message": "lite mode: in-memory, file-backed"})
	}
}

func snapOptimization(snap *config.Snapshot) map[string]interface{} {
	if snap == nil {
		return map[string]interface{}{"rtk_compression": false, "caveman": false, "headroom": false}
	}
	return map[string]interface{}{
		"rtk_compression": snap.Optimization.RTKCompression,
		"caveman":         snap.Optimization.Caveman,
		"headroom":        snap.Optimization.Headroom,
		"headroom_reserve": snap.Optimization.HeadroomReserve,
		"headroom_keep":   snap.Optimization.HeadroomKeep,
	}
}

func (h *Handler) Providers() []map[string]interface{} {
	snap := h.store.Snapshot()
	if snap == nil {
		return []map[string]interface{}{}
	}
	// Derive providers from accounts' adapters
	seen := map[string]bool{}
	var out []map[string]interface{}
	for _, a := range snap.Accounts {
		adapter := a.Adapter
		if adapter == "" {
			adapter = "openai-compatible"
		}
		if seen[adapter] {
			continue
		}
		seen[adapter] = true
		out = append(out, map[string]interface{}{
			"id": adapter, "slug": adapter, "name": adapter, "adapter": adapter, "base_url": a.BaseURL, "enabled": true,
		})
	}
	return out
}

func (h *Handler) Accounts() []map[string]interface{} {
	snap := h.store.Snapshot()
	if snap == nil {
		return []map[string]interface{}{}
	}
	var out []map[string]interface{}
	for _, a := range snap.Accounts {
		out = append(out, map[string]interface{}{
			"id": a.ID, "name": a.Name, "display_name": a.Name, "base_url": a.BaseURL, "enabled": a.Enabled,
			"priority": a.Priority, "weight": a.Weight, "max_concurrency": a.MaxConcurrency, "cost": a.Cost,
			"provider_id": a.Adapter, "adapter": a.Adapter,
		})
	}
	return out
}

func (h *Handler) Models() []map[string]interface{} {
	snap := h.store.Snapshot()
	if snap == nil {
		return []map[string]interface{}{}
	}
	var out []map[string]interface{}
	for _, m := range snap.Models {
		out = append(out, map[string]interface{}{
			"alias": m.Alias, "upstream_model": m.UpstreamModel, "accounts": m.Accounts, "enabled": true,
			"routing_strategy": m.RoutingStrategy, "fallbacks": m.Fallbacks,
		})
	}
	return out
}

func (h *Handler) VirtualKeys() []map[string]interface{} {
	snap := h.store.Snapshot()
	if snap == nil {
		return []map[string]interface{}{}
	}
	var out []map[string]interface{}
	for _, k := range snap.VirtualKeys {
		prefix := "***"
		if len(k.Key) >= 2 {
			prefix = k.Key[:2] + "***"
		} else if k.KeyHash != "" && len(k.KeyHash) >= 2 {
			prefix = k.KeyHash[:2] + "***"
		}
		out = append(out, map[string]interface{}{
			"id": k.ID, "name": k.Name, "key_prefix": prefix, "enabled": true,
			"models": k.Models, "rpm": k.RPM, "tpm": k.TPM, "budget_usd": k.BudgetUSD,
		})
	}
	return out
}

func respond(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
}

func respondStatus(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
}

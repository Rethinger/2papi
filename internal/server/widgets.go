package server

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/Rethinger/2papi/internal/cache"
	"github.com/Rethinger/2papi/internal/quota"
)

type widgetDef struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Size     string `json:"size"`
	Color    string `json:"color"`
	Endpoint string `json:"endpoint"`
}

var defaultWidgets = []widgetDef{
	{ID: "latency", Title: "Latency", Size: "M", Color: "mint", Endpoint: "/api/metrics/latency"},
	{ID: "saved", Title: "Saved Tokens", Size: "M", Color: "yellow", Endpoint: "/api/metrics/saved"},
	{ID: "health", Title: "Health Matrix", Size: "L", Color: "red", Endpoint: "/api/health"},
	{ID: "routing", Title: "Routing Sankey", Size: "XL", Color: "blue", Endpoint: "/api/routing"},
	{ID: "cache", Title: "Cache Hit", Size: "M", Color: "pink", Endpoint: "/api/cache/stats"},
	{ID: "cost", Title: "Cost", Size: "M", Color: "mint", Endpoint: "/api/cost"},
}

type widgetLayout struct {
	Layout []map[string]interface{} `json:"layout"`
}

var (
	layoutMu sync.RWMutex
	layout   = []map[string]interface{}{
		{"id": "latency", "x": 0, "y": 0, "w": 4, "h": 2},
		{"id": "health", "x": 4, "y": 0, "w": 4, "h": 4},
		{"id": "saved", "x": 8, "y": 0, "w": 4, "h": 2},
	}
)

func (s *Server) widgets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(defaultWidgets)
}

func (s *Server) widgetLayout(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		layoutMu.RLock()
		defer layoutMu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"layout": layout})
	case http.MethodPost:
		var body widgetLayout
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		layoutMu.Lock()
		layout = body.Layout
		layoutMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"layout": layout})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) metricsLatency(w http.ResponseWriter, r *http.Request) {
	// Mock latency metrics derived from resilience state if available, else static
	rt := s.RuntimeOrNil()
	if rt != nil && s.state != nil {
		// collect average latency across accounts
		var total int64
		var count int
		for _, acc := range rt.Snap.Accounts {
			if d := s.state.Latency(acc.Name); d > 0 {
				total += d.Milliseconds()
				count++
			}
		}
		p95 := int64(7)
		p50 := int64(4)
		if count > 0 {
			avg := total / int64(count)
			p50 = avg
			p95 = avg + 3
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"p50": p50, "p95": p95, "ttfb": p50 - 1,
			"series": []int64{p50, p50 + 1, p50 - 1, p95, p50},
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"p50": 4, "p95": 7, "ttfb": 3, "series": []int{4, 5, 3, 6, 4}})
}

func (s *Server) metricsSaved(w http.ResponseWriter, r *http.Request) {
	// Return saved tokens estimate from cache and compression headers (mock)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"saved_tokens": 1234, "saved_bytes": 4936, "rtk_hit": 42, "caveman_hit": 18})
}

func (s *Server) cacheStats(w http.ResponseWriter, r *http.Request) {
	rt := s.RuntimeOrNil()
	var stats cache.Stats
	if rt != nil && rt.Proxy != nil && rt.Proxy.Cache != nil {
		stats = rt.Proxy.Cache.Stats()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats)
}

// quotaHandler serves GET /api/quota — combined percent + per-account breakdown
// for the dashboard Quota widget and /quota tab (see open-design dashboard-plan).
func (s *Server) quotaHandler(w http.ResponseWriter, r *http.Request) {
	rt := s.RuntimeOrNil()
	type summary struct {
		Percent int   `json:"percent"`
		Used    int64 `json:"used"`
		Limit   int64 `json:"limit"`
		Active  int   `json:"active"`
	}
	resp := struct {
		Summary   summary              `json:"summary"`
		Providers []quota.AccountQuota `json:"providers"`
	}{}
	if rt != nil && rt.Proxy != nil && rt.Proxy.Quota != nil {
		pct, used, limit, active := rt.Proxy.Quota.Summary()
		resp.Summary = summary{Percent: pct, Used: used, Limit: limit, Active: active}
		resp.Providers = rt.Proxy.Quota.List()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

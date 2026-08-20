package server

import (
	"encoding/json"
	"net/http"
)

// pluginsHandler serves GET /api/plugins — list of registered gateway plugins
// (in-process + HTTP sidecars) for the future Plugins dashboard tab.
func (s *Server) pluginsHandler(w http.ResponseWriter, r *http.Request) {
	rt := s.RuntimeOrNil()
	type item struct {
		Name     string `json:"name"`
		Endpoint string `json:"endpoint,omitempty"`
		InProc   bool   `json:"in_proc"`
	}
	out := []item{}
	if rt != nil && rt.Proxy != nil && rt.Proxy.Plugins != nil {
		for _, p := range rt.Proxy.Plugins.List() {
			out = append(out, item{Name: p.Name, Endpoint: p.Endpoint, InProc: p.Endpoint == ""})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"plugins": out, "count": len(out)})
}

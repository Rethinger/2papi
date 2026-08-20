package quota

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ParseHeaders extracts quota signal from arbitrary upstream response headers.
// Supports codex-style x-*-primary/secondary-used-percent and reset-at, plus a
// generic X-Provider-Quota-* convention for provider adapters.
func ParseHeaders(h http.Header) (Observation, map[string]int64, bool) {
	if h == nil {
		return Observation{}, nil, false
	}
	now := time.Now()
	var o Observation
	weights := map[string]int64{}

	// Generic convention: X-Provider-Quota-Used / -Limit / -ResetAt
	if used := h.Get("X-Provider-Quota-Used"); used != "" {
		if n, err := strconv.ParseInt(used, 10, 64); err == nil {
			o.Used = n
		}
	}
	if limit := h.Get("X-Provider-Quota-Limit"); limit != "" {
		if n, err := strconv.ParseInt(limit, 10, 64); err == nil {
			o.Limit = n
		}
	}
	if reset := h.Get("X-Provider-Quota-ResetAt"); reset != "" {
		if t, err := time.Parse(time.RFC3339, reset); err == nil {
			o.ResetAt = t
		}
	}

	// Codex-style per-window percentages (x-...primary-used-percent etc.)
	for name, values := range h {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, "x-") || (!strings.HasSuffix(lower, "-primary-used-percent") && !strings.HasSuffix(lower, "-secondary-used-percent")) {
			continue
		}
		for _, raw := range values {
			if n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil {
				if n > 100 {
					n = 100
				}
				window := "secondary"
				if strings.HasSuffix(lower, "-primary-used-percent") {
					window = "primary"
				}
				weights[window] = n
				if n > o.Used {
					o.Used = n
				}
			}
		}
	}
	// Reset time from x-...-primary-reset-at
	for name, values := range h {
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, "-primary-reset-at") && !strings.HasSuffix(lower, "-secondary-reset-at") {
			continue
		}
		for _, raw := range values {
			if secs, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil {
				t := time.Unix(secs, 0)
				if t.After(now) && (o.ResetAt.IsZero() || t.Before(o.ResetAt)) {
					o.ResetAt = t
				}
			}
		}
	}

	if o.Used > 0 || o.Limit > 0 || len(weights) > 0 || !o.ResetAt.IsZero() {
		o.Source = "header"
		return o, weights, true
	}
	return Observation{}, nil, false
}

// FamilyFromHeader infers provider family from response headers (best-effort).
func FamilyFromHeader(h http.Header) string {
	if v := h.Get("X-Provider-Quota-Family"); v != "" {
		return v
	}
	for k := range h {
		lk := strings.ToLower(k)
		switch {
		case strings.Contains(lk, "codex"), strings.Contains(lk, "openai"):
			return "codex"
		case strings.Contains(lk, "anthropic"), strings.Contains(lk, "claude"):
			return "claude"
		case strings.Contains(lk, "kimi"), strings.Contains(lk, "moonshot"):
			return "kimi"
		}
	}
	return ""
}

// MarshalSummary is a JSON helper for the dashboard endpoint.
func MarshalSummary(summary struct {
	Percent int   `json:"percent"`
	Used    int64 `json:"used"`
	Limit   int64 `json:"limit"`
	Active  int   `json:"active"`
}) []byte {
	b, _ := json.Marshal(summary)
	return b
}

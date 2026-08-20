package server

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/Rethinger/2papi/internal/telemetry"
)

type MetricsCollector struct {
	mu                sync.Mutex
	requests          map[string]*atomic.Uint64 // key: model,status,outcome
	tokensInput       map[string]*atomic.Uint64 // key: model
	tokensOutput      map[string]*atomic.Uint64 // key: model
	latencySum        map[string]*atomic.Uint64 // key: model
	latencyCount      map[string]*atomic.Uint64 // key: model
	upstreamTTFBSum   map[string]*atomic.Uint64 // key: model
	upstreamTTFBCount map[string]*atomic.Uint64 // key: model
	overheadSum       map[string]*atomic.Uint64 // key: model
	overheadCount     map[string]*atomic.Uint64 // key: model
}

func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		requests:          map[string]*atomic.Uint64{},
		tokensInput:       map[string]*atomic.Uint64{},
		tokensOutput:      map[string]*atomic.Uint64{},
		latencySum:        map[string]*atomic.Uint64{},
		latencyCount:      map[string]*atomic.Uint64{},
		upstreamTTFBSum:   map[string]*atomic.Uint64{},
		upstreamTTFBCount: map[string]*atomic.Uint64{},
		overheadSum:       map[string]*atomic.Uint64{},
		overheadCount:     map[string]*atomic.Uint64{},
	}
}

func (m *MetricsCollector) Observe(event telemetry.Event) {
	if m == nil {
		return
	}
	outcome := "success"
	if !event.Success {
		outcome = "error"
	}
	if len(event.Attempts) > 0 {
		outcome = event.Attempts[len(event.Attempts)-1].Outcome
	}

	model := event.PublicModel
	if model == "" {
		model = "unknown"
	}

	reqKey := fmt.Sprintf(`model="%s",status="%d",outcome="%s"`, model, event.FinalStatus, outcome)
	m.counter(m.requests, reqKey).Add(1)

	if event.InputTokens > 0 {
		m.counter(m.tokensInput, model).Add(uint64(event.InputTokens))
	}
	if event.OutputTokens > 0 {
		m.counter(m.tokensOutput, model).Add(uint64(event.OutputTokens))
	}
	if event.TotalLatencyMS > 0 {
		m.counter(m.latencySum, model).Add(uint64(event.TotalLatencyMS))
		m.counter(m.latencyCount, model).Add(1)
	}
	if event.UpstreamTTFBMS > 0 {
		m.counter(m.upstreamTTFBSum, model).Add(uint64(event.UpstreamTTFBMS))
		m.counter(m.upstreamTTFBCount, model).Add(1)
	}
	if event.OverheadMS > 0 {
		m.counter(m.overheadSum, model).Add(uint64(event.OverheadMS))
		m.counter(m.overheadCount, model).Add(1)
	}
}

func (m *MetricsCollector) counter(store map[string]*atomic.Uint64, key string) *atomic.Uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctr, ok := store[key]
	if !ok {
		ctr = &atomic.Uint64{}
		store[key] = ctr
	}
	return ctr
}

func (s *Server) metricsHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	rt := s.RuntimeOrNil()
	m := s.metrics

	_, _ = io.WriteString(w, "# HELP gateway_requests_total Total requests processed by the gateway\n# TYPE gateway_requests_total counter\n")
	if m != nil {
		m.mu.Lock()
		keys := make([]string, 0, len(m.requests))
		for k := range m.requests {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			_, _ = fmt.Fprintf(w, "gateway_requests_total{%s} %d\n", k, m.requests[k].Load())
		}
		m.mu.Unlock()
	}

	_, _ = io.WriteString(w, "\n# HELP gateway_tokens_total Total tokens processed by model and direction\n# TYPE gateway_tokens_total counter\n")
	if m != nil {
		m.mu.Lock()
		models := make([]string, 0, len(m.tokensInput))
		for k := range m.tokensInput {
			models = append(models, k)
		}
		sort.Strings(models)
		for _, model := range models {
			_, _ = fmt.Fprintf(w, "gateway_tokens_total{model=\"%s\",direction=\"input\"} %d\n", model, m.tokensInput[model].Load())
		}
		outModels := make([]string, 0, len(m.tokensOutput))
		for k := range m.tokensOutput {
			outModels = append(outModels, k)
		}
		sort.Strings(outModels)
		for _, model := range outModels {
			_, _ = fmt.Fprintf(w, "gateway_tokens_total{model=\"%s\",direction=\"output\"} %d\n", model, m.tokensOutput[model].Load())
		}
		m.mu.Unlock()
	}

	_, _ = io.WriteString(w, "\n# HELP gateway_request_duration_ms Average and total latency in milliseconds\n# TYPE gateway_request_duration_ms summary\n")
	if m != nil {
		m.mu.Lock()
		latModels := make([]string, 0, len(m.latencyCount))
		for k := range m.latencyCount {
			latModels = append(latModels, k)
		}
		sort.Strings(latModels)
		for _, model := range latModels {
			cnt := m.latencyCount[model].Load()
			sum := m.latencySum[model].Load()
			_, _ = fmt.Fprintf(w, "gateway_request_duration_ms_sum{model=\"%s\"} %d\n", model, sum)
			_, _ = fmt.Fprintf(w, "gateway_request_duration_ms_count{model=\"%s\"} %d\n", model, cnt)
		}
		m.mu.Unlock()
	}

	if m != nil {
		_, _ = io.WriteString(w, "\n# HELP gateway_upstream_ms Upstream time-to-first-byte latency in milliseconds per model\n# TYPE gateway_upstream_ms summary\n")
		m.mu.Lock()
		models := make([]string, 0, len(m.upstreamTTFBCount))
		for k := range m.upstreamTTFBCount {
			models = append(models, k)
		}
		sort.Strings(models)
		for _, model := range models {
			cnt := m.upstreamTTFBCount[model].Load()
			sum := m.upstreamTTFBSum[model].Load()
			_, _ = fmt.Fprintf(w, "gateway_upstream_ms_sum{model=\"%s\"} %d\n", model, sum)
			_, _ = fmt.Fprintf(w, "gateway_upstream_ms_count{model=\"%s\"} %d\n", model, cnt)
			if cnt > 0 {
				_, _ = fmt.Fprintf(w, "gateway_upstream_ms_avg{model=\"%s\"} %d\n", model, sum/cnt)
			}
		}
		m.mu.Unlock()
	}

	if m != nil {
		_, _ = io.WriteString(w, "\n# HELP gateway_overhead_ms Gateway-internal overhead in milliseconds per model\n# TYPE gateway_overhead_ms summary\n")
		m.mu.Lock()
		models := make([]string, 0, len(m.overheadCount))
		for k := range m.overheadCount {
			models = append(models, k)
		}
		sort.Strings(models)
		for _, model := range models {
			cnt := m.overheadCount[model].Load()
			sum := m.overheadSum[model].Load()
			_, _ = fmt.Fprintf(w, "gateway_overhead_ms_sum{model=\"%s\"} %d\n", model, sum)
			_, _ = fmt.Fprintf(w, "gateway_overhead_ms_count{model=\"%s\"} %d\n", model, cnt)
			if cnt > 0 {
				_, _ = fmt.Fprintf(w, "gateway_overhead_ms_avg{model=\"%s\"} %d\n", model, sum/cnt)
			}
		}
		m.mu.Unlock()
	}

	if rt != nil && s.state != nil {
		_, _ = io.WriteString(w, "\n# HELP gateway_account_active_connections Current in-flight requests per account\n# TYPE gateway_account_active_connections gauge\n")
		for _, acct := range rt.Snap.Accounts {
			_, _ = fmt.Fprintf(w, "gateway_account_active_connections{account=\"%s\"} %d\n", acct.Name, s.state.Active(acct.Name))
		}

		_, _ = io.WriteString(w, "\n# HELP gateway_account_cooldown_active 1 if account is in cooldown, 0 otherwise\n# TYPE gateway_account_cooldown_active gauge\n")
		for _, acct := range rt.Snap.Accounts {
			cooling := 0
			if s.state.Cooling(acct.Name) {
				cooling = 1
			}
			_, _ = fmt.Fprintf(w, "gateway_account_cooldown_active{account=\"%s\"} %d\n", acct.Name, cooling)
		}

		_, _ = io.WriteString(w, "\n# HELP gateway_account_locked_out 1 if account is locked out, 0 otherwise\n# TYPE gateway_account_locked_out gauge\n")
		for _, acct := range rt.Snap.Accounts {
			locked := 0
			if s.state.LockedOut(acct.Name) {
				locked = 1
			}
			_, _ = fmt.Fprintf(w, "gateway_account_locked_out{account=\"%s\"} %d\n", acct.Name, locked)
		}
	}
}

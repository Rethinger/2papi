package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Plugin is a gateway extension. It can be in-process (Go func) or out-of-process
// HTTP sidecar (endpoint). This mirrors dsh's seam idea but pragmatic for Go gateway:
//   Service Definition = Hook func signature
//   Service Provider = Plugin struct (in-process or HTTP sidecar)
//   Consumer = proxy/router (calls Registry.Hooks)
type Plugin struct {
	Name string
	// BeforeRequest can mutate the outgoing upstream request (e.g., add headers, compress).
	BeforeRequest func(ctx context.Context, req *http.Request) error
	// AfterResponse can post-process the upstream response headers (e.g., add quota headers).
	AfterResponse func(ctx context.Context, h http.Header) error
	// Compress is called for tool_result compression (like RTK) — return new body, true if modified.
	Compress func(body []byte) ([]byte, bool)
	// Endpoint is an HTTP sidecar URL for out-of-process plugins (like caddy).
	// If set, BeforeRequest/AfterResponse are not used; instead the gateway POSTs
	// JSON to endpoint + "/before" or "/after" with 10ms timeout.
	Endpoint string
	Timeout  time.Duration
}

// Registry holds all registered plugins. Like dsh's Cordis context, but simple.
type Registry struct {
	mu      sync.RWMutex
	plugins map[string]*Plugin
}

func NewRegistry() *Registry {
	return &Registry{plugins: map[string]*Plugin{}}
}

func (r *Registry) Register(p *Plugin) error {
	if p == nil || p.Name == "" {
		return fmt.Errorf("plugin name required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[p.Name]; exists {
		return fmt.Errorf("plugin %s already registered", p.Name)
	}
	r.plugins[p.Name] = p
	return nil
}

func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	delete(r.plugins, name)
	r.mu.Unlock()
}

func (r *Registry) List() []*Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Plugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		out = append(out, p)
	}
	return out
}

// BeforeRequest runs all plugins' BeforeRequest hooks. If any returns error, the request is aborted.
func (r *Registry) BeforeRequest(ctx context.Context, req *http.Request) error {
	r.mu.RLock()
	plugins := make([]*Plugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		plugins = append(plugins, p)
	}
	r.mu.RUnlock()
	for _, p := range plugins {
		if p.Endpoint != "" {
			if err := r.callSidecar(ctx, p, "before", req); err != nil {
				return err
			}
			continue
		}
		if p.BeforeRequest != nil {
			if err := p.BeforeRequest(ctx, req); err != nil {
				return err
			}
		}
	}
	return nil
}

// AfterResponse runs all plugins' AfterResponse hooks.
func (r *Registry) AfterResponse(ctx context.Context, h http.Header) error {
	r.mu.RLock()
	plugins := make([]*Plugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		plugins = append(plugins, p)
	}
	r.mu.RUnlock()
	for _, p := range plugins {
		if p.Endpoint != "" {
			if err := r.callSidecar(ctx, p, "after", h); err != nil {
				return err
			}
			continue
		}
		if p.AfterResponse != nil {
			if err := p.AfterResponse(ctx, h); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Registry) callSidecar(ctx context.Context, p *Plugin, hook string, payload interface{}) error {
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 10 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint+"/"+hook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Plugin-Name", p.Name)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Sidecar failure is non-fatal for gateway TTF — log and continue.
		return nil
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("plugin %s sidecar %s returned %d", p.Name, hook, resp.StatusCode)
	}
	return nil
}

// Config mirrors plugins: section in snapshot.
type Config struct {
	Name     string `yaml:"name" json:"name"`
	Endpoint string `yaml:"endpoint" json:"endpoint"`
	Timeout  string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

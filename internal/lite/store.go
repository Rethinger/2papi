package lite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/Rethinger/2papi/internal/config"
)

// Store is a file-backed lite control-plane for single-binary mode.
// It holds the declarative config and persists to JSON file.
// When DATABASE_URL is not set, gateway uses this instead of Postgres.
type Store struct {
	mu       sync.RWMutex
	path     string
	snapshot *config.Snapshot
	cfg      config.Config
	onUpdate func(*config.Snapshot)
}

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".2papi", "lite.json")
}

func New(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath()
	}
	s := &Store{path: path}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// Try load existing
	if data, err := os.ReadFile(path); err == nil {
		var cfg config.Config
		if err := json.Unmarshal(data, &cfg); err == nil {
			if snap, err := config.Build(cfg); err == nil {
				s.cfg = cfg
				s.snapshot = snap
				return s, nil
			}
		}
	}
	// Fallback: try config/example.yaml or minimal default
	if snap, err := config.Load("config/example.yaml"); err == nil {
		s.cfg = snap.Config
		s.snapshot = snap
		_ = s.persist()
		return s, nil
	}
	// Minimal default
	cfg := config.Config{
		Version: 1,
		Secret:  "lite-secret-change-me",
		VirtualKeys: []config.VirtualKey{{Name: "dev", Key: "sk-gateway-dev", Models: []string{"gpt-dev"}, RPM: 60}},
		Models: []config.Model{{Alias: "gpt-dev", UpstreamModel: "gpt-4o-mini", Accounts: []string{"primary"}}},
		Accounts: []config.Account{{Name: "primary", BaseURL: "http://localhost:9001", APIKey: "sk-test", Enabled: true}},
	}
	snap, _ := config.Build(cfg)
	s.cfg = cfg
	s.snapshot = snap
	_ = s.persist()
	return s, nil
}

func (s *Store) Snapshot() *config.Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func (s *Store) Config() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Store) OnUpdate(fn func(*config.Snapshot)) { s.onUpdate = fn }

func (s *Store) Update(fn func(*config.Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfgCopy := s.cfg
	if err := fn(&cfgCopy); err != nil {
		return err
	}
	snap, err := config.Build(cfgCopy)
	if err != nil {
		return err
	}
	s.cfg = cfgCopy
	s.snapshot = snap
	if err := s.persist(); err != nil {
		return err
	}
	if s.onUpdate != nil {
		// call without holding lock to avoid deadlock (snapshot is already built)
		go s.onUpdate(snap)
	}
	return nil
}

func (s *Store) persist() error {
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// For dashboard API: return overview-like data derived from snapshot
func (s *Store) Overview() map[string]interface{} {
	snap := s.Snapshot()
	if snap == nil {
		return map[string]interface{}{"providers": 0, "accounts": 0, "models": 0, "virtual_keys": 0}
	}
	// providers = distinct adapters
	adapters := map[string]bool{}
	for _, a := range snap.Accounts {
		adapters[a.Adapter] = true
	}
	return map[string]interface{}{
		"providers":      len(adapters),
		"accounts":       len(snap.Accounts),
		"models":         len(snap.Models),
		"virtual_keys":   len(snap.VirtualKeys),
		"requests_24h":   0,
		"success_rate_24h": 1.0,
		"p95_latency_ms_24h": 5,
		"tokens_24h":     0,
	}
}

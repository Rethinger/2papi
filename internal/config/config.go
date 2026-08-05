package config

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"time"
)

type Config struct {
	Version     int          `yaml:"version" json:"version"`
	Server      Server       `yaml:"server" json:"server"`
	Secret      string       `yaml:"secret" json:"secret"`
	VirtualKeys []VirtualKey `yaml:"virtual_keys" json:"virtual_keys"`
	Models      []Model      `yaml:"models" json:"models"`
	Accounts    []Account    `yaml:"accounts" json:"accounts"`
	Routing     Routing      `yaml:"routing" json:"routing"`
	Resilience  Resilience   `yaml:"resilience" json:"resilience"`
}
type Server struct {
	Addr         string `yaml:"addr" json:"addr"`
	ReadTimeout  string `yaml:"read_timeout" json:"read_timeout"`
	WriteTimeout string `yaml:"write_timeout" json:"write_timeout"`
}
type VirtualKey struct {
	Name    string   `yaml:"name" json:"name"`
	Key     string   `yaml:"key,omitempty" json:"key,omitempty"`
	KeyHash string   `yaml:"key_hash,omitempty" json:"key_hash,omitempty"`
	Models  []string `yaml:"models" json:"models"`
	RPM     int      `yaml:"rpm" json:"rpm"`
	hash    []byte
}
type Model struct {
	Alias         string   `yaml:"alias" json:"alias"`
	UpstreamModel string   `yaml:"upstream_model" json:"upstream_model"`
	Accounts      []string `yaml:"accounts" json:"accounts"`
}
type Account struct {
	ID             string     `yaml:"id,omitempty" json:"id,omitempty"`
	Name           string     `yaml:"name" json:"name"`
	Adapter        string     `yaml:"adapter,omitempty" json:"adapter,omitempty"`
	BaseURL        string     `yaml:"base_url" json:"base_url"`
	APIKey         string     `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	Credential     Credential `yaml:"credential,omitempty" json:"credential,omitempty"`
	Enabled        bool       `yaml:"enabled" json:"enabled"`
	Priority       int        `yaml:"priority" json:"priority"`
	Weight         int        `yaml:"weight" json:"weight"`
	MaxConcurrency int        `yaml:"max_concurrency" json:"max_concurrency"`
	Cost           float64    `yaml:"cost" json:"cost"`
}

type Credential struct {
	Kind             string `yaml:"kind,omitempty" json:"kind,omitempty"`
	APIKey           string `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	AccessToken      string `yaml:"access_token,omitempty" json:"access_token,omitempty"`
	RefreshToken     string `yaml:"refresh_token,omitempty" json:"refresh_token,omitempty"`
	IDToken          string `yaml:"id_token,omitempty" json:"id_token,omitempty"`
	ExpiresAt        string `yaml:"expires_at,omitempty" json:"expires_at,omitempty"`
	ClientID         string `yaml:"client_id,omitempty" json:"client_id,omitempty"`
	ChatGPTAccountID string `yaml:"chatgpt_account_id,omitempty" json:"chatgpt_account_id,omitempty"`
	Revision         int64  `yaml:"revision,omitempty" json:"revision,omitempty"`
}
type Routing struct {
	Strategy    string `yaml:"strategy" json:"strategy"`
	StickyTTL   string `yaml:"sticky_ttl" json:"sticky_ttl"`
	MaxAttempts int    `yaml:"max_attempts" json:"max_attempts"`
}
type Resilience struct {
	Cooldown        string `yaml:"cooldown" json:"cooldown"`
	CircuitFailures int    `yaml:"circuit_failures" json:"circuit_failures"`
	CircuitReset    string `yaml:"circuit_reset" json:"circuit_reset"`
}

type Snapshot struct {
	Config
	KeyHashes                         map[string][]byte
	ModelsByAlias                     map[string]Model
	AccountsByName                    map[string]Account
	StickyTTL, Cooldown, CircuitReset time.Duration
	ReadTimeout, WriteTimeout         time.Duration
}

func Load(path string) (*Snapshot, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var c Config
	if e = yaml.Unmarshal(b, &c); e != nil {
		return nil, e
	}
	return Build(c)
}
func Build(c Config) (*Snapshot, error) {
	if c.Version != 1 && c.Version != 2 {
		return nil, fmt.Errorf("unsupported config version %d", c.Version)
	}
	if c.Secret == "" {
		return nil, errors.New("secret required")
	}
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Routing.Strategy == "" {
		c.Routing.Strategy = "balanced"
	}
	if c.Routing.MaxAttempts <= 0 {
		c.Routing.MaxAttempts = 2
	}
	if c.Resilience.CircuitFailures <= 0 {
		c.Resilience.CircuitFailures = 3
	}
	s := &Snapshot{Config: c, KeyHashes: map[string][]byte{}, ModelsByAlias: map[string]Model{}, AccountsByName: map[string]Account{}}
	var e error
	s.StickyTTL, e = parseDur(c.Routing.StickyTTL, time.Hour)
	if e != nil {
		return nil, e
	}
	s.Cooldown, e = parseDur(c.Resilience.Cooldown, 30*time.Second)
	if e != nil {
		return nil, e
	}
	s.CircuitReset, e = parseDur(c.Resilience.CircuitReset, time.Minute)
	if e != nil {
		return nil, e
	}
	s.ReadTimeout, e = parseDur(c.Server.ReadTimeout, 10*time.Second)
	if e != nil {
		return nil, e
	}
	s.WriteTimeout, e = parseDur(c.Server.WriteTimeout, 0)
	if e != nil {
		return nil, e
	}
	for i, a := range c.Accounts {
		if a.Name == "" || a.BaseURL == "" {
			return nil, errors.New("account name/base_url required")
		}
		if c.Version == 1 {
			if a.APIKey == "" {
				return nil, errors.New("account api_key required")
			}
			a.Adapter = "openai-compatible"
			a.Credential = Credential{Kind: "api_key", APIKey: a.APIKey, Revision: 1}
			c.Accounts[i] = a
		} else {
			if a.ID == "" || a.Adapter == "" || a.Credential.Kind == "" || a.Credential.Revision <= 0 {
				return nil, errors.New("version 2 account id/adapter/credential kind/positive revision required")
			}
			switch a.Adapter {
			case "openai-compatible":
				if a.Credential.APIKey == "" {
					return nil, errors.New("openai-compatible credential api_key required")
				}
			case "openai-codex":
				if a.Credential.AccessToken == "" || a.Credential.ChatGPTAccountID == "" {
					return nil, errors.New("openai-codex access_token and chatgpt_account_id required")
				}
			default:
				return nil, fmt.Errorf("unsupported account adapter %s", a.Adapter)
			}
		}
		if a.Weight <= 0 {
			a.Weight = 1
		}
		if a.MaxConcurrency <= 0 {
			a.MaxConcurrency = 100
		}
		if _, exists := s.AccountsByName[a.Name]; exists {
			return nil, fmt.Errorf("duplicate account %s", a.Name)
		}
		c.Accounts[i] = a
		s.Config.Accounts[i] = a
		s.AccountsByName[a.Name] = a
	}
	for _, m := range c.Models {
		if m.Alias == "" || m.UpstreamModel == "" || len(m.Accounts) == 0 {
			return nil, errors.New("model alias/upstream_model/accounts required")
		}
		if _, exists := s.ModelsByAlias[m.Alias]; exists {
			return nil, fmt.Errorf("duplicate model %s", m.Alias)
		}
		eligible := false
		for _, an := range m.Accounts {
			a, ok := s.AccountsByName[an]
			if !ok {
				return nil, fmt.Errorf("model %s references unknown account %s", m.Alias, an)
			}
			if a.Enabled {
				eligible = true
			}
		}
		if !eligible {
			return nil, fmt.Errorf("model %s has no enabled account", m.Alias)
		}
		s.ModelsByAlias[m.Alias] = m
	}
	for _, k := range c.VirtualKeys {
		if k.Name == "" || (k.Key == "" && k.KeyHash == "") {
			return nil, errors.New("virtual key name and key or key_hash required")
		}
		if k.KeyHash != "" {
			hash, err := hex.DecodeString(k.KeyHash)
			if err != nil || len(hash) != sha256.Size {
				return nil, fmt.Errorf("virtual key %s has invalid key_hash", k.Name)
			}
			s.KeyHashes[k.Name] = hash
		} else {
			mac := hmac.New(sha256.New, []byte(c.Secret))
			mac.Write([]byte(k.Key))
			s.KeyHashes[k.Name] = mac.Sum(nil)
		}
	}
	if len(s.VirtualKeys) == 0 {
		return nil, errors.New("at least one virtual key required")
	}
	return s, nil
}
func parseDur(v string, d time.Duration) (time.Duration, error) {
	if v == "" {
		return d, nil
	}
	return time.ParseDuration(v)
}
func (s *Snapshot) HashPresented(key string) []byte {
	mac := hmac.New(sha256.New, []byte(s.Secret))
	mac.Write([]byte(key))
	return mac.Sum(nil)
}
func (s *Snapshot) KeyHashHex(name string) string { return hex.EncodeToString(s.KeyHashes[name]) }

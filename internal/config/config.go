package config

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"strings"
	"time"

	"github.com/Rethinger/2papi/internal/proxylib"
)

type Config struct {
	Version      int          `yaml:"version" json:"version"`
	Server       Server       `yaml:"server" json:"server"`
	Secret       string       `yaml:"secret" json:"secret"`
	VirtualKeys  []VirtualKey `yaml:"virtual_keys" json:"virtual_keys"`
	Models       []Model      `yaml:"models" json:"models"`
	Accounts     []Account    `yaml:"accounts" json:"accounts"`
	Routing      Routing      `yaml:"routing" json:"routing"`
	Resilience   Resilience   `yaml:"resilience" json:"resilience"`
	Optimization Optimization `yaml:"optimization,omitempty" json:"optimization,omitempty"`
	Webhook      Webhook      `yaml:"webhook,omitempty" json:"webhook,omitempty"`
	// Proxies is the global upstream proxy pool (any format: one entry per
	// line, commas/semicolons, JSON array; http/https/socks4/4a/5/5h).
	// Accounts without their own proxy use this pool; accounts with one use
	// their own. Empty = direct connections (HTTP_PROXY env still applies).
	Proxies []string `yaml:"proxies,omitempty" json:"proxies,omitempty"`
	// Plugins lists sidecar/config plugins (like dsh plugins but HTTP sidecars).
	// Each becomes a plugin.Registry entry wired on runtime start.
	Plugins []PluginConfig `yaml:"plugins,omitempty" json:"plugins,omitempty"`
	// MCPServers exposes upstream Model Context Protocol endpoints through
	// the gateway (/v1/mcp/<name>) behind virtual-key auth.
	MCPServers []McpServer `yaml:"mcp_servers,omitempty" json:"mcp_servers,omitempty"`
	// Guardrails enables request-content checking (G5): PII regexes and
	// prompt-injection heuristics in off|log|redact|block mode.
	Guardrails GuardrailsConfig `yaml:"guardrails,omitempty" json:"guardrails,omitempty"`
}

// GuardrailsConfig mirrors internal/guardrails.Config.
type GuardrailsConfig struct {
	// Mode: "" | "off" | "log" | "redact" | "block".
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
	// PII toggles; empty = all PII detectors on when Mode is set.
	PII GuardrailsPII `yaml:"pii,omitempty" json:"pii,omitempty"`
	// Injection enables prompt-injection heuristics (zero = inherit/off).
	Injection *bool `yaml:"injection,omitempty" json:"injection,omitempty"`
}

type GuardrailsPII struct {
	Email  *bool `yaml:"email,omitempty" json:"email,omitempty"`
	Phone  *bool `yaml:"phone,omitempty" json:"phone,omitempty"`
	Card   *bool `yaml:"card,omitempty" json:"card,omitempty"`
	APIKey *bool `yaml:"api_key,omitempty" json:"api_key,omitempty"`
}

// PluginConfig declares a gateway plugin: HTTP sidecar endpoint or in-process
// enabled flag.
type PluginConfig struct {
	Name     string `yaml:"name" json:"name"`
	Endpoint string `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Enabled  bool   `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// Webhook delivers gateway alerts (account lockout, budget exceeded) to an
// external endpoint. Requests are signed with HMAC-SHA256 of the body using
// Secret when set.
type Webhook struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	URL     string `yaml:"url,omitempty" json:"url,omitempty"`
	Secret  string `yaml:"secret,omitempty" json:"secret,omitempty"`
}

// Optimization carries global request-optimization flags applied by the
// gateway to every request, mirroring 9Router's token savers. Individual
// requests can still opt in with X-Gateway-* headers even when disabled.
type Optimization struct {
	// RTKCompression compresses large tool_result content (saves 20-40% input tokens).
	RTKCompression bool `yaml:"rtk_compression" json:"rtk_compression"`
	// Caveman injects a terse "caveman" system directive so the model replies
	// with fewer output tokens (up to ~65%), while keeping technical substance.
	Caveman bool `yaml:"caveman" json:"caveman"`
	// Headroom reserves output space by pruning old tool history when the
	// estimated input tokens exceed the headroom window (like 9Router).
	Headroom bool `yaml:"headroom" json:"headroom"`
	// HeadroomReserve is the max estimated input tokens before pruning. 0 = 120k default.
	HeadroomReserve int `yaml:"headroom_reserve,omitempty" json:"headroom_reserve,omitempty"`
	// HeadroomKeep is how many recent turns to keep when pruning. 0 = 8 default.
	HeadroomKeep int `yaml:"headroom_keep,omitempty" json:"headroom_keep,omitempty"`
	// Mode presets. Empty string = legacy behavior (standard/full/balanced).
	// Valid values: rtk_mode light|standard|aggressive|auto,
	// caveman_mode lite|full|auto, headroom_profile conservative|balanced|aggressive|auto.
	RTKMode         string `yaml:"rtk_mode,omitempty" json:"rtk_mode,omitempty"`
	CavemanMode     string `yaml:"caveman_mode,omitempty" json:"caveman_mode,omitempty"`
	HeadroomProfile string `yaml:"headroom_profile,omitempty" json:"headroom_profile,omitempty"`
	// Squoze enables the experimental squoze engine (github.com/Rethinger/squoze)
	// as the ONLY request-body optimizer: content-routed head/tail squeezing
	// with never-elide, decision memo and reversible marker refs. Exclusive:
	// when set, rtk/caveman/headroom must be empty or Build() fails.
	Squoze bool `yaml:"squoze,omitempty" json:"squoze,omitempty"`
}
type Server struct {
	Addr         string `yaml:"addr" json:"addr"`
	ReadTimeout  string `yaml:"read_timeout" json:"read_timeout"`
	WriteTimeout string `yaml:"write_timeout" json:"write_timeout"`
	// Gzip compresses buffered non-streaming JSON responses >= 1 KiB when
	// the client advertises Accept-Encoding: gzip (LiteLLM parity).
	Gzip bool `yaml:"gzip,omitempty" json:"gzip,omitempty"`
}
type VirtualKey struct {
	Name           string   `yaml:"name" json:"name"`
	ID             string   `yaml:"id,omitempty" json:"id,omitempty"`
	Key            string   `yaml:"key,omitempty" json:"key,omitempty"`
	KeyHash        string   `yaml:"key_hash,omitempty" json:"key_hash,omitempty"`
	Models         []string `yaml:"models" json:"models"`
	RPM            int      `yaml:"rpm" json:"rpm"`
	TPM            int      `yaml:"tpm,omitempty" json:"tpm,omitempty"`
	MaxConcurrency int      `yaml:"max_concurrency,omitempty" json:"max_concurrency,omitempty"`
	BudgetUSD      float64  `yaml:"budget_usd,omitempty" json:"budget_usd,omitempty"` // daily by default, 0 = unlimited
	// BudgetDuration is the reset window for BudgetUSD: "" | "day" (default)
	// | "month". A monthly window keeps the same allowance but resets at the
	// first of the month (G1 gap — Cloud self-serve blocker).
	BudgetDuration string        `yaml:"budget_duration,omitempty" json:"budget_duration,omitempty"`
	Team           *Team         `yaml:"team,omitempty" json:"team,omitempty"`
	Optimization   *Optimization `yaml:"optimization,omitempty" json:"optimization,omitempty"`
	hash           []byte
}

type Team struct {
	ID        string  `yaml:"id" json:"id"`
	BudgetUSD float64 `yaml:"budget_usd" json:"budget_usd"`                   // shared daily team budget, 0 = unlimited
	ShareUSD  float64 `yaml:"share_usd,omitempty" json:"share_usd,omitempty"` // per-key fair share = budget / key count
	// BalanceUSD is the prepaid credit remaining (шаг 6, Cloud). It caps the
	// effective team budget (owner formula: min(budget, balance)). Precision
	// is bounded by snapshot freshness — control-plane decrements balance per
	// request_event and a nightly reconcile keeps it honest.
	BalanceUSD float64 `yaml:"balance_usd,omitempty" json:"balance_usd,omitempty"`
	// Org is the enterprise organization owning this team (migration 015).
	// Its budget is an upper bound on every team budget under it.
	Org *Org `yaml:"org,omitempty" json:"org,omitempty"`
}

// Org carries only what policy enforcement needs from organizations.
type Org struct {
	ID        string  `yaml:"id" json:"id"`
	BudgetUSD float64 `yaml:"budget_usd" json:"budget_usd"` // caps team budgets, 0 = unlimited
}

// McpServer is an upstream Model Context Protocol endpoint exposed through
// the gateway at /v1/mcp/<name> behind virtual-key auth. Headers carry the
// upstream credentials (same trust level as account api_key in file config).
type McpServer struct {
	Name    string            `yaml:"name" json:"name"`
	URL     string            `yaml:"url" json:"url"`
	Enabled *bool             `yaml:"enabled,omitempty" json:"enabled,omitempty"` // omitted = enabled
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	// PinTools (G2) enables rug-pull detection for the tool surface: the
	// first tools/list response is pinned (hash + tool names) and any later
	// change is BLOCKED with 409 (otherwise it is only audited). The gateway
	// always records a telemetry outcome (mcp_tools_registered/changed/blocked).
	PinTools bool `yaml:"pin_tools,omitempty" json:"pin_tools,omitempty"`
}

// IsEnabled reports whether the server accepts traffic; file configs may
// omit the flag, in which case the server is enabled.
func (m McpServer) IsEnabled() bool {
	if m.Enabled == nil {
		return true
	}
	return *m.Enabled
}

type Model struct {
	Alias             string        `yaml:"alias" json:"alias"`
	UpstreamModel     string        `yaml:"upstream_model" json:"upstream_model"`
	Accounts          []string      `yaml:"accounts" json:"accounts"`
	RoutingStrategy   string        `yaml:"routing_strategy,omitempty" json:"routing_strategy,omitempty"`
	Fallbacks         []string      `yaml:"fallbacks,omitempty" json:"fallbacks,omitempty"`
	InputCostPerMtok  float64       `yaml:"input_cost_per_mtok,omitempty" json:"input_cost_per_mtok,omitempty"`
	OutputCostPerMtok float64       `yaml:"output_cost_per_mtok,omitempty" json:"output_cost_per_mtok,omitempty"`
	Optimization      *Optimization `yaml:"optimization,omitempty" json:"optimization,omitempty"`
	// Cache (G4) enables the per-model exact-match response cache:
	// "" = inherit (off unless the client sends X-Gateway-Cache: true),
	// "exact" = non-streaming responses are cached with no opt-in header.
	// CacheTTL overrides the default 5m window (Go duration string).
	Cache    string `yaml:"cache,omitempty" json:"cache,omitempty"`
	CacheTTL string `yaml:"cache_ttl,omitempty" json:"cache_ttl,omitempty"`
	// Sources (шаг 5 хребта): per-account overrides for multi-provider
	// aliases — one public name served by different providers with their own
	// upstream model names, weights and prices. Empty = legacy 1:1 behavior.
	Sources []ModelSource `yaml:"sources,omitempty" json:"sources,omitempty"`
}

type ModelSource struct {
	Account           string  `yaml:"account" json:"account"`
	UpstreamModel     string  `yaml:"upstream_model,omitempty" json:"upstream_model,omitempty"`
	Weight            int     `yaml:"weight,omitempty" json:"weight,omitempty"` // ordering within THIS alias; overrides account.Weight
	InputCostPerMtok  float64 `yaml:"input_cost_per_mtok,omitempty" json:"input_cost_per_mtok,omitempty"`
	OutputCostPerMtok float64 `yaml:"output_cost_per_mtok,omitempty" json:"output_cost_per_mtok,omitempty"`
}

func (m Model) sourceFor(account string) *ModelSource {
	for i := range m.Sources {
		if m.Sources[i].Account == account {
			return &m.Sources[i]
		}
	}
	return nil
}

// WeightFor reports the per-source ordering weight for an account within
// this alias, when the source defines one.
func (m Model) WeightFor(account string) (int, bool) {
	s := m.sourceFor(account)
	if s != nil && s.Weight > 0 {
		return s.Weight, true
	}
	return 0, false
}

// UpstreamFor resolves the upstream model for an attempt against account:
// the source override wins over the alias default.
func (m Model) UpstreamFor(account string) string {
	if s := m.sourceFor(account); s != nil && s.UpstreamModel != "" {
		return s.UpstreamModel
	}
	return m.UpstreamModel
}

// ResolvedFor returns an attempt-scoped copy of the model with the source's
// upstream name and costs applied — adapters and response rewriting need no
// changes to honor per-provider overrides.
func (m Model) ResolvedFor(account string) Model {
	s := m.sourceFor(account)
	if s == nil {
		return m
	}
	if s.UpstreamModel != "" {
		m.UpstreamModel = s.UpstreamModel
	}
	if s.InputCostPerMtok > 0 {
		m.InputCostPerMtok = s.InputCostPerMtok
	}
	if s.OutputCostPerMtok > 0 {
		m.OutputCostPerMtok = s.OutputCostPerMtok
	}
	return m
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
	// Proxy is an upstream proxy for THIS account (any format, list allowed).
	// Empty = fall back to the global pool, then direct/HTTP_PROXY.
	Proxy string `yaml:"proxy,omitempty" json:"proxy,omitempty"`
}

type Credential struct {
	Kind             string `yaml:"kind,omitempty" json:"kind,omitempty"`
	APIKey           string `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	AccessToken      string `yaml:"access_token,omitempty" json:"access_token,omitempty"`
	RefreshToken     string `yaml:"refresh_token,omitempty" json:"refresh_token,omitempty"`
	IDToken          string `yaml:"id_token,omitempty" json:"id_token,omitempty"`
	Cookies          string `yaml:"cookies,omitempty" json:"cookies,omitempty"`
	OrganizationID   string `yaml:"organization_id,omitempty" json:"organization_id,omitempty"`
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
	LockoutFailures int    `yaml:"lockout_failures,omitempty" json:"lockout_failures,omitempty"`
	LockoutDuration string `yaml:"lockout_duration,omitempty" json:"lockout_duration,omitempty"`
}

type Snapshot struct {
	Config
	KeyHashes                         map[string][]byte
	ModelsByAlias                     map[string]Model
	VirtualKeysByName                 map[string]VirtualKey
	AccountsByName                    map[string]Account
	MCPServersByName                  map[string]McpServer
	GlobalProxies                     []proxylib.Entry            // parsed from Config.Proxies
	AccountProxies                    map[string][]proxylib.Entry // parsed per account (nil = unset)
	StickyTTL, Cooldown, CircuitReset time.Duration
	Lockout                           time.Duration
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
	// Optimization mode presets: fail fast on typos so a misconfigured mode
	// can't silently run as legacy behavior.
	if err := validateOptimizationModes("global", &c.Optimization); err != nil {
		return nil, err
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
	s := &Snapshot{Config: c, KeyHashes: map[string][]byte{}, ModelsByAlias: map[string]Model{}, VirtualKeysByName: map[string]VirtualKey{}, AccountsByName: map[string]Account{}, MCPServersByName: map[string]McpServer{}, AccountProxies: map[string][]proxylib.Entry{}}
	// Global proxy pool (strict: any invalid entry fails the snapshot).
	if len(c.Proxies) > 0 {
		global, err := proxylib.Parse(strings.Join(c.Proxies, "\n"))
		if err != nil {
			return nil, fmt.Errorf("global proxy pool: %w", err)
		}
		s.GlobalProxies = global
	}
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
	s.Lockout, e = parseDur(c.Resilience.LockoutDuration, 15*time.Minute)
	if e != nil {
		return nil, e
	}
	if c.Resilience.LockoutFailures < 0 {
		return nil, errors.New("lockout_failures must be non-negative")
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
			case "openai-compatible", "gemini", "deepseek":
				// Free providers (opencode/felo/qoder) may have no secret.
				if a.Credential.Kind == "free" {
					// no secret required
				} else if a.Credential.Kind == "api_key" && a.Credential.APIKey == "" {
					return nil, fmt.Errorf("%s credential api_key required", a.Adapter)
				}
			case "opencode", "free", "felo", "qoder":
				if a.Credential.Kind != "free" && a.Credential.Kind != "api_key" {
					return nil, fmt.Errorf("%s credential free or api_key required", a.Adapter)
				}
			case "cursor", "copilot", "kimi":
				// OAuth-based providers (added from OmniRoute-style adapter set).
				if a.Credential.Kind == "free" {
					// no secret
				} else if a.Credential.Kind == "oauth" && a.Credential.AccessToken == "" {
					return nil, fmt.Errorf("%s oauth credential requires access_token", a.Adapter)
				} else if a.Credential.Kind == "api_key" && a.Credential.APIKey == "" {
					return nil, fmt.Errorf("%s api_key credential requires api_key", a.Adapter)
				}
			case "anthropic":
				switch a.Credential.Kind {
				case "api_key":
					if a.Credential.APIKey == "" {
						return nil, errors.New("anthropic api_key credential requires api_key")
					}
				case "oauth":
					if a.Credential.AccessToken == "" {
						return nil, errors.New("anthropic oauth credential requires access_token")
					}
				case "cookie":
					if a.Credential.Cookies == "" {
						return nil, errors.New("anthropic cookie credential requires cookies")
					}
				default:
					return nil, fmt.Errorf("unsupported anthropic credential kind %q", a.Credential.Kind)
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
		if a.Proxy != "" {
			entries, perr := proxylib.Parse(a.Proxy)
			if perr != nil {
				return nil, fmt.Errorf("account %s proxy: %w", a.Name, perr)
			}
			s.AccountProxies[a.Name] = entries
		}
		c.Accounts[i] = a
		s.Config.Accounts[i] = a
		s.AccountsByName[a.Name] = a
	}
	for i, m := range c.Models {
		if m.Alias == "" || m.UpstreamModel == "" || len(m.Accounts) == 0 {
			return nil, errors.New("model alias/upstream_model/accounts required")
		}
		if _, exists := s.ModelsByAlias[m.Alias]; exists {
			return nil, fmt.Errorf("duplicate model %s", m.Alias)
		}
		if m.RoutingStrategy == "" {
			m.RoutingStrategy = c.Routing.Strategy
		}
		if !validModelRoutingStrategy(m.RoutingStrategy) {
			return nil, fmt.Errorf("unsupported model routing strategy %s", m.RoutingStrategy)
		}
		if m.InputCostPerMtok < 0 || m.OutputCostPerMtok < 0 {
			return nil, fmt.Errorf("model %s has negative pricing", m.Alias)
		}
		if m.Optimization != nil {
			if err := validateOptimizationModes("model "+m.Alias, m.Optimization); err != nil {
				return nil, err
			}
		}
		knownAccounts := map[string]bool{}
		for _, an := range m.Accounts {
			knownAccounts[an] = true
		}
		seenSource := map[string]bool{}
		for _, src := range m.Sources {
			if !knownAccounts[src.Account] {
				return nil, fmt.Errorf("model %s source references unknown account %s", m.Alias, src.Account)
			}
			if seenSource[src.Account] {
				return nil, fmt.Errorf("model %s has duplicate sources for account %s", m.Alias, src.Account)
			}
			seenSource[src.Account] = true
			if src.Weight < 0 || src.InputCostPerMtok < 0 || src.OutputCostPerMtok < 0 {
				return nil, fmt.Errorf("model %s source %s has negative weight or pricing", m.Alias, src.Account)
			}
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
		c.Models[i] = m
		s.Config.Models[i] = m
		s.ModelsByAlias[m.Alias] = m
	}
	for _, m := range c.Models {
		for _, fb := range m.Fallbacks {
			target, ok := s.ModelsByAlias[fb]
			if !ok {
				return nil, fmt.Errorf("model %s fallback references unknown model %s", m.Alias, fb)
			}
			if target.UpstreamModel == m.UpstreamModel && len(target.Accounts) == len(m.Accounts) {
				return nil, fmt.Errorf("model %s fallback %s is redundant", m.Alias, fb)
			}
		}
		if cycleErr := modelFallbackCycle(c.Models, m.Alias, map[string]bool{}); cycleErr != "" {
			return nil, fmt.Errorf("model fallback cycle involving %s", cycleErr)
		}
		if m.Cache != "" && m.Cache != "off" && m.Cache != "exact" {
			return nil, fmt.Errorf("model %s cache must be off|exact|empty, got %q", m.Alias, m.Cache)
		}
		if m.CacheTTL != "" {
			if d, err := time.ParseDuration(m.CacheTTL); err != nil || d <= 0 {
				return nil, fmt.Errorf("model %s cache_ttl must be a positive Go duration", m.Alias)
			}
		}
	}
	for _, k := range c.VirtualKeys {
		if k.Name == "" || (k.Key == "" && k.KeyHash == "") {
			return nil, errors.New("virtual key name and key or key_hash required")
		}
		if k.RPM < 0 || k.TPM < 0 || k.MaxConcurrency < 0 || k.BudgetUSD < 0 {
			return nil, fmt.Errorf("virtual key %s has negative limit", k.Name)
		}
		if k.BudgetDuration != "" && k.BudgetDuration != "day" && k.BudgetDuration != "month" {
			return nil, fmt.Errorf("virtual key %s budget_duration must be day|month, got %q", k.Name, k.BudgetDuration)
		}
		if k.Optimization != nil {
			if err := validateOptimizationModes("virtual key "+k.Name, k.Optimization); err != nil {
				return nil, err
			}
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
		s.VirtualKeysByName[k.Name] = k
	}
	seenMcp := map[string]bool{}
	for _, srv := range c.MCPServers {
		if srv.Name == "" {
			return nil, errors.New("mcp server name required")
		}
		if seenMcp[srv.Name] {
			return nil, fmt.Errorf("duplicate mcp server %s", srv.Name)
		}
		seenMcp[srv.Name] = true
		if !strings.HasPrefix(srv.URL, "http://") && !strings.HasPrefix(srv.URL, "https://") {
			return nil, fmt.Errorf("mcp server %s needs an http(s) url", srv.Name)
		}
		s.MCPServersByName[srv.Name] = srv
	}
	if len(s.VirtualKeys) == 0 {
		return nil, errors.New("at least one virtual key required")
	}
	if c.Guardrails.Mode != "" {
		switch c.Guardrails.Mode {
		case "off", "log", "redact", "block":
		default:
			return nil, fmt.Errorf("guardrails mode must be off|log|redact|block, got %q", c.Guardrails.Mode)
		}
	}
	return s, nil
}

func modelFallbackCycle(models []Model, start string, path map[string]bool) string {
	if path[start] {
		return start
	}
	path[start] = true
	defer delete(path, start)
	var next Model
	for _, m := range models {
		if m.Alias == start {
			next = m
			break
		}
	}
	for _, fb := range next.Fallbacks {
		if cycle := modelFallbackCycle(models, fb, path); cycle != "" {
			return cycle
		}
	}
	return ""
}
func validModelRoutingStrategy(strategy string) bool {
	switch strategy {
	case "balanced", "priority", "weighted", "fallback-chain", "fastest", "cheapest", "quota-drain", "round_robin", "quota_failover", "p2c", "least-used", "lkgp", "reset-aware", "adaptive":
		return true
	default:
		return false
	}
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

// validateOptimizationModes rejects unknown optimization mode presets so a
// typo can't silently run as legacy behavior (fail-fast, like license features).
func validateOptimizationModes(where string, o *Optimization) error {
	rtk := map[string]bool{"": true, "light": true, "standard": true, "aggressive": true, "auto": true}
	cav := map[string]bool{"": true, "lite": true, "full": true, "auto": true}
	hr := map[string]bool{"": true, "conservative": true, "balanced": true, "aggressive": true, "auto": true}
	if !rtk[o.RTKMode] {
		return fmt.Errorf("%s: invalid rtk_mode %q", where, o.RTKMode)
	}
	if !cav[o.CavemanMode] {
		return fmt.Errorf("%s: invalid caveman_mode %q", where, o.CavemanMode)
	}
	if !hr[o.HeadroomProfile] {
		return fmt.Errorf("%s: invalid headroom_profile %q", where, o.HeadroomProfile)
	}
	// squoze is an experimental EXCLUSIVE mode: it replaces (not combines
	// with) the built-in optimizers, so mixing them is a config error.
	if o.Squoze {
		var conflicts []string
		if o.RTKMode != "" || o.RTKCompression {
			conflicts = append(conflicts, "rtk")
		}
		if o.CavemanMode != "" || o.Caveman {
			conflicts = append(conflicts, "caveman")
		}
		if o.HeadroomProfile != "" || o.Headroom {
			conflicts = append(conflicts, "headroom")
		}
		if len(conflicts) > 0 {
			return fmt.Errorf("%s: squoze mode is exclusive and cannot be combined with: %s",
				where, strings.Join(conflicts, ", "))
		}
	}
	return nil
}

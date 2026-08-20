package thirdparty

import (
	"net/http"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/adapter/oauthrefresh"
)

// Provider specs — subscription/free OpenAI-compatible accounts.
// Base URLs are defaults users can override per-account in config; free
// endpoints change over time (9Router notes "subject to change"), so the
// config base_url always wins.
var (
	CursorSpec = Spec{
		Name:           "cursor",
		DefaultBaseURL: "https://api2.cursor.sh",
		Headers: map[string]string{
			"User-Agent":          "cursor/1.0.0 (official CLI)",
			"X-Cursor-Client":     "cursor-cli",
			"X-Origin":            "cursor",
			"X-Requested-With":    "XMLHttpRequest",
		},
		SupportsOAuth: true,
	}
	CopilotSpec = Spec{
		Name:           "copilot",
		DefaultBaseURL: "https://api.githubcopilot.com",
		Headers: map[string]string{
			"User-Agent":          "GitHubCopilot-CLI/0.1.0",
			"X-GitHub-Copilot-CLI": "1",
		},
		SupportsOAuth: true,
	}
	KimiSpec = Spec{
		Name:           "kimi",
		DefaultBaseURL: "https://api.moonshot.cn/v1",
		Headers: map[string]string{
			"User-Agent":  "kimi-code/1.0.0 (official CLI)",
			"X-Kimi-CLI":  "1",
		},
		SupportsOAuth: true,
		FreeByDefault: true,
	}
	OpenCodeSpec = Spec{
		Name:           "opencode",
		DefaultBaseURL: "https://opencode.ai/v1",
		Headers: map[string]string{
			"User-Agent": "opencode/1.0.0 (official CLI)",
		},
		FreeByDefault: true,
	}
	FeloSpec = Spec{
		Name:           "felo",
		DefaultBaseURL: "https://api.felo.ai/v1",
		Headers: map[string]string{
			"User-Agent": "felo/1.0.0 (official CLI)",
		},
		FreeByDefault: true,
	}
	QoderSpec = Spec{
		Name:           "qoder",
		DefaultBaseURL: "https://api.qoder.ai/v1",
		Headers: map[string]string{
			"User-Agent": "qoder/1.0.0 (official CLI)",
		},
		FreeByDefault: true,
	}
)

// RegisterPlugins registers all thirdparty providers into a fresh registry.
func RegisterPlugins(reg *adapter.Registry, client *http.Client) error {
	if client == nil {
		return nil
	}
	_ = RegisterIfAbsent(reg, OpenCodeSpec, client)
	_ = RegisterIfAbsent(reg, FeloSpec, client)
	_ = RegisterIfAbsent(reg, QoderSpec, client)
	_ = RegisterIfAbsent(reg, CursorSpec, client)
	_ = RegisterIfAbsent(reg, CopilotSpec, client)
	_ = RegisterIfAbsent(reg, KimiSpec, client)
	return nil
}

func RegisterIfAbsent(reg *adapter.Registry, spec Spec, client *http.Client) bool {
	if _, ok := reg.Get(spec.Name); ok {
		return false
	}
	_ = reg.Register(spec.Name, New(client, spec))
	return true
}

// Trigger and Sink alias oauthrefresh interfaces for main.go wiring.
type Trigger = oauthrefresh.SnapshotRefreshTrigger
type Sink = oauthrefresh.CredentialSink
type ControlPlaneSink = oauthrefresh.ControlPlaneSink

// RegisterOAuth registers OAuth-capable thirdparty adapters (cursor, copilot,
// kimi) with token refresh. Idempotent: replaces in-memory adapters when a
// control-plane sink is available.
func RegisterOAuth(reg *adapter.Registry, client *http.Client, sink Sink, trigger Trigger) error {
	if client == nil {
		return nil
	}
	oauthSpecs := []Spec{CursorSpec, CopilotSpec, KimiSpec}
	for _, spec := range oauthSpecs {
		_ = RegisterWithAuth(reg, spec, client, sink, trigger)
	}
	return nil
}

// ControlPlaneClient is a tiny helper for main.go to fetch the shared client.
func (a *Adapter) SharedClient() *http.Client { return a.Client }

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/Rethinger/2papi/internal/config"
)

const maxModelDiscoveryBody = 1 << 20

type modelClient struct {
	client  *http.Client
	options Options
}

func newModelClient(client *http.Client, options Options) *modelClient {
	return &modelClient{client: client, options: options}
}

type ModelDiscovery struct {
	Models []CodexModel `json:"models"`
}
type CodexModel struct {
	Slug                  string          `json:"slug"`
	DisplayName           string          `json:"display_name,omitempty"`
	Description           string          `json:"description,omitempty"`
	Visibility            string          `json:"visibility,omitempty"`
	SupportedInAPI        bool            `json:"supported_in_api"`
	ContextWindow         int64           `json:"context_window,omitempty"`
	WebSearchToolType     string          `json:"web_search_tool_type,omitempty"`
	SupportsSearchTool    *bool           `json:"supports_search_tool,omitempty"`
	InputModalities       []string        `json:"input_modalities,omitempty"`
	ToolMode              string          `json:"tool_mode,omitempty"`
	SupportsParallelCalls *bool           `json:"supports_parallel_tool_calls,omitempty"`
	AvailableInPlans      []string        `json:"available_in_plans,omitempty"`
	Capabilities          json.RawMessage `json:"capabilities,omitempty"`
}

func (m *modelClient) discover(ctx context.Context, cred config.Credential) (json.RawMessage, error) {
	raw, err := m.getModels(ctx, cred)
	if err != nil {
		return nil, err
	}
	var env struct {
		Models []CodexModel `json:"models"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	if env.Models == nil {
		env.Models = []CodexModel{}
	}
	for i := range env.Models {
		enrichModel(&env.Models[i])
	}
	env.Models = appendImageModels(env.Models)
	return json.Marshal(ModelDiscovery{Models: env.Models})
}

// appendImageModels adds image generation models from the supplementary
// catalog when the backend list does not mention them. The ChatGPT models
// endpoint omits image models entirely, so the catalog is the only source of
// their discovery rows. Backend entries always win and are never duplicated.
func appendImageModels(models []CodexModel) []CodexModel {
	known := make(map[string]bool, len(models))
	for _, model := range models {
		known[model.Slug] = true
	}
	slugs := make([]string, 0, len(imageModelCatalog))
	for slug := range imageModelCatalog {
		if !known[slug] {
			slugs = append(slugs, slug)
		}
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		entry := imageModelCatalog[slug]
		models = append(models, CodexModel{
			Slug:            slug,
			DisplayName:     entry.DisplayName,
			Description:     entry.Description,
			Visibility:      "list",
			SupportedInAPI:  true,
			InputModalities: entry.InputModalities,
			Capabilities:    mergeCapabilities(nil, entry.ReasoningLevels, true),
		})
	}
	return models
}

// enrichModel fills gaps in the sparse backend payload from the bundled
// openai/codex catalog snapshot. Backend data always wins when present.
func enrichModel(model *CodexModel) {
	entry, known := modelCatalog[model.Slug]
	imageGeneration := false
	if !known {
		entry, known = imageModelCatalog[model.Slug]
		imageGeneration = known
	}
	if !known {
		return
	}
	if model.DisplayName == "" {
		model.DisplayName = entry.DisplayName
	}
	if model.Description == "" {
		model.Description = entry.Description
	}
	if model.ContextWindow == 0 {
		model.ContextWindow = entry.ContextWindow
	}
	if model.WebSearchToolType == "" {
		model.WebSearchToolType = entry.WebSearchToolType
	}
	if model.SupportsSearchTool == nil && entry.SupportsSearchTool {
		model.SupportsSearchTool = boolPtr(true)
	}
	if model.InputModalities == nil && len(entry.InputModalities) > 0 {
		model.InputModalities = entry.InputModalities
	}
	if model.ToolMode == "" {
		model.ToolMode = entry.ToolMode
	}
	if model.SupportsParallelCalls == nil && entry.SupportsParallelCalls {
		model.SupportsParallelCalls = boolPtr(true)
	}
	if model.AvailableInPlans == nil && len(entry.AvailableInPlans) > 0 {
		model.AvailableInPlans = entry.AvailableInPlans
	}
	model.Capabilities = mergeCapabilities(model.Capabilities, entry.ReasoningLevels, imageGeneration)
}

func mergeCapabilities(raw json.RawMessage, reasoningLevels []string, imageGeneration bool) json.RawMessage {
	if len(raw) > 0 && !json.Valid(raw) {
		return raw
	}
	merged := map[string]any{}
	if len(raw) > 0 {
		var existing map[string]any
		if err := json.Unmarshal(raw, &existing); err == nil {
			merged = existing
		}
	}
	if _, present := merged["reasoning"]; !present && len(reasoningLevels) > 0 {
		merged["reasoning"] = true
	}
	if _, present := merged["image_generation"]; !present && imageGeneration {
		merged["image_generation"] = true
	}
	if len(merged) == 0 {
		return nil
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return raw
	}
	return out
}

func boolPtr(value bool) *bool { return &value }

func (m *modelClient) validate(ctx context.Context, cred config.Credential) error {
	_, err := m.getModels(ctx, cred)
	return err
}

func (m *modelClient) getModels(ctx context.Context, cred config.Credential) ([]byte, error) {
	base := strings.TrimRight(m.options.BackendBaseURL, "/")
	u, err := url.Parse(base + "/backend-api/codex/models")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("client_version", m.options.ClientVersion)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("ChatGPT-Account-ID", cred.ChatGPTAccountID)
	req.Header.Set("client_version", m.options.ClientVersion)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := readLimited(resp.Body, maxModelDiscoveryBody)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, unauthorizedError{}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("codex model discovery status %d", resp.StatusCode)
	}
	return body, nil
}

type unauthorizedError struct{}

func (unauthorizedError) Error() string { return "codex unauthorized" }
func isUnauthorized(err error) bool     { var e unauthorizedError; return errors.As(err, &e) }

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("codex response body too large")
	}
	return b, nil
}

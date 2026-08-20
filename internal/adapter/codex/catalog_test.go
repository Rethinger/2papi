package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/config"
)

func discoverFrom(t *testing.T, payload string) map[string]any {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/backend-api/codex/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(payload))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()
	a := New(ts.Client(), nil, nil, Options{TestMode: true, AuthBaseURL: ts.URL, BackendBaseURL: ts.URL, Now: time.Now})
	out, err := a.Operate(context.Background(), adapter.Operation{Kind: adapter.OperationDiscoverModels, Account: config.Account{ID: "id", Credential: config.Credential{AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339), Revision: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Data, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func findModel(t *testing.T, models []any, slug string) map[string]any {
	t.Helper()
	for _, item := range models {
		model := item.(map[string]any)
		if model["slug"] == slug {
			return model
		}
	}
	t.Fatalf("model %q missing from %v", slug, models)
	return nil
}

func countSlug(t *testing.T, models []any, slug string) int {
	t.Helper()
	count := 0
	for _, item := range models {
		if item.(map[string]any)["slug"] == slug {
			count++
		}
	}
	return count
}

func TestDiscoverEnrichesKnownModelFromCatalog(t *testing.T) {
	payload := `{"models":[{"slug":"gpt-5.6-luna","visibility":"list","supported_in_api":true}]}`
	got := discoverFrom(t, payload)
	models, ok := got["models"].([]any)
	if !ok || len(models) == 0 {
		t.Fatalf("models missing: %v", got)
	}
	model := findModel(t, models, "gpt-5.6-luna")
	if model["display_name"] != "GPT-5.6-Luna" {
		t.Fatalf("display_name=%v", model["display_name"])
	}
	if model["context_window"] != float64(272000) {
		t.Fatalf("context_window=%v", model["context_window"])
	}
	if model["supports_search_tool"] != true {
		t.Fatalf("supports_search_tool=%v", model["supports_search_tool"])
	}
	if model["web_search_tool_type"] != "text_and_image" {
		t.Fatalf("web_search_tool_type=%v", model["web_search_tool_type"])
	}
	modalities, ok := model["input_modalities"].([]any)
	if !ok || len(modalities) != 2 || modalities[1] != "image" {
		t.Fatalf("input_modalities=%v", model["input_modalities"])
	}
	if model["supports_parallel_tool_calls"] != true {
		t.Fatalf("supports_parallel_tool_calls=%v", model["supports_parallel_tool_calls"])
	}
	plans, ok := model["available_in_plans"].([]any)
	if !ok || len(plans) == 0 {
		t.Fatalf("available_in_plans=%v", model["available_in_plans"])
	}
	capabilities, ok := model["capabilities"].(map[string]any)
	if !ok || capabilities["reasoning"] != true {
		t.Fatalf("capabilities=%v", model["capabilities"])
	}
}

func TestDiscoverKeepsBackendValuesWhenPresent(t *testing.T) {
	payload := `{"models":[{"slug":"gpt-5.6-terra","display_name":"Backend Name","context_window":999,"capabilities":{"tools":true}}]}`
	got := discoverFrom(t, payload)
	model := got["models"].([]any)[0].(map[string]any)
	if model["display_name"] != "Backend Name" {
		t.Fatalf("backend display_name overridden: %v", model["display_name"])
	}
	if model["context_window"] != float64(999) {
		t.Fatalf("backend context_window overridden: %v", model["context_window"])
	}
	capabilities := model["capabilities"].(map[string]any)
	if capabilities["tools"] != true {
		t.Fatalf("backend capabilities lost: %v", model["capabilities"])
	}
	if capabilities["reasoning"] != true {
		t.Fatalf("catalog reasoning not merged: %v", model["capabilities"])
	}
}

func TestDiscoverLeavesUnknownModelUntouched(t *testing.T) {
	payload := `{"models":[{"slug":"codex-mini","visibility":"allow","supported_in_api":true,"context_window":128000,"capabilities":{"tools":true}}]}`
	got := discoverFrom(t, payload)
	model := got["models"].([]any)[0].(map[string]any)
	if _, present := model["display_name"]; present {
		t.Fatalf("unknown model enriched: %v", model)
	}
	if _, present := model["supports_search_tool"]; present {
		t.Fatalf("unknown model enriched: %v", model)
	}
	if _, present := model["available_in_plans"]; present {
		t.Fatalf("unknown model enriched: %v", model)
	}
	if model["capabilities"].(map[string]any)["reasoning"] == true {
		t.Fatalf("reasoning invented for unknown model: %v", model["capabilities"])
	}
}

func TestMergeCapabilitiesPreservesMalformedInput(t *testing.T) {
	if out := mergeCapabilities(json.RawMessage(`{invalid`), []string{"low"}, false); string(out) != `{invalid` {
		t.Fatalf("malformed capabilities mangled: %s", out)
	}
	if out := mergeCapabilities(nil, nil, false); out != nil {
		t.Fatalf("empty merge should be nil: %s", out)
	}
}

func TestDiscoverEnrichesImageModelsFromCatalog(t *testing.T) {
	payload := `{"models":[{"slug":"gpt-image-1","visibility":"allow","supported_in_api":true},{"slug":"dall-e-3","visibility":"allow","supported_in_api":true}]}`
	got := discoverFrom(t, payload)
	models, ok := got["models"].([]any)
	if !ok || len(models) == 0 {
		t.Fatalf("models missing: %v", got)
	}
	if countSlug(t, models, "gpt-image-1") != 1 || countSlug(t, models, "dall-e-3") != 1 {
		t.Fatalf("image models duplicated: %v", models)
	}
	first := findModel(t, models, "gpt-image-1")
	if first["display_name"] != "GPT Image 1" {
		t.Fatalf("display_name=%v", first["display_name"])
	}
	modalities, ok := first["input_modalities"].([]any)
	if !ok || len(modalities) != 2 || modalities[1] != "image" {
		t.Fatalf("input_modalities=%v", first["input_modalities"])
	}
	if first["capabilities"].(map[string]any)["image_generation"] != true {
		t.Fatalf("capabilities=%v", first["capabilities"])
	}
	second := findModel(t, models, "dall-e-3")
	if second["display_name"] != "DALL·E 3" {
		t.Fatalf("display_name=%v", second["display_name"])
	}
	if second["capabilities"].(map[string]any)["image_generation"] != true {
		t.Fatalf("capabilities=%v", second["capabilities"])
	}
}

func TestDiscoverAppendsImageModelsWhenBackendOmitsThem(t *testing.T) {
	payload := `{"models":[{"slug":"gpt-5-codex","visibility":"allow","supported_in_api":true}]}`
	got := discoverFrom(t, payload)
	models := got["models"].([]any)
	for _, slug := range []string{"gpt-image-1", "gpt-image-2", "gpt-5.5-image", "dall-e-3"} {
		model := findModel(t, models, slug)
		if model["supported_in_api"] != true {
			t.Fatalf("%s supported_in_api=%v", slug, model["supported_in_api"])
		}
		if model["visibility"] != "list" {
			t.Fatalf("%s visibility=%v", slug, model["visibility"])
		}
		if model["capabilities"].(map[string]any)["image_generation"] != true {
			t.Fatalf("%s capabilities=%v", slug, model["capabilities"])
		}
	}
}

func TestDiscoverKeepsBackendImageGenerationValue(t *testing.T) {
	payload := `{"models":[{"slug":"gpt-image-1","visibility":"allow","supported_in_api":true,"capabilities":{"image_generation":false}}]}`
	got := discoverFrom(t, payload)
	model := findModel(t, got["models"].([]any), "gpt-image-1")
	if model["capabilities"].(map[string]any)["image_generation"] != false {
		t.Fatalf("backend image_generation overridden: %v", model["capabilities"])
	}
}

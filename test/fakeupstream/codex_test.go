package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFakeCodexResetDeduplicatesRedeemRequestID(t *testing.T) {
	server := httptest.NewServer(newCodexHandler())
	defer server.Close()
	for range 2 {
		response, err := http.Post(server.URL+"/backend-api/wham/rate-limit-reset-credits/consume", "application/json", strings.NewReader(`{"redeem_request_id":"66d28bee-2f55-41fc-aba8-b8ef8b07a923"}`))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", response.StatusCode)
		}
	}
	response, err := http.Get(server.URL + "/__test/counters")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var counters map[string]int
	if err := json.NewDecoder(response.Body).Decode(&counters); err != nil {
		t.Fatal(err)
	}
	if counters["reset_consume_calls"] != 2 || counters["reset_consume"] != 1 {
		t.Fatalf("counters=%v", counters)
	}
	credits, err := http.Get(server.URL + "/backend-api/wham/rate-limit-reset-credits")
	if err != nil {
		t.Fatal(err)
	}
	defer credits.Body.Close()
	var state struct {
		AvailableCount int `json:"available_count"`
	}
	if err := json.NewDecoder(credits.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	if state.AvailableCount != 0 {
		t.Fatalf("available_count=%d", state.AvailableCount)
	}
}

func TestFakeCodexResponsesStreamsDeterministicTextAndToolCall(t *testing.T) {
	server := httptest.NewServer(newCodexHandler())
	defer server.Close()
	response, err := http.Post(server.URL+"/backend-api/codex/responses", "application/json", strings.NewReader(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body strings.Builder
	_, _ = io.Copy(&body, response.Body)
	text := body.String()
	if response.Header.Get("Content-Type") != "text/event-stream" || !strings.Contains(text, `"type":"response.output_text.delta"`) || !strings.Contains(text, `"delta":"fake codex reply"`) || !strings.Contains(text, `"type":"response.completed"`) || !strings.Contains(text, "data: [DONE]") {
		t.Fatalf("headers=%v body=%s", response.Header, text)
	}
}

package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/config"
)

type fakeAdapter struct {
	got    []adapter.Operation
	result adapter.OperationResult
	err    error
}

func (f *fakeAdapter) Execute(context.Context, adapter.Execution) (*adapter.Result, error) {
	return nil, nil
}
func (f *fakeAdapter) Operate(_ context.Context, op adapter.Operation) (adapter.OperationResult, error) {
	f.got = append(f.got, op)
	if f.err != nil {
		return adapter.OperationResult{}, f.err
	}
	if len(f.result.Data) == 0 {
		f.result.Data = json.RawMessage(`{"ok":true}`)
	}
	return f.result, nil
}

func TestOperationServerAuthBodyAndDispatch(t *testing.T) {
	var logs bytes.Buffer
	previousLogWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previousLogWriter)
	reg := adapter.NewRegistry()
	fa := &fakeAdapter{}
	_ = reg.Register("openai-codex", fa)
	h := NewServer(reg, "secret-token").Routes()
	body := []byte(`{"operation":"discover_models","account":{"id":"a1","name":"n","adapter":"openai-codex","base_url":"https://x","credential":{"kind":"oauth","access_token":"one-shot-access-token","refresh_token":"one-shot-refresh-token","id_token":"one-shot-id-token","chatgpt_account_id":"acct","revision":1},"enabled":true,"priority":0,"weight":1,"max_concurrency":3,"cost":0},"input":{},"idempotency_key":""}`)
	for _, tc := range []struct {
		name, token string
		want        int
	}{{"missing", "", 401}, {"wrong", "bad", 401}, {"ok", "secret-token", 200}} {
		r := httptest.NewRequest(http.MethodPost, "/internal/v1/provider-operations", bytes.NewReader(body))
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s got %d want %d body %s", tc.name, w.Code, tc.want, w.Body.String())
		}
		for _, secret := range []string{"one-shot-access-token", "one-shot-refresh-token", "one-shot-id-token", "secret-token", bodyString(body)} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("%s response leaked secret %q", tc.name, secret)
			}
		}
	}
	if len(fa.got) != 1 {
		t.Fatalf("adapter dispatch count=%d", len(fa.got))
	}
	if fa.got[0].Account.Credential.AccessToken != "one-shot-access-token" {
		t.Fatal("credential not passed to adapter")
	}
	for _, secret := range []string{"one-shot-access-token", "one-shot-refresh-token", "one-shot-id-token", "secret-token", bodyString(body)} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("gateway logs leaked secret %q", secret)
		}
	}
}

func bodyString(body []byte) string { return string(body) }

func TestOperationServerRejectsOversizedBody(t *testing.T) {
	h := NewServer(adapter.NewRegistry(), "secret-token").Routes()
	prefix := []byte(`{"operation":"discover_models","account":{"id":"a1","adapter":"openai-codex","base_url":"https://example.test","credential":{"kind":"oauth","access_token":"secret","revision":1}},"input":{"padding":"`)
	suffix := []byte(`"}}`)
	body := append(prefix, bytes.Repeat([]byte("x"), maxOperationBody)...)
	body = append(body, suffix...)
	r := httptest.NewRequest(http.MethodPost, "/internal/v1/provider-operations", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer secret-token")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestOperationServerRejectsUnknownAdapter(t *testing.T) {
	h := NewServer(adapter.NewRegistry(), "secret-token").Routes()
	r := httptest.NewRequest(http.MethodPost, "/internal/v1/provider-operations", bytes.NewReader([]byte(`{"operation":"discover_models","account":{"adapter":"missing"},"input":{}}`)))
	r.Header.Set("Authorization", "Bearer secret-token")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("got %d", w.Code)
	}
}

func TestOperationServerRejectsUnknownOperationEmptyTokenAndTrailingJSON(t *testing.T) {
	reg := adapter.NewRegistry()
	_ = reg.Register("openai-codex", &fakeAdapter{})
	validAccount := `{"id":"a1","adapter":"openai-codex","base_url":"https://example.test","credential":{"kind":"oauth","access_token":"secret","revision":1}}`
	for _, tc := range []struct {
		name, token, body string
		want              int
	}{
		{"unknown operation", "secret-token", `{"operation":"erase_everything","account":` + validAccount + `,"input":{}}`, http.StatusBadRequest},
		{"empty configured token", "", `{"operation":"discover_models","account":` + validAccount + `,"input":{}}`, http.StatusUnauthorized},
		{"trailing json", "secret-token", `{"operation":"discover_models","account":` + validAccount + `,"input":{}} {}`, http.StatusBadRequest},
	} {
		h := NewServer(reg, tc.token).Routes()
		r := httptest.NewRequest(http.MethodPost, "/internal/v1/provider-operations", strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s got %d want %d body=%s", tc.name, w.Code, tc.want, w.Body.String())
		}
	}
}

func TestOperationServerUsesCurrentRegistryAndTypedResult(t *testing.T) {
	first := adapter.NewRegistry()
	second := adapter.NewRegistry()
	fa := &fakeAdapter{result: adapter.OperationResult{Data: json.RawMessage(`{"models":[]}`), WarningCode: "credential_persistence_degraded", CredentialRevision: 8}}
	_ = second.Register("openai-codex", fa)
	current := first
	h := NewDynamicServer(func() *adapter.Registry { return current }, "secret-token").Routes()
	body := `{"operation":"discover_models","account":{"id":"a1","adapter":"openai-codex","base_url":"https://example.test","credential":{"kind":"oauth","access_token":"secret","revision":7}},"input":{}}`

	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/internal/v1/provider-operations", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer secret-token")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if got := request(); got.Code != http.StatusBadRequest {
		t.Fatalf("first registry status=%d body=%s", got.Code, got.Body.String())
	}
	current = second
	got := request()
	if got.Code != http.StatusOK {
		t.Fatalf("second registry status=%d body=%s", got.Code, got.Body.String())
	}
	var response Response
	if err := json.Unmarshal(got.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.CredentialRevision != 8 || response.WarningCode != "credential_persistence_degraded" {
		t.Fatalf("response=%+v", response)
	}

	fa.err = &adapter.OperationError{Code: "credential_revision_conflict"}
	if conflict := request(); conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "credential_revision_conflict") {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestOperationServerRequiresJSONContentType(t *testing.T) {
	h := NewServer(adapter.NewRegistry(), "secret-token").Routes()
	r := httptest.NewRequest(http.MethodPost, "/internal/v1/provider-operations", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer secret-token")
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType || !strings.Contains(w.Body.String(), "unsupported_media_type") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

var _ = config.Credential{}

package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/1jehuang/2papi/internal/adapter"
)

func TestConsumeResetCreditSendsStoredRedeemRequestIDOnce(t *testing.T) {
	const redeemID = "66d28bee-2f55-41fc-aba8-b8ef8b07a923"
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != resetCreditsConsumePath || r.Method != http.MethodPost {
			t.Fatalf("method/path=%s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Equal(body, []byte(`{"redeem_request_id":"`+redeemID+`"}`)) {
			t.Fatalf("body=%s", body)
		}
		if r.Header.Get("Authorization") != "Bearer access" || r.Header.Get("ChatGPT-Account-ID") != "acct" {
			t.Fatalf("missing Codex authorization headers")
		}
		_, _ = io.WriteString(w, `{"success":true,"credit_id":"must-not-be-returned"}`)
	}))
	defer up.Close()

	ad := New(up.Client(), nil, nil, Options{TestMode: true, BackendBaseURL: up.URL})
	result, err := ad.Operate(context.Background(), adapter.Operation{
		Kind: adapter.OperationConsumeResetCredit, Account: codexAccount(),
		Input: json.RawMessage(`{"redeem_request_id":"` + redeemID + `"}`), IdempotencyKey: "browser-request-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || string(result.Data) != `{"consumed":true}` || strings.Contains(string(result.Data), "credit_id") {
		t.Fatalf("calls=%d data=%s", calls, result.Data)
	}
}

func TestConsumeResetCreditRejectsMissingRedeemRequestIDBeforeDispatch(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer up.Close()
	ad := New(up.Client(), nil, nil, Options{TestMode: true, BackendBaseURL: up.URL})
	_, err := ad.Operate(context.Background(), adapter.Operation{Kind: adapter.OperationConsumeResetCredit, Account: codexAccount(), Input: json.RawMessage(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "codex_reset_request_invalid") || calls != 0 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestReadUsageNormalizesQuotaAndDropsIdentity(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != usagePath {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer access" || r.Header.Get("ChatGPT-Account-ID") != "acct" {
			t.Fatalf("missing Codex authorization headers")
		}
		_, _ = io.WriteString(w, `{"user_id":"user-secret","account_id":"account-secret","email":"private@example.com","plan_type":"plus","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":42.5,"limit_window_seconds":604800,"reset_after_seconds":120,"reset_at":1787122967},"secondary_window":null},"code_review_rate_limit":null,"additional_rate_limits":null,"credits":{"has_credits":false,"unlimited":false,"overage_limit_reached":false,"balance":"0","approx_local_messages":[0,0],"approx_cloud_messages":[0,0]},"spend_control":{"reached":false,"individual_limit":null},"rate_limit_reached_type":null,"promo":null,"rate_limit_reset_credits":{"available_count":0,"applicable_available_count":0}}`)
	}))
	defer up.Close()

	ad := New(up.Client(), nil, nil, Options{TestMode: true, BackendBaseURL: up.URL, Now: func() time.Time { return now }})
	result, err := ad.Operate(context.Background(), adapter.Operation{Kind: adapter.OperationReadUsage, Account: codexAccount()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result.Data), "user-secret") || strings.Contains(string(result.Data), "account-secret") || strings.Contains(string(result.Data), "private@example.com") {
		t.Fatalf("identity leaked in normalized quota: %s", result.Data)
	}
	var got map[string]any
	if err := json.Unmarshal(result.Data, &got); err != nil {
		t.Fatal(err)
	}
	if got["plan_type"] != "plus" || got["fetched_at"] != now.Format(time.RFC3339) {
		t.Fatalf("unexpected quota: %s", result.Data)
	}
	rateLimit := got["rate_limit"].(map[string]any)
	primary := rateLimit["primary_window"].(map[string]any)
	if primary["used_percent"] != 42.5 || primary["reset_after_seconds"] != float64(120) {
		t.Fatalf("unexpected primary window: %#v", primary)
	}
}

func TestListResetCreditsReturnsOnlySafeSummary(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != resetCreditsPath {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"credits":[{"id":"credit-secret","expires_at":"2026-08-20T00:00:00Z"}],"available_count":1,"total_earned_count":3,"immediate_reset_purchase_eligible":false}`)
	}))
	defer up.Close()

	ad := New(up.Client(), nil, nil, Options{TestMode: true, BackendBaseURL: up.URL, Now: func() time.Time { return now }})
	result, err := ad.Operate(context.Background(), adapter.Operation{Kind: adapter.OperationListResetCredits, Account: codexAccount()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result.Data), "credit-secret") {
		t.Fatalf("reset credit ID leaked: %s", result.Data)
	}
	var got map[string]any
	if err := json.Unmarshal(result.Data, &got); err != nil {
		t.Fatal(err)
	}
	if got["available_count"] != float64(1) || got["next_expires_at"] != "2026-08-20T00:00:00Z" || got["fetched_at"] != now.Format(time.RFC3339) {
		t.Fatalf("unexpected reset summary: %s", result.Data)
	}
}

func TestReadUsageRejectsChangedContract(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"plan_type":"plus","new_rate_limit_shape":{}}`)
	}))
	defer up.Close()
	ad := New(up.Client(), nil, nil, Options{TestMode: true, BackendBaseURL: up.URL})
	_, err := ad.Operate(context.Background(), adapter.Operation{Kind: adapter.OperationReadUsage, Account: codexAccount()})
	if err == nil || !strings.Contains(err.Error(), "quota_contract_changed") {
		t.Fatalf("err=%v", err)
	}
}

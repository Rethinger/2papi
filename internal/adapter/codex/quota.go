package codex

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/1jehuang/2papi/internal/adapter"
	"github.com/1jehuang/2papi/internal/config"
)

const (
	usagePath               = "/backend-api/wham/usage"
	resetCreditsPath        = "/backend-api/wham/rate-limit-reset-credits"
	resetCreditsConsumePath = "/backend-api/wham/rate-limit-reset-credits/consume"
	maxQuotaBody            = 1 << 20
)

type quotaClient struct {
	client  *http.Client
	options Options
}

type consumeResetCreditInput struct {
	RedeemRequestID string `json:"redeem_request_id"`
}

type quotaWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type quotaRateLimit struct {
	Allowed         bool         `json:"allowed"`
	LimitReached    bool         `json:"limit_reached"`
	PrimaryWindow   *quotaWindow `json:"primary_window"`
	SecondaryWindow *quotaWindow `json:"secondary_window"`
}

type quotaCredits struct {
	HasCredits          bool    `json:"has_credits"`
	Unlimited           bool    `json:"unlimited"`
	OverageLimitReached bool    `json:"overage_limit_reached"`
	Balance             string  `json:"balance"`
	ApproxLocalMessages []int64 `json:"approx_local_messages"`
	ApproxCloudMessages []int64 `json:"approx_cloud_messages"`
}

type usageEnvelope struct {
	PlanType              string          `json:"plan_type"`
	RateLimit             *quotaRateLimit `json:"rate_limit"`
	CodeReviewRateLimit   *quotaRateLimit `json:"code_review_rate_limit"`
	AdditionalRateLimits  json.RawMessage `json:"additional_rate_limits"`
	Credits               quotaCredits    `json:"credits"`
	RateLimitResetCredits struct {
		AvailableCount           int `json:"available_count"`
		ApplicableAvailableCount int `json:"applicable_available_count"`
	} `json:"rate_limit_reset_credits"`
}

type normalizedUsage struct {
	PlanType              string          `json:"plan_type"`
	RateLimit             *quotaRateLimit `json:"rate_limit"`
	CodeReviewRateLimit   *quotaRateLimit `json:"code_review_rate_limit"`
	Credits               quotaCredits    `json:"credits"`
	RateLimitResetCredits struct {
		AvailableCount           int `json:"available_count"`
		ApplicableAvailableCount int `json:"applicable_available_count"`
	} `json:"rate_limit_reset_credits"`
	FetchedAt string `json:"fetched_at"`
}

type resetCreditsEnvelope struct {
	Credits []struct {
		ExpiresAt string `json:"expires_at"`
	} `json:"credits"`
	AvailableCount                 int  `json:"available_count"`
	TotalEarnedCount               int  `json:"total_earned_count"`
	ImmediateResetPurchaseEligible bool `json:"immediate_reset_purchase_eligible"`
}

type resetCreditsSummary struct {
	AvailableCount                 int    `json:"available_count"`
	TotalEarnedCount               int    `json:"total_earned_count"`
	ImmediateResetPurchaseEligible bool   `json:"immediate_reset_purchase_eligible"`
	NextExpiresAt                  string `json:"next_expires_at,omitempty"`
	FetchedAt                      string `json:"fetched_at"`
}

func newQuotaClient(client *http.Client, options Options) *quotaClient {
	return &quotaClient{client: client, options: options}
}

func (q *quotaClient) readUsage(ctx context.Context, cred config.Credential) (json.RawMessage, error) {
	body, err := q.get(ctx, usagePath, cred)
	if err != nil {
		return nil, err
	}
	var source usageEnvelope
	if err := json.Unmarshal(body, &source); err != nil || source.PlanType == "" || source.RateLimit == nil || source.RateLimit.PrimaryWindow == nil {
		return nil, &adapter.OperationError{Code: "codex_quota_contract_changed"}
	}
	if !validQuotaWindow(source.RateLimit.PrimaryWindow) || !validQuotaWindow(source.RateLimit.SecondaryWindow) || !validRateLimit(source.CodeReviewRateLimit) {
		return nil, &adapter.OperationError{Code: "codex_quota_contract_changed"}
	}
	var normalized normalizedUsage
	normalized.PlanType = source.PlanType
	normalized.RateLimit = source.RateLimit
	normalized.CodeReviewRateLimit = source.CodeReviewRateLimit
	normalized.Credits = source.Credits
	normalized.RateLimitResetCredits = source.RateLimitResetCredits
	normalized.FetchedAt = q.options.Now().UTC().Format(time.RFC3339)
	return json.Marshal(normalized)
}

func (q *quotaClient) listResetCredits(ctx context.Context, cred config.Credential) (json.RawMessage, error) {
	body, err := q.get(ctx, resetCreditsPath, cred)
	if err != nil {
		return nil, err
	}
	var source resetCreditsEnvelope
	if err := json.Unmarshal(body, &source); err != nil || source.AvailableCount < 0 || source.TotalEarnedCount < 0 {
		return nil, &adapter.OperationError{Code: "codex_quota_contract_changed"}
	}
	expirations := make([]string, 0, len(source.Credits))
	for _, credit := range source.Credits {
		if credit.ExpiresAt == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339, credit.ExpiresAt); err != nil {
			return nil, &adapter.OperationError{Code: "codex_quota_contract_changed"}
		}
		expirations = append(expirations, credit.ExpiresAt)
	}
	sort.Strings(expirations)
	summary := resetCreditsSummary{
		AvailableCount:                 source.AvailableCount,
		TotalEarnedCount:               source.TotalEarnedCount,
		ImmediateResetPurchaseEligible: source.ImmediateResetPurchaseEligible,
		FetchedAt:                      q.options.Now().UTC().Format(time.RFC3339),
	}
	if len(expirations) > 0 {
		summary.NextExpiresAt = expirations[0]
	}
	return json.Marshal(summary)
}

func (q *quotaClient) consumeResetCredit(ctx context.Context, cred config.Credential, input json.RawMessage) (json.RawMessage, error) {
	var request consumeResetCreditInput
	if json.Unmarshal(input, &request) != nil || !validUUID(request.RedeemRequestID) {
		return nil, &adapter.OperationError{Code: "codex_reset_request_invalid"}
	}
	body, _ := json.Marshal(request)
	u, err := url.JoinPath(strings.TrimRight(q.options.BackendBaseURL, "/"), resetCreditsConsumePath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("ChatGPT-Account-ID", cred.ChatGPTAccountID)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", q.options.ClientVersion)
	req.Header.Set("X-Codex-Client", q.options.ClientVersion)
	resp, err := q.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if _, err := readLimited(resp.Body, maxQuotaBody); err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, unauthorizedError{}
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return nil, &adapter.OperationError{Code: "codex_quota_unsupported"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &adapter.OperationError{Code: "codex_reset_credit_failed"}
	}
	return json.RawMessage(`{"consumed":true}`), nil
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	raw := strings.ReplaceAll(value, "-", "")
	decoded, err := hex.DecodeString(raw)
	return err == nil && len(decoded) == 16
}

func (q *quotaClient) get(ctx context.Context, path string, cred config.Credential) ([]byte, error) {
	u, err := url.JoinPath(strings.TrimRight(q.options.BackendBaseURL, "/"), path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("ChatGPT-Account-ID", cred.ChatGPTAccountID)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", q.options.ClientVersion)
	req.Header.Set("X-Codex-Client", q.options.ClientVersion)
	resp, err := q.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := readLimited(resp.Body, maxQuotaBody)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, unauthorizedError{}
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return nil, &adapter.OperationError{Code: "codex_quota_unsupported"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("codex quota status %d", resp.StatusCode)
	}
	return body, nil
}

func validRateLimit(limit *quotaRateLimit) bool {
	return limit == nil || (limit.PrimaryWindow != nil && validQuotaWindow(limit.PrimaryWindow) && validQuotaWindow(limit.SecondaryWindow))
}

func validQuotaWindow(window *quotaWindow) bool {
	if window == nil {
		return true
	}
	return window.UsedPercent >= 0 && window.LimitWindowSeconds >= 0 && window.ResetAfterSeconds >= 0 && window.ResetAt >= 0
}

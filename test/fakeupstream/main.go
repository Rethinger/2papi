package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

func main() {
	go serveCompatible(9001)
	go serveCompatible(9002)
	go serveCompatible(9003)
	serveHTTP(9010, newCodexHandler())
}

func serveCompatible(port int) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "fail429") {
			http.Error(w, "rate", 429)
			return
		}
		if strings.Contains(string(b), "fail500") {
			http.Error(w, "boom", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, s := range []string{"hello", " world"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", s)
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	})
	serveHTTP(port, mux)
}

func serveHTTP(port int, handler http.Handler) {
	addr := fmt.Sprintf(":%d", port)
	log.Println("fake upstream", addr, os.Getpid())
	log.Fatal(http.ListenAndServe(addr, handler))
}

type fakeCodex struct {
	mu           sync.Mutex
	counters     map[string]int
	redeemed     map[string]bool
	deviceNonces map[string]string
	key          *rsa.PrivateKey
}

func newCodexHandler() http.Handler {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	fake := &fakeCodex{counters: map[string]int{}, redeemed: map[string]bool{}, deviceNonces: map[string]string{}, key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc("/oauth/authorize", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "fake authorization") })
	mux.HandleFunc("/oauth/token", fake.token)
	mux.HandleFunc("/api/accounts/deviceauth/usercode", fake.deviceCode)
	mux.HandleFunc("/api/accounts/deviceauth/token", fake.token)
	mux.HandleFunc("/.well-known/jwks.json", fake.jwks)
	mux.HandleFunc("/backend-api/codex/models", fake.models)
	mux.HandleFunc("/backend-api/codex/responses", fake.responses)
	mux.HandleFunc("/backend-api/wham/usage", fake.usage)
	mux.HandleFunc("/backend-api/wham/rate-limit-reset-credits", fake.resetCredits)
	mux.HandleFunc("/backend-api/wham/rate-limit-reset-credits/consume", fake.consumeResetCredit)
	mux.HandleFunc("/__test/counters", fake.showCounters)
	return mux
}

func (f *fakeCodex) increment(name string) {
	f.mu.Lock()
	f.counters[name]++
	f.mu.Unlock()
}

func (f *fakeCodex) token(w http.ResponseWriter, r *http.Request) {
	f.increment("token_refresh")
	_ = r.ParseForm()
	f.mu.Lock()
	nonce := f.deviceNonces[r.Form.Get("device_code")]
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"access_token": "fake-access", "refresh_token": "fake-refresh", "id_token": f.idToken(nonce), "expires_in": 3600})
}

func (f *fakeCodex) deviceCode(w http.ResponseWriter, r *http.Request) {
	f.increment("device_code")
	var request struct {
		Nonce string `json:"nonce"`
	}
	_ = json.NewDecoder(r.Body).Decode(&request)
	f.mu.Lock()
	f.deviceNonces["fake-device"] = request.Nonce
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"device_code": "fake-device", "user_code": "FAKE-CODE", "verification_uri": "http://fake-upstream:9010/codex/device", "expires_in": 900, "interval": 0})
}

func (f *fakeCodex) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": "fake-kid", "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(f.key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
	}}})
}

func (f *fakeCodex) idToken(nonce string) string {
	header, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": "fake-kid", "typ": "JWT"})
	payload, _ := json.Marshal(map[string]any{
		"iss": "http://fake-upstream:9010", "aud": "app_EMoamEEZ73f0CkXaXp7hrann",
		"exp": time.Now().Add(time.Hour).Unix(), "sub": "fake-user", "nonce": nonce,
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "fake-account", "chatgpt_user_id": "fake-user", "email": "fake@example.test", "chatgpt_plan_type": "plus"},
	})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := f.key.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		panic(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func (f *fakeCodex) models(w http.ResponseWriter, _ *http.Request) {
	f.increment("models")
	writeJSON(w, http.StatusOK, map[string]any{"models": []map[string]any{{"slug": "gpt-5-codex", "visibility": "allow", "supported_in_api": true, "context_window": 200000, "capabilities": map[string]any{"tools": true, "reasoning": true}}}})
}

func (f *fakeCodex) responses(w http.ResponseWriter, r *http.Request) {
	f.increment("inference")
	var request struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&request)
	if request.Model == "" {
		request.Model = "gpt-5-codex"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	created := `{"type":"response.created","response":{"id":"resp_fake","model":` + quoted(request.Model) + `,"created_at":1786500000}}`
	delta := `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"fake codex reply"}`
	completed := `{"type":"response.completed","response":{"id":"resp_fake","model":` + quoted(request.Model) + `,"created_at":1786500000,"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fake codex reply"}]}],"usage":{"input_tokens":4,"output_tokens":3,"total_tokens":7}}}`
	for _, event := range []string{created, delta, completed} {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func (f *fakeCodex) usage(w http.ResponseWriter, _ *http.Request) {
	f.increment("quota")
	f.mu.Lock()
	consumed := len(f.redeemed) > 0
	f.mu.Unlock()
	used, resetAt := 100.0, int64(1787122967)
	if consumed {
		used, resetAt = 0, 1787722967
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plan_type":                "plus",
		"rate_limit":               map[string]any{"allowed": true, "limit_reached": !consumed, "primary_window": map[string]any{"used_percent": used, "limit_window_seconds": 604800, "reset_after_seconds": 3600, "reset_at": resetAt}, "secondary_window": nil},
		"code_review_rate_limit":   nil,
		"additional_rate_limits":   nil,
		"credits":                  map[string]any{"has_credits": false, "unlimited": false, "overage_limit_reached": false, "balance": "0", "approx_local_messages": []int{0, 0}, "approx_cloud_messages": []int{0, 0}},
		"rate_limit_reset_credits": map[string]any{"available_count": boolInt(!consumed), "applicable_available_count": boolInt(!consumed)},
	})
}

func (f *fakeCodex) resetCredits(w http.ResponseWriter, _ *http.Request) {
	f.increment("reset_list")
	f.mu.Lock()
	available := len(f.redeemed) == 0
	f.mu.Unlock()
	credits := []map[string]any{}
	if available {
		credits = append(credits, map[string]any{"id": "fake-credit-secret", "expires_at": "2030-08-20T00:00:00Z"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"credits": credits, "available_count": boolInt(available), "total_earned_count": 1, "immediate_reset_purchase_eligible": false})
}

func (f *fakeCodex) consumeResetCredit(w http.ResponseWriter, r *http.Request) {
	f.increment("reset_consume_calls")
	var request struct {
		RedeemRequestID string `json:"redeem_request_id"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil || request.RedeemRequestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "redeem_request_id_required"})
		return
	}
	f.mu.Lock()
	if !f.redeemed[request.RedeemRequestID] {
		if len(f.redeemed) > 0 {
			f.mu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]any{"error": "no_reset_credit"})
			return
		}
		f.redeemed[request.RedeemRequestID] = true
		f.counters["reset_consume"]++
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (f *fakeCodex) showCounters(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	copy := make(map[string]int, len(f.counters))
	for key, value := range f.counters {
		copy[key] = value
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, copy)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func quoted(value string) string {
	b, _ := json.Marshal(value)
	return string(b)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

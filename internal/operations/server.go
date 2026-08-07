package operations

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/1jehuang/2papi/internal/adapter"
	"github.com/1jehuang/2papi/internal/config"
)

const maxOperationBody = 2 << 20

type Server struct {
	registry func() *adapter.Registry
	token    string
	timeouts map[adapter.OperationKind]time.Duration
}

type OperationAccount struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Adapter        string            `json:"adapter"`
	BaseURL        string            `json:"base_url"`
	Credential     config.Credential `json:"credential"`
	Enabled        bool              `json:"enabled"`
	Priority       int               `json:"priority"`
	Weight         int               `json:"weight"`
	MaxConcurrency int               `json:"max_concurrency"`
	Cost           float64           `json:"cost"`
}

type Request struct {
	Operation      adapter.OperationKind `json:"operation"`
	Account        OperationAccount      `json:"account"`
	Input          json.RawMessage       `json:"input"`
	IdempotencyKey string                `json:"idempotency_key"`
}

type Response struct {
	Data               json.RawMessage `json:"data"`
	WarningCode        string          `json:"warning_code"`
	CredentialRevision int64           `json:"credential_revision"`
}

func NewServer(reg *adapter.Registry, token string) *Server {
	return NewDynamicServer(func() *adapter.Registry { return reg }, token)
}

func NewDynamicServer(registry func() *adapter.Registry, token string) *Server {
	return &Server{
		registry: registry,
		token:    token,
		timeouts: map[adapter.OperationKind]time.Duration{
			adapter.OperationDiscoverModels:      30 * time.Second,
			adapter.OperationValidateCredentials: 20 * time.Second,
			adapter.OperationReadUsage:           20 * time.Second,
			adapter.OperationListResetCredits:    20 * time.Second,
			adapter.OperationConsumeResetCredit:  30 * time.Second,
		},
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/provider-operations", s.handle)
	return mux
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if !validBearer(r, s.token) {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxOperationBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req Request
	if err := dec.Decode(&req); err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := ensureJSONEOF(dec); err != nil {
		writeDecodeError(w, err)
		return
	}
	timeout, ok := s.timeouts[req.Operation]
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown_operation")
		return
	}
	if err := validateOperationAccount(req.Account); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_account")
		return
	}
	reg := s.registry()
	if reg == nil {
		writeErr(w, http.StatusServiceUnavailable, "operation_unavailable")
		return
	}
	ad, ok := reg.Get(req.Account.Adapter)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown_adapter")
		return
	}
	acct := config.Account{ID: req.Account.ID, Name: req.Account.Name, Adapter: req.Account.Adapter, BaseURL: req.Account.BaseURL, Credential: req.Account.Credential, Enabled: req.Account.Enabled, Priority: req.Account.Priority, Weight: req.Account.Weight, MaxConcurrency: req.Account.MaxConcurrency, Cost: req.Account.Cost}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	res, err := ad.Operate(ctx, adapter.Operation{Kind: req.Operation, Account: acct, Input: req.Input, IdempotencyKey: req.IdempotencyKey})
	rev := acct.Credential.Revision
	req = Request{}
	if err != nil {
		var capErr *adapter.CapabilityError
		if errors.As(err, &capErr) {
			writeErr(w, http.StatusBadRequest, "unknown_operation")
			return
		}
		var opErr *adapter.OperationError
		if errors.As(err, &opErr) {
			writeErr(w, http.StatusConflict, opErr.Code)
			return
		}
		writeErr(w, http.StatusBadGateway, "operation_failed")
		return
	}
	data := res.Data
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	if res.CredentialRevision > 0 {
		rev = res.CredentialRevision
	}
	writeJSON(w, http.StatusOK, Response{Data: data, WarningCode: res.WarningCode, CredentialRevision: rev})
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large")
		return
	}
	writeErr(w, http.StatusBadRequest, "invalid_request")
}

func validateOperationAccount(account OperationAccount) error {
	if account.ID == "" || account.Adapter == "" || account.BaseURL == "" || account.Credential.Revision <= 0 {
		return errors.New("account identity, adapter, base URL, and credential revision are required")
	}
	switch account.Credential.Kind {
	case "api_key":
		if account.Credential.APIKey == "" {
			return errors.New("API key required")
		}
	case "oauth":
		if account.Credential.AccessToken == "" {
			return errors.New("OAuth access token required")
		}
	default:
		return errors.New("unsupported credential kind")
	}
	return nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return errors.New("multiple JSON values")
	} else if errors.Is(err, io.EOF) {
		return nil
	} else {
		return err
	}
}

func validBearer(r *http.Request, want string) bool {
	if want == "" {
		return false
	}
	got := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(got) < len(p) || got[:len(p)] != p {
		return false
	}
	gh := sha256.Sum256([]byte(got[len(p):]))
	wh := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gh[:], wh[:]) == 1
}
func writeErr(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": fmt.Sprintf("operation error: %s", code)}})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/1jehuang/2papi/internal/config"
)

type Endpoint string

const (
	EndpointChatCompletions Endpoint = "chat_completions"
	EndpointResponses       Endpoint = "responses"
)

type OperationKind string

const (
	OperationDiscoverModels      OperationKind = "discover_models"
	OperationValidateCredentials OperationKind = "validate_credentials"
	OperationReadUsage           OperationKind = "read_usage"
	OperationListResetCredits    OperationKind = "list_reset_credits"
	OperationConsumeResetCredit  OperationKind = "consume_reset_credit"
)

type Adapter interface {
	Execute(context.Context, Execution) (*Result, error)
	Operate(context.Context, Operation) (OperationResult, error)
}

type Execution struct {
	Endpoint    Endpoint
	Request     *http.Request
	Account     config.Account
	Model       config.Model
	PublicModel string
	Body        []byte
}

type Result struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
}

type Operation struct {
	Kind           OperationKind
	Account        config.Account
	Input          json.RawMessage
	IdempotencyKey string
}

type OperationResult struct {
	Data json.RawMessage
}

type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

func NewRegistry() *Registry {
	return &Registry{adapters: map[string]Adapter{}}
}

func (r *Registry) Register(name string, ad Adapter) error {
	if name == "" {
		return errors.New("adapter name required")
	}
	if ad == nil {
		return errors.New("adapter required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.adapters == nil {
		r.adapters = map[string]Adapter{}
	}
	if _, exists := r.adapters[name]; exists {
		return fmt.Errorf("adapter %s already registered", name)
	}
	r.adapters[name] = ad
	return nil
}

func (r *Registry) Get(name string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ad, ok := r.adapters[name]
	return ad, ok
}

type CapabilityError struct {
	Kind OperationKind
}

func (e *CapabilityError) Error() string {
	return fmt.Sprintf("operation %s unsupported by adapter", e.Kind)
}

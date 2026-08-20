package adapter_test

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/Rethinger/2papi/internal/adapter"
)

type fakeAdapter struct{}

func (fakeAdapter) Execute(context.Context, adapter.Execution) (*adapter.Result, error) {
	return &adapter.Result{Status: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(nil)}, nil
}
func (fakeAdapter) Operate(context.Context, adapter.Operation) (adapter.OperationResult, error) {
	return adapter.OperationResult{}, nil
}

func TestRegistryRejectsDuplicateAndReportsUnknown(t *testing.T) {
	reg := adapter.NewRegistry()
	if _, ok := reg.Get("missing"); ok {
		t.Fatal("unexpected missing adapter")
	}
	if err := reg.Register("openai-compatible", fakeAdapter{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register("openai-compatible", fakeAdapter{}); err == nil {
		t.Fatal("expected duplicate registration error")
	}
	if _, ok := reg.Get("openai-compatible"); !ok {
		t.Fatal("registered adapter not found")
	}
}

package plugin

import (
	"context"
	"net/http"
	"testing"
)

func TestRegistryRegisterAndHooks(t *testing.T) {
	r := NewRegistry()
	called := false
	p := &Plugin{
		Name: "test",
		BeforeRequest: func(ctx context.Context, req *http.Request) error {
			called = true
			req.Header.Set("X-Test", "1")
			return nil
		},
	}
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(p); err == nil {
		t.Fatal("expected duplicate error")
	}
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	if err := r.BeforeRequest(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !called || req.Header.Get("X-Test") != "1" {
		t.Fatal("hook not called")
	}
	if len(r.List()) != 1 {
		t.Fatal("list")
	}
	r.Unregister("test")
	if len(r.List()) != 0 {
		t.Fatal("unregister")
	}
}

func TestSidecarTimeoutIsNonFatal(t *testing.T) {
	r := NewRegistry()
	p := &Plugin{Name: "sidecar", Endpoint: "http://127.0.0.1:1"} // no server
	_ = r.Register(p)
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	// Should not error even if sidecar down (non-fatal)
	if err := r.BeforeRequest(context.Background(), req); err != nil {
		t.Fatalf("sidecar down should be non-fatal, got %v", err)
	}
}

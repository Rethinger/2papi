package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestOTelEnabledEnvLogic(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	if OTelEnabled() {
		t.Fatal("no endpoint must mean disabled")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
	if !OTelEnabled() {
		t.Fatal("endpoint set must enable OTel")
	}
	t.Setenv("OTEL_SDK_DISABLED", "true")
	if OTelEnabled() {
		t.Fatal("OTEL_SDK_DISABLED=true must disable OTel")
	}
}

func TestNewOTelRecorderDisabledReturnsNilWrapper(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	base := &recordingRecorder{}
	rec, shutdown, err := NewOTelRecorder(base, "test")
	if err != nil {
		t.Fatalf("disabled path must not error: %v", err)
	}
	if rec != nil {
		t.Fatal("disabled OTel must return a nil wrapper")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown must not error: %v", err)
	}
}

func TestOTelRecorderForwardsAndExportsSpans(t *testing.T) {
	var posts atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("exporter must POST, got %s", r.Method)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		posts.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")

	base := &recordingRecorder{}
	rec, shutdown, err := NewOTelRecorder(base, "test-service")
	if err != nil {
		t.Fatalf("otel init: %v", err)
	}
	if rec == nil {
		t.Fatal("enabled OTel must return a wrapper")
	}

	rec.Record(Event{
		Endpoint:      "/v1/chat/completions",
		PublicModel:   "gpt-dev",
		UpstreamModel: "upstream-raw",
		VirtualKey:    "vk-1",
		FinalStatus:   200,
		Success:       true,
		InputTokens:   120,
		OutputTokens:  30,
	})

	// Shutdown flushes the batch processor: at least one export must reach
	// the collector with the span payload.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("otel shutdown: %v", err)
	}

	if posts.Load() == 0 {
		t.Fatal("batcher must export the recorded span to the collector")
	}
	base.mu.Lock()
	defer base.mu.Unlock()
	if len(base.events) != 1 || base.events[0].PublicModel != "gpt-dev" {
		t.Fatalf("base recorder must still receive the event: %+v", base.events)
	}
}

// OTel GenAI emission (G3): when an OTLP endpoint is configured via env
// (OTEL_EXPORTER_OTLP_ENDPOINT / OTEL_EXPORTER_OTLP_TRACES_ENDPOINT), every
// gateway request is mirrored as a trace span with the minimal gen_ai.*
// attribute set (model, tokens, operation, status). OTEL_SDK_DISABLED=true
// turns the whole thing off; without an endpoint the wrapper is a no-op that
// forwards to the underlying recorder untouched (zero dependency on the OTel
// runtime in default deployments).
package telemetry

import (
	"context"
	"os"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otlptracehttp "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// OTelEnabled reports whether an OTLP exporter should be started: an
// endpoint env var is set and the SDK is not explicitly disabled.
func OTelEnabled() bool {
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return false
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

type otelRecorder struct {
	base   Recorder
	tracer trace.Tracer
}

// NewOTelRecorder wraps base with span emission. When OTel is not configured
// (or disabled) it returns (nil, noopShutdown, nil) so callers can keep one
// code path. base may be nil when the gateway otherwise has no recorder
// (lite mode) — spans still flow, plain telemetry does not.
func NewOTelRecorder(base Recorder, serviceName string) (Recorder, func(ctx context.Context) error, error) {
	if !OTelEnabled() {
		return nil, func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(context.Background())
	if err != nil {
		return nil, nil, err
	}
	res, err := resource.New(context.Background(),
		resource.WithAttributes(attribute.String("service.name", serviceName)))
	if err != nil {
		res = resource.Default()
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	shutdown := func(ctx context.Context) error {
		// Flush spans through the live exporter first, then release it.
		tpErr := tp.Shutdown(ctx)
		exErr := exporter.Shutdown(ctx)
		if tpErr != nil {
			return tpErr
		}
		return exErr
	}
	return &otelRecorder{base: base, tracer: tp.Tracer(serviceName)}, shutdown, nil
}

// Record forwards the event and emits one span per request with the minimal
// GenAI semantic-convention attributes (gen_ai.*) plus 2papi identifiers.
func (r *otelRecorder) Record(e Event) {
	if r.base != nil {
		r.base.Record(e)
	}
	cacheHit := len(e.Attempts) == 1 && e.Attempts[0].Account == "cache"
	_, span := r.tracer.Start(context.Background(), "gateway.request")
	defer span.End()
	span.SetAttributes(
		attribute.String("gen_ai.operation.name", e.Endpoint),
		attribute.String("gen_ai.request.model", e.PublicModel),
		attribute.String("gen_ai.response.model", e.UpstreamModel),
		attribute.Int64("gen_ai.usage.input_tokens", e.InputTokens),
		attribute.Int64("gen_ai.usage.output_tokens", e.OutputTokens),
		attribute.Int("http.response.status_code", e.FinalStatus),
		attribute.String("2papi.virtual_key", e.VirtualKey),
		attribute.String("2papi.gateway_id", e.GatewayID),
		attribute.Bool("2papi.cache", cacheHit),
	)
	if !e.Success {
		span.SetStatus(codes.Error, "request failed")
	}
}

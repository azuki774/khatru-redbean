package telemetry

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
)

const defaultEndpoint = "10.254.0.10:4318"

func endpoint() string {
	if value := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); value != "" {
		return value
	}

	return defaultEndpoint
}

type Provider struct {
	TracerProvider *trace.TracerProvider
}

func NewProvider(ctx context.Context, version string) (*Provider, error) {
	return newProvider(ctx, endpoint(), version)
}

func newProvider(ctx context.Context, endpoint, version string) (*Provider, error) {
	traceExporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	res := resource.NewWithAttributes("",
		attribute.String("service.name", "khatru-redbean"),
		attribute.String("service.version", version),
	)

	return &Provider{
		TracerProvider: trace.NewTracerProvider(
			trace.WithBatcher(traceExporter),
			trace.WithResource(res),
			trace.WithSampler(trace.AlwaysSample()),
		),
	}, nil
}

func (p *Provider) RegisterGlobal() {
	otel.SetTracerProvider(p.TracerProvider)
}

func (p *Provider) Shutdown(ctx context.Context) error {
	return p.TracerProvider.Shutdown(ctx)
}

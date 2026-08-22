package telemetry

import (
	"context"
	"errors"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
)

const defaultEndpoint = "10.254.0.10:4317"

func endpoint() string {
	if value := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); value != "" {
		return value
	}

	return defaultEndpoint
}

type Provider struct {
	TracerProvider *trace.TracerProvider
	MeterProvider  *metric.MeterProvider
}

func NewProvider(ctx context.Context, version string) (*Provider, error) {
	return newProvider(ctx, endpoint(), version)
}

func newProvider(ctx context.Context, endpoint, version string) (*Provider, error) {
	traceExporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	metricExporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	resource := resource.NewWithAttributes("",
		attribute.String("service.name", "khatru-redbean"),
		attribute.String("service.version", version),
	)

	return &Provider{
		TracerProvider: trace.NewTracerProvider(
			trace.WithBatcher(traceExporter),
			trace.WithResource(resource),
			trace.WithSampler(trace.AlwaysSample()),
		),
		MeterProvider: metric.NewMeterProvider(
			metric.WithReader(metric.NewPeriodicReader(metricExporter)),
			metric.WithResource(resource),
		),
	}, nil
}

func (p *Provider) RegisterGlobal() {
	otel.SetTracerProvider(p.TracerProvider)
	otel.SetMeterProvider(p.MeterProvider)
}

func (p *Provider) Shutdown(ctx context.Context) error {
	results := make(chan error, 2)
	go func() { results <- p.TracerProvider.Shutdown(ctx) }()
	go func() { results <- p.MeterProvider.Shutdown(ctx) }()

	return errors.Join(<-results, <-results)
}

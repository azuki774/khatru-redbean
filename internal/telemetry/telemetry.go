package telemetry

import (
	"context"
	"errors"
	"os"
	"sync"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
)

const defaultEndpoint = "10.254.0.10:4318"

var runtimeMetricsStarted sync.Once

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
	traceExporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	metricExporter, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(endpoint),
		otlpmetrichttp.WithInsecure(),
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
		MeterProvider: metric.NewMeterProvider(
			metric.WithReader(metric.NewPeriodicReader(metricExporter)),
			metric.WithResource(res),
		),
	}, nil
}

func (p *Provider) RegisterGlobal() {
	otel.SetTracerProvider(p.TracerProvider)
	otel.SetMeterProvider(p.MeterProvider)

	runtimeMetricsStarted.Do(func() {
		_ = runtime.Start(runtime.WithMeterProvider(p.MeterProvider))
	})
}

func (p *Provider) Shutdown(ctx context.Context) error {
	err := p.TracerProvider.Shutdown(ctx)
	if mErr := p.MeterProvider.Shutdown(ctx); mErr != nil {
		return errors.Join(err, mErr)
	}
	return err
}

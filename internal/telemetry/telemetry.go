package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

const (
	defaultEndpoint = "10.254.0.10:4318"
	serviceName     = "khatru-redbean"
)

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

// newResource builds the Resource shared by the trace and metric providers.
// OTEL_RESOURCE_ATTRIBUTES supplies deployment attributes such as
// deployment.environment.name and an optional service.instance.id. The relay
// identity (service.name and service.version) is always explicit so an
// environment file cannot rename the service.
func newResource(ctx context.Context, version string) (*resource.Resource, error) {
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithAttributes(
			attribute.String("service.name", serviceName),
			attribute.String("service.version", version),
		),
	)
	if err != nil {
		// invalid OTEL_RESOURCE_ATTRIBUTES entries are dropped by the SDK; a
		// partial-resource error must not stop the relay from starting.
		if res == nil || errors.Is(err, resource.ErrSchemaURLConflict) || !errors.Is(err, resource.ErrPartialResource) {
			return nil, err
		}
	}

	instanceKey := attribute.Key("service.instance.id")
	if value, ok := res.Set().Value(instanceKey); ok && value.AsString() != "" {
		return res, nil
	}

	instanceID, err := generateInstanceID()
	if err != nil {
		return nil, err
	}
	return resource.Merge(res, resource.NewSchemaless(attribute.String(string(instanceKey), instanceID)))
}

// generateInstanceID creates a random 128-bit identifier so separate provider
// instances (and therefore processes) are distinguishable by default.
func generateInstanceID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
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

	res, err := newResource(ctx, version)
	if err != nil {
		return nil, err
	}

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

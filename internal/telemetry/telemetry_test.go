package telemetry

import (
	"context"
	"net"
	"testing"
	"time"

	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

func TestEndpoint(t *testing.T) {
	t.Run("uses the default endpoint when unset", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

		if got := endpoint(); got != "10.254.0.10:4317" {
			t.Errorf("endpoint() = %q, want default endpoint", got)
		}
	})

	t.Run("uses the configured endpoint", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "alloy.observability.svc:4317")

		if got := endpoint(); got != "alloy.observability.svc:4317" {
			t.Errorf("endpoint() = %q, want configured endpoint", got)
		}
	})
}

func TestNewProvider(t *testing.T) {
	endpoint, exports := startCollector(t)

	provider, err := newProvider(context.Background(), endpoint, "test")
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	if provider.TracerProvider == nil {
		t.Error("NewProvider() TracerProvider is nil")
	}
	if provider.MeterProvider == nil {
		t.Error("NewProvider() MeterProvider is nil")
	}

	_, span := provider.TracerProvider.Tracer("test").Start(context.Background(), "test.span")
	span.End()
	counter, err := provider.MeterProvider.Meter("test").Int64Counter("test.counter")
	if err != nil {
		t.Fatalf("Int64Counter() error = %v", err)
	}
	counter.Add(context.Background(), 1)

	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	select {
	case <-exports.traces:
	case <-time.After(time.Second):
		t.Error("trace was not exported")
	}
	select {
	case <-exports.metrics:
	case <-time.After(time.Second):
		t.Error("metric was not exported")
	}
}

type telemetryExports struct {
	metrics chan struct{}
	traces  chan struct{}
}

type metricsCollector struct {
	colmetricpb.UnimplementedMetricsServiceServer
	exports *telemetryExports
}

func (c metricsCollector) Export(context.Context, *colmetricpb.ExportMetricsServiceRequest) (*colmetricpb.ExportMetricsServiceResponse, error) {
	select {
	case c.exports.metrics <- struct{}{}:
	default:
	}
	return &colmetricpb.ExportMetricsServiceResponse{}, nil
}

type tracesCollector struct {
	coltracepb.UnimplementedTraceServiceServer
	exports *telemetryExports
}

func (c tracesCollector) Export(context.Context, *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	select {
	case c.exports.traces <- struct{}{}:
	default:
	}
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

func startCollector(t *testing.T) (string, *telemetryExports) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}

	exports := &telemetryExports{
		metrics: make(chan struct{}, 1),
		traces:  make(chan struct{}, 1),
	}
	server := grpc.NewServer()
	colmetricpb.RegisterMetricsServiceServer(server, metricsCollector{exports: exports})
	coltracepb.RegisterTraceServiceServer(server, tracesCollector{exports: exports})
	go server.Serve(listener)
	t.Cleanup(func() {
		server.Stop()
		listener.Close()
	})

	return listener.Addr().String(), exports
}

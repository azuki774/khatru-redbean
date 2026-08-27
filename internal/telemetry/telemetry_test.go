package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEndpoint(t *testing.T) {
	t.Run("uses the default endpoint when unset", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

		if got := endpoint(); got != "10.254.0.10:4318" {
			t.Errorf("endpoint() = %q, want default endpoint", got)
		}
	})

	t.Run("uses the configured endpoint", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "alloy.observability.svc:4318")

		if got := endpoint(); got != "alloy.observability.svc:4318" {
			t.Errorf("endpoint() = %q, want configured endpoint", got)
		}
	})
}

func TestNewProvider(t *testing.T) {
	traceExported := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/traces" {
			select {
			case traceExported <- struct{}{}:
			default:
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	provider, err := newProvider(context.Background(), strings.TrimPrefix(server.URL, "http://"), "test")
	if err != nil {
		t.Fatalf("newProvider() error = %v", err)
	}
	if provider.TracerProvider == nil {
		t.Error("newProvider() TracerProvider is nil")
	}

	_, span := provider.TracerProvider.Tracer("test").Start(context.Background(), "test.span")
	span.End()

	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	select {
	case <-traceExported:
	case <-time.After(time.Second):
		t.Error("trace was not exported")
	}
}

func TestNewProviderExportsMetrics(t *testing.T) {
	metricExported := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/metrics" {
			select {
			case metricExported <- struct{}{}:
			default:
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	provider, err := newProvider(context.Background(), strings.TrimPrefix(server.URL, "http://"), "test")
	if err != nil {
		t.Fatalf("newProvider() error = %v", err)
	}
	if provider.MeterProvider == nil {
		t.Fatal("newProvider() MeterProvider is nil")
	}

	meter := provider.MeterProvider.Meter("test")
	counter, err := meter.Int64Counter("test.counter")
	if err != nil {
		t.Fatalf("Int64Counter() error = %v", err)
	}
	counter.Add(context.Background(), 1)

	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	select {
	case <-metricExported:
	case <-time.After(time.Second):
		t.Error("metric was not exported")
	}
}

package telemetry

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	collectormetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

func TestResourceExportedToTraceAndMetricSignals(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES",
		"deployment.environment.name=staging,service.instance.id=instance-123,service.name=renamed,service.version=wrong")
	t.Setenv("OTEL_SERVICE_NAME", "")

	server, capture := newOTLPCapture(t)
	provider := mustProvider(t, server)

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

	traceResource := traceResource(t, capture)
	metricResource := metricResource(t, capture)

	for name, attrs := range map[string]map[string]string{
		"trace":  traceResource,
		"metric": metricResource,
	} {
		if got := attrs["service.name"]; got != serviceName {
			t.Errorf("%s resource service.name = %q, want %q", name, got, serviceName)
		}
		if got := attrs["service.version"]; got != "test-only" {
			t.Errorf("%s resource service.version = %q, want test-only", name, got)
		}
		if got := attrs["service.instance.id"]; got != "instance-123" {
			t.Errorf("%s resource service.instance.id = %q, want instance-123", name, got)
		}
		if got := attrs["deployment.environment.name"]; got != "staging" {
			t.Errorf("%s resource deployment.environment.name = %q, want staging", name, got)
		}
	}
}

func TestGeneratedInstanceIDIsSharedAndDistinctPerProvider(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
	t.Setenv("OTEL_SERVICE_NAME", "")

	instanceIDs := make([]string, 0, 2)
	for range 2 {
		server, capture := newOTLPCapture(t)
		provider := mustProvider(t, server)

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

		traceID := traceResource(t, capture)["service.instance.id"]
		metricID := metricResource(t, capture)["service.instance.id"]
		if traceID == "" {
			t.Fatal("trace resource has no generated service.instance.id")
		}
		if metricID != traceID {
			t.Errorf("metric instance id = %q, trace instance id = %q; want the same resource", metricID, traceID)
		}
		instanceIDs = append(instanceIDs, traceID)
	}

	if instanceIDs[0] == instanceIDs[1] {
		t.Errorf("two providers share generated service.instance.id %q", instanceIDs[0])
	}
}

func mustProvider(t *testing.T, server *httptest.Server) *Provider {
	t.Helper()

	provider, err := newProvider(context.Background(), strings.TrimPrefix(server.URL, "http://"), "test-only")
	if err != nil {
		t.Fatalf("newProvider() error = %v", err)
	}
	return provider
}

type otlpCapture struct {
	traces  chan []byte
	metrics chan []byte
}

func newOTLPCapture(t *testing.T) (*httptest.Server, *otlpCapture) {
	t.Helper()

	capture := &otlpCapture{
		traces:  make(chan []byte, 1),
		metrics: make(chan []byte, 1),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var body io.Reader = r.Body
		if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
			reader, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Errorf("gzip.NewReader() error = %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer reader.Close()
			body = reader
		}
		payload, err := io.ReadAll(body)
		if err != nil {
			t.Errorf("ReadAll() error = %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		switch r.URL.Path {
		case "/v1/traces":
			select {
			case capture.traces <- payload:
			default:
			}
		case "/v1/metrics":
			select {
			case capture.metrics <- payload:
			default:
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server, capture
}

func traceResource(t *testing.T, capture *otlpCapture) map[string]string {
	t.Helper()

	payload := waitPayload(t, capture.traces, "trace")
	var request collectortrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(payload, &request); err != nil {
		t.Fatalf("Unmarshal(traces) error = %v", err)
	}
	if len(request.ResourceSpans) == 0 {
		t.Fatal("trace request has no resource spans")
	}
	return resourceAttributes(t, request.ResourceSpans[0].Resource)
}

func metricResource(t *testing.T, capture *otlpCapture) map[string]string {
	t.Helper()

	payload := waitPayload(t, capture.metrics, "metric")
	var request collectormetric.ExportMetricsServiceRequest
	if err := proto.Unmarshal(payload, &request); err != nil {
		t.Fatalf("Unmarshal(metrics) error = %v", err)
	}
	if len(request.ResourceMetrics) == 0 {
		t.Fatal("metric request has no resource metrics")
	}
	return resourceAttributes(t, request.ResourceMetrics[0].Resource)
}

func waitPayload(t *testing.T, payloads <-chan []byte, signal string) []byte {
	t.Helper()

	select {
	case payload := <-payloads:
		return payload
	case <-time.After(2 * time.Second):
		t.Fatalf("%s payload was not exported", signal)
		return nil
	}
}

func resourceAttributes(t *testing.T, res *resourcepb.Resource) map[string]string {
	t.Helper()

	attributes := make(map[string]string, len(res.GetAttributes()))
	for _, attr := range res.GetAttributes() {
		attributes[attr.GetKey()] = attr.GetValue().GetStringValue()
	}
	return attributes
}

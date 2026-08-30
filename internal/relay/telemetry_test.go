package relay

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nbd-wtf/go-nostr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRelayTelemetryEventPolicyPreservesOutcome(t *testing.T) {
	telemetry, err := newRelayTelemetry()
	if err != nil {
		t.Fatalf("newRelayTelemetry() error = %v", err)
	}

	policy := telemetry.eventPolicy("test", func(context.Context, *nostr.Event) (bool, string) {
		return true, "blocked: test"
	})

	reject, message := policy(context.Background(), &nostr.Event{Kind: 1})
	if !reject {
		t.Error("eventPolicy() reject = false, want true")
	}
	if message != "blocked: test" {
		t.Errorf("eventPolicy() message = %q, want %q", message, "blocked: test")
	}
}

func TestEventAttributesUseLowCardinalityValues(t *testing.T) {
	attributes := eventAttributes(&nostr.Event{Kind: 1})
	if attributes[0].Key != "nostr.event.class" {
		t.Errorf("event attribute key = %q, want nostr.event.class", attributes[0].Key)
	}
	if attributes[0].Value.AsString() != "regular" {
		t.Errorf("event class = %q, want regular", attributes[0].Value.AsString())
	}
	if cardinalityBucket(250) != "101+" {
		t.Errorf("cardinalityBucket(250) = %q, want 101+", cardinalityBucket(250))
	}
}

func TestQueryOperationEndsSpanAfterChannelIsConsumed(t *testing.T) {
	telemetry, recorder := testRelayTelemetry(t)
	source := make(chan *nostr.Event)
	query := telemetry.queryOperation(func(context.Context, nostr.Filter) (chan *nostr.Event, error) {
		return source, nil
	})

	events, err := query(context.Background(), nostr.Filter{})
	if err != nil {
		t.Fatalf("queryOperation() error = %v", err)
	}
	go func() {
		source <- &nostr.Event{Kind: 1}
		close(source)
	}()
	<-events

	deadline := time.After(time.Second)
	for len(recorder.Ended()) == 0 {
		select {
		case <-deadline:
			t.Fatal("query span was not ended")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestDatabaseOperationDoesNotRecordErrorMessage(t *testing.T) {
	telemetry, recorder := testRelayTelemetry(t)
	operation := telemetry.eventOperation("save_event", func(context.Context, *nostr.Event) error {
		return errors.New("private query value")
	})

	if err := operation(context.Background(), &nostr.Event{Kind: 1}); err == nil {
		t.Fatal("eventOperation() error = nil, want error")
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if spans[0].Status().Description != "database operation failed" {
		t.Errorf("span status = %q, want generic database failure", spans[0].Status().Description)
	}
	for _, attribute := range spans[0].Attributes() {
		if attribute.Key == "exception.message" {
			t.Error("span contains exception.message")
		}
	}
}

func testRelayTelemetry(t *testing.T) (*relayTelemetry, *tracetest.SpanRecorder) {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	provider := trace.NewTracerProvider(trace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		provider.Shutdown(context.Background())
	})

	telemetry, err := newRelayTelemetry()
	if err != nil {
		t.Fatalf("newRelayTelemetry() error = %v", err)
	}
	return telemetry, recorder
}

func TestHTTPMiddlewareRecordsSpan(t *testing.T) {
	telemetry, recorder := testRelayTelemetry(t)

	handler := telemetry.httpMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if spans[0].Name() != "http.request" {
		t.Errorf("span name = %q, want http.request", spans[0].Name())
	}
	var gotStatus int64
	for _, a := range spans[0].Attributes() {
		if a.Key == "http.response.status_code" {
			gotStatus = a.Value.AsInt64()
		}
	}
	if gotStatus != http.StatusTeapot {
		t.Errorf("status code attribute = %d, want 418", gotStatus)
	}
}

func TestHTTPMiddlewareSupportsWebSocketUpgrade(t *testing.T) {
	telemetry, _ := testRelayTelemetry(t)

	handler := telemetry.httpMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("websocket.Accept() error = %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
	}))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket.Dial() error = %v", err)
	}
	conn.Close(websocket.StatusNormalClosure, "")
}

func TestConnectionMetrics(t *testing.T) {
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(previous)
		provider.Shutdown(context.Background())
	})

	telemetry, err := newRelayTelemetry()
	if err != nil {
		t.Fatalf("newRelayTelemetry() error = %v", err)
	}

	ctx := context.Background()
	telemetry.onConnect(ctx)
	telemetry.onDisconnect(ctx)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}

	connections, ok := sumInt64(rm, "nostr.relay.connections")
	if !ok {
		t.Fatal("nostr.relay.connections not exported")
	}
	if connections != 0 {
		t.Errorf("nostr.relay.connections = %d, want 0 (connect/disconnect cancel out)", connections)
	}

	count, ok := sumInt64(rm, "nostr.relay.connection.count")
	if !ok {
		t.Fatal("nostr.relay.connection.count not exported")
	}
	if count != 2 {
		t.Errorf("nostr.relay.connection.count = %d, want 2 (open + close)", count)
	}

	if _, ok := histogramCount(rm, "nostr.relay.connection.duration"); !ok {
		t.Error("nostr.relay.connection.duration not exported")
	}
}

func sumInt64(rm metricdata.ResourceMetrics, name string) (int64, bool) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			if data, ok := m.Data.(metricdata.Sum[int64]); ok {
				var total int64
				for _, dp := range data.DataPoints {
					total += dp.Value
				}
				return total, true
			}
		}
	}
	return 0, false
}

func histogramCount(rm metricdata.ResourceMetrics, name string) (uint64, bool) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			if data, ok := m.Data.(metricdata.Histogram[int64]); ok {
				var total uint64
				for _, dp := range data.DataPoints {
					total += dp.Count
				}
				return total, true
			}
			if data, ok := m.Data.(metricdata.Histogram[float64]); ok {
				var total uint64
				for _, dp := range data.DataPoints {
					total += dp.Count
				}
				return total, true
			}
		}
	}
	return 0, false
}

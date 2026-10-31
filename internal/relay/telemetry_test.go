package relay

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/azuki774/khatru-redbean/internal/nostr"
	"github.com/azuki774/khatru-redbean/internal/nostr/nip77"
	"github.com/azuki774/khatru-redbean/internal/relay/engine"
	"github.com/coder/websocket"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
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

func TestMessageSpanName(t *testing.T) {
	cases := []struct {
		envelope any
		want     string
	}{
		{(*nostr.EventEnvelope)(nil), "nostr.event"},
		{(*nostr.ReqEnvelope)(nil), "nostr.req"},
		{(*nostr.CountEnvelope)(nil), "nostr.count"},
		{(*nostr.CloseEnvelope)(nil), "nostr.close"},
		{(*nostr.AuthEnvelope)(nil), "nostr.auth"},
		{(*nip77.OpenEnvelope)(nil), "nostr.negentropy"},
		{(*nip77.MessageEnvelope)(nil), "nostr.negentropy"},
		{(*nip77.CloseEnvelope)(nil), "nostr.negentropy"},
		{nil, "ws.parse"},
		{(*nostr.NoticeEnvelope)(nil), "ws.message"},
	}

	for _, tc := range cases {
		if got := messageSpanName(tc.envelope); got != tc.want {
			t.Errorf("messageSpanName(%T) = %q, want %q", tc.envelope, got, tc.want)
		}
	}
}

func TestMessageAttributes(t *testing.T) {
	eventAttrs := messageAttributes(&nostr.EventEnvelope{Event: nostr.Event{Kind: 1}})
	if value, ok := findAttribute(eventAttrs, "nostr.event.class"); !ok || value.AsString() != "regular" {
		t.Errorf("nostr.event.class = %v (present=%v), want regular", value.AsString(), ok)
	}

	reqAttributes := messageAttributes(&nostr.ReqEnvelope{Filters: []nostr.Filter{{Kinds: []int{1}}}})
	if value, ok := findAttribute(reqAttributes, "nostr.filter.kinds_count_bucket"); !ok || value.AsString() != "1" {
		t.Errorf("nostr.filter.kinds_count_bucket = %v (present=%v), want 1", value.AsString(), ok)
	}

	countAttributes := messageAttributes(&nostr.CountEnvelope{Filter: nostr.Filter{Kinds: []int{1, 2}}})
	if value, ok := findAttribute(countAttributes, "nostr.filter.kinds_count_bucket"); !ok || value.AsString() != "2-5" {
		t.Errorf("nostr.filter.kinds_count_bucket = %v (present=%v), want 2-5", value.AsString(), ok)
	}

	if attributes := messageAttributes(&nip77.OpenEnvelope{}); len(attributes) != 0 {
		t.Errorf("negentropy message attributes = %v, want none", attributes)
	}
	if attributes := messageAttributes(nil); len(attributes) != 0 {
		t.Errorf("nil envelope message attributes = %v, want none", attributes)
	}
}

func findAttribute(attributes []attribute.KeyValue, key string) (attribute.Value, bool) {
	for _, attr := range attributes {
		if string(attr.Key) == key {
			return attr.Value, true
		}
	}
	return attribute.Value{}, false
}

func TestQueryOperationEndsSpanWhenSourceCloses(t *testing.T) {
	telemetry, recorder := testRelayTelemetry(t)
	source := make(chan *nostr.Event, 2)
	source <- &nostr.Event{Kind: 1}
	source <- &nostr.Event{Kind: 2}
	close(source)
	query := telemetry.queryOperation(func(context.Context, nostr.Filter) (chan *nostr.Event, error) {
		return source, nil
	})

	events, err := query(context.Background(), nostr.Filter{})
	if err != nil {
		t.Fatalf("queryOperation() error = %v", err)
	}

	// the span ends when the database scan is done, even before the consumer
	// has read any of the buffered events
	waitForEndedSpan(t, recorder, "postgres.query_events")

	// and delivery still completes afterwards
	if event := <-events; event == nil || event.Kind != 1 {
		t.Fatalf("first event = %v, want kind 1", event)
	}
	if event := <-events; event == nil || event.Kind != 2 {
		t.Fatalf("second event = %v, want kind 2", event)
	}
	if _, ok := <-events; ok {
		t.Error("wrapped channel is still open after the source closed")
	}
}

func TestQueryOperationEndsSpanWhenConsumerAbandons(t *testing.T) {
	telemetry, recorder := testRelayTelemetry(t)
	source := make(chan *nostr.Event, 1)
	source <- &nostr.Event{Kind: 1}
	close(source)

	query := telemetry.queryOperation(func(context.Context, nostr.Filter) (chan *nostr.Event, error) {
		return source, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := query(ctx, nostr.Filter{}); err != nil {
		t.Fatalf("queryOperation() error = %v", err)
	}

	// nobody consumes the wrapped channel: the span must still end on source close
	waitForEndedSpan(t, recorder, "postgres.query_events")

	cancel()
}

func TestQueryOperationEndsSpanWhenConsumerStopsBeforeSourceClose(t *testing.T) {
	telemetry, recorder := testRelayTelemetry(t)
	source := make(chan *nostr.Event)

	query := telemetry.queryOperation(func(context.Context, nostr.Filter) (chan *nostr.Event, error) {
		return source, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := query(ctx, nostr.Filter{})
	if err != nil {
		t.Fatalf("queryOperation() error = %v", err)
	}

	// the producer streams asynchronously and then closes the source, while the
	// consumer stops after a single event: the span must still end on source close.
	// the delay makes sure the wrapper is already waiting for a consumer that will
	// never read again when the close arrives.
	go func() {
		source <- &nostr.Event{Kind: 1}
		source <- &nostr.Event{Kind: 2}
		time.Sleep(100 * time.Millisecond)
		close(source)
	}()

	if event := <-events; event == nil || event.Kind != 1 {
		t.Fatalf("first event = %v, want kind 1", event)
	}

	// the source is still open: the span must still be running
	time.Sleep(30 * time.Millisecond)
	if spans := recorder.Ended(); len(spans) != 0 {
		t.Fatalf("span ended before the source closed: %v", spans[0].Name())
	}

	waitForEndedSpan(t, recorder, "postgres.query_events")

	// releasing the wrapper is enough: the span was already ended by source close
	cancel()
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-time.After(time.Second):
			t.Fatal("wrapped channel did not close after cancellation")
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

func TestFinishDatabaseIgnoresClientCancellation(t *testing.T) {
	cases := []struct {
		name   string
		cancel bool
		err    error
	}{
		{name: "canceled context", cancel: true, err: context.Canceled},
		{name: "canceled error", cancel: false, err: context.Canceled},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			telemetry, recorder := testRelayTelemetry(t)

			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancel {
				cancel()
			}
			defer cancel()

			_, span, started := telemetry.startDatabase(ctx, "query_events", nil)
			telemetry.finishDatabase(ctx, span, started, "query_events", tc.err)

			spans := recorder.Ended()
			if len(spans) != 1 {
				t.Fatalf("ended spans = %d, want 1", len(spans))
			}
			if _, ok := attributeValue(spans[0], "error.type"); ok {
				t.Error("canceled database operation recorded error.type")
			}
			if spans[0].Status().Code == codes.Error {
				t.Error("canceled database operation recorded error status")
			}
		})
	}
}

func TestMessageObserverCreatesRootSpanLinkedToHandshake(t *testing.T) {
	telemetry, recorder := testRelayTelemetry(t)

	relay := engine.NewRelay()
	relay.OnMessage = telemetry.messageObserver

	server := httptest.NewServer(telemetry.httpMiddleware(relay))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket.Dial() error = %v", err)
	}
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "") })

	if err := conn.Write(ctx, websocket.MessageText, []byte(`["COUNT","sub",{"kinds":[1]}]`)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, message, err := conn.Read(ctx); err != nil {
		t.Fatalf("Read() error = %v", err)
	} else if !strings.HasPrefix(string(message), `["CLOSED"`) {
		t.Fatalf("response = %s, want CLOSED", message)
	}

	countSpan := waitForEndedSpan(t, recorder, "nostr.count")
	if countSpan.Parent().IsValid() {
		t.Error("message span has a parent, want a root span")
	}
	if countSpan.SpanKind() != oteltrace.SpanKindServer {
		t.Errorf("message span kind = %v, want server", countSpan.SpanKind())
	}
	if value, ok := attributeValue(countSpan, "nostr.message.type"); !ok || value.AsString() != "count" {
		t.Errorf("nostr.message.type = %v (present=%v), want count", value.AsString(), ok)
	}
	if value, ok := attributeValue(countSpan, "nostr.outcome"); !ok || value.AsString() != "unsupported" {
		t.Errorf("nostr.outcome = %v (present=%v), want unsupported", value.AsString(), ok)
	}
	if value, ok := attributeValue(countSpan, "nostr.filter.kinds_count_bucket"); !ok || value.AsString() != "1" {
		t.Errorf("nostr.filter.kinds_count_bucket = %v (present=%v), want 1", value.AsString(), ok)
	}
	if value, ok := attributeValue(countSpan, "nostr.connection.id"); !ok || len(value.AsString()) != 16 {
		t.Errorf("nostr.connection.id = %q (present=%v), want an 8-byte hex challenge", value.AsString(), ok)
	}
	if countSpan.Status().Code != codes.Error {
		t.Errorf("message span status = %v, want error", countSpan.Status().Code)
	}

	handshakeSpan := waitForEndedSpan(t, recorder, "http.request")
	if value, ok := attributeValue(handshakeSpan, "nostr.websocket"); !ok || !value.AsBool() {
		t.Error("handshake span is missing nostr.websocket=true")
	}
	if _, ok := attributeValue(handshakeSpan, "http.response.status_code"); ok {
		t.Error("hijacked websocket handshake recorded http.response.status_code")
	}

	links := countSpan.Links()
	if len(links) != 1 {
		t.Fatalf("message span links = %d, want 1", len(links))
	}
	linked := links[0].SpanContext
	handshake := handshakeSpan.SpanContext()
	if linked.TraceID() != handshake.TraceID() || linked.SpanID() != handshake.SpanID() {
		t.Error("message span is not linked to the handshake span")
	}

	if err := conn.Write(ctx, websocket.MessageText, []byte(`["CLOSE","sub"]`)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	closeSpan := waitForEndedSpan(t, recorder, "nostr.close")
	if _, ok := attributeValue(closeSpan, "nostr.outcome"); ok {
		t.Error("successful message span recorded nostr.outcome")
	}
	if closeSpan.Status().Code != codes.Unset {
		t.Errorf("successful message span status = %v, want unset", closeSpan.Status().Code)
	}
	if value, ok := attributeValue(closeSpan, "nostr.message.type"); !ok || value.AsString() != "close" {
		t.Errorf("nostr.message.type = %v (present=%v), want close", value.AsString(), ok)
	}
}

func waitForEndedSpan(t *testing.T, recorder *tracetest.SpanRecorder, name string) trace.ReadOnlySpan {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, span := range recorder.Ended() {
			if span.Name() == name {
				return span
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("span %q was not ended", name)
	return nil
}

func attributeValue(span trace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return attr.Value, true
		}
	}
	return attribute.Value{}, false
}

package relay

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azuki774/khatru-redbean/internal/nostr"
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

func TestMessageAndPersistenceLatencyShareReceiveTimestamp(t *testing.T) {
	telemetry, recorder, reader := newTestTelemetry(t)

	receivedAt := time.Now().Add(-2 * time.Second)
	processingCtx, end := telemetry.messageObserver(context.Background(), &nostr.EventEnvelope{
		Event: nostr.Event{Kind: 1},
	}, receivedAt)

	persistenceFinishedAt := receivedAt.Add(120 * time.Millisecond)
	rootCtx := telemetry.persistenceObserver(processingCtx, engine.PersistenceResult{
		Outcome:    "stored",
		FinishedAt: persistenceFinishedAt,
	})
	rootSpanContext := oteltrace.SpanFromContext(rootCtx).SpanContext()
	if !rootSpanContext.IsValid() {
		t.Fatal("persistence callback did not return the message root context")
	}
	persistenceSpanContext := oteltrace.SpanFromContext(processingCtx).SpanContext()
	if persistenceSpanContext.SpanID() == rootSpanContext.SpanID() {
		t.Fatal("persistence callback received the message root instead of the persistence span context")
	}

	// duplicate lifecycle callbacks must not record extra metrics or end spans twice
	telemetry.persistenceObserver(processingCtx, engine.PersistenceResult{
		Outcome:    "duplicate",
		FinishedAt: persistenceFinishedAt.Add(time.Second),
	})

	finishedAt := receivedAt.Add(150 * time.Millisecond)
	end(engine.MessageResult{Outcome: "ok", FinishedAt: finishedAt})
	end(engine.MessageResult{Outcome: "error", FinishedAt: finishedAt.Add(time.Second)})

	messageSpan := waitForEndedSpan(t, recorder, "nostr.event")
	persistenceSpan := waitForEndedSpan(t, recorder, "nostr.event.persist")
	if len(recorder.Ended()) != 2 {
		t.Fatalf("ended spans = %d, want exactly message and persistence spans", len(recorder.Ended()))
	}

	if messageSpan.SpanContext().SpanID() != rootSpanContext.SpanID() {
		t.Error("persistence callback returned a context unrelated to the message root span")
	}
	if !messageSpan.StartTime().Equal(receivedAt) {
		t.Errorf("message span start = %v, want %v", messageSpan.StartTime(), receivedAt)
	}
	if !messageSpan.EndTime().Equal(finishedAt) {
		t.Errorf("message span end = %v, want %v", messageSpan.EndTime(), finishedAt)
	}
	if !persistenceSpan.StartTime().Equal(receivedAt) {
		t.Errorf("persistence span start = %v, want %v", persistenceSpan.StartTime(), receivedAt)
	}
	if !persistenceSpan.EndTime().Equal(persistenceFinishedAt) {
		t.Errorf("persistence span end = %v, want %v", persistenceSpan.EndTime(), persistenceFinishedAt)
	}
	if persistenceSpan.Parent().SpanID() != messageSpan.SpanContext().SpanID() {
		t.Error("persistence span is not a child of the message span")
	}
	if value, ok := attributeValue(messageSpan, "nostr.outcome"); !ok || value.AsString() != "ok" {
		t.Errorf("nostr.outcome = %v (present=%v), want explicit ok", value.AsString(), ok)
	}

	rm := collectMetrics(t, reader)
	messagePoint := singleHistogramDataPoint(t, rm, "nostr.relay.message.duration")
	if messagePoint.Count != 1 {
		t.Errorf("message histogram count = %d, want 1", messagePoint.Count)
	}
	if !approximately(messagePoint.Sum, finishedAt.Sub(receivedAt).Seconds()) {
		t.Errorf("message histogram sum = %v, want %v", messagePoint.Sum, finishedAt.Sub(receivedAt).Seconds())
	}
	assertMetricAttribute(t, messagePoint.Attributes, "nostr.message.type", "event")
	assertMetricAttribute(t, messagePoint.Attributes, "nostr.outcome", "ok")
	assertMetricAttribute(t, messagePoint.Attributes, "nostr.event.class", "regular")
	assertMetricAttribute(t, messagePoint.Attributes, "nostr.persistence.outcome", "stored")

	persistencePoint := singleHistogramDataPoint(t, rm, "nostr.relay.event.persist.duration")
	if persistencePoint.Count != 1 {
		t.Errorf("persistence histogram count = %d, want 1", persistencePoint.Count)
	}
	if !approximately(persistencePoint.Sum, persistenceFinishedAt.Sub(receivedAt).Seconds()) {
		t.Errorf("persistence histogram sum = %v, want %v", persistencePoint.Sum, persistenceFinishedAt.Sub(receivedAt).Seconds())
	}
	assertMetricAttribute(t, persistencePoint.Attributes, "nostr.event.class", "regular")
	assertMetricAttribute(t, persistencePoint.Attributes, "nostr.persistence.outcome", "stored")

	if len(messagePoint.Exemplars) > 0 {
		exemplar := messagePoint.Exemplars[0]
		messageSpanContext := messageSpan.SpanContext()
		messageTraceID := messageSpanContext.TraceID()
		messageSpanID := messageSpanContext.SpanID()
		if !bytes.Equal(exemplar.TraceID, messageTraceID[:]) {
			t.Error("message histogram exemplar does not use the message trace")
		}
		if !bytes.Equal(exemplar.SpanID, messageSpanID[:]) {
			t.Error("message histogram exemplar does not use the message span")
		}
	}
	if len(persistencePoint.Exemplars) > 0 {
		exemplar := persistencePoint.Exemplars[0]
		messageSpanContext := messageSpan.SpanContext()
		persistenceSpanContext := persistenceSpan.SpanContext()
		messageTraceID := messageSpanContext.TraceID()
		persistenceSpanID := persistenceSpanContext.SpanID()
		if !bytes.Equal(exemplar.TraceID, messageTraceID[:]) {
			t.Error("persistence histogram exemplar does not use the message trace")
		}
		if !bytes.Equal(exemplar.SpanID, persistenceSpanID[:]) {
			t.Error("persistence histogram exemplar does not use the persistence span")
		}
	}
}

func TestStoredPersistenceSurvivesResponseWriteFailure(t *testing.T) {
	telemetry, recorder, reader := newTestTelemetry(t)

	receivedAt := time.Now().Add(-time.Second)
	processingCtx, end := telemetry.messageObserver(context.Background(), &nostr.EventEnvelope{
		Event: nostr.Event{Kind: 1},
	}, receivedAt)
	telemetry.persistenceObserver(processingCtx, engine.PersistenceResult{
		Outcome:    "stored",
		FinishedAt: receivedAt.Add(30 * time.Millisecond),
	})
	end(engine.MessageResult{Outcome: "write_error", FinishedAt: receivedAt.Add(40 * time.Millisecond)})

	rm := collectMetrics(t, reader)
	if count, ok := histogramCount(rm, "nostr.relay.event.persist.duration"); !ok || count != 1 {
		t.Errorf("persistence histogram count = %d (present=%v), want 1 for a stored event", count, ok)
	}
	messagePoint := singleHistogramDataPoint(t, rm, "nostr.relay.message.duration")
	assertMetricAttribute(t, messagePoint.Attributes, "nostr.outcome", "write_error")
	assertMetricAttribute(t, messagePoint.Attributes, "nostr.persistence.outcome", "stored")

	messageSpan := waitForEndedSpan(t, recorder, "nostr.event")
	if value, ok := attributeValue(messageSpan, "nostr.outcome"); !ok || value.AsString() != "write_error" {
		t.Errorf("message span outcome = %v (present=%v), want write_error", value.AsString(), ok)
	}
	if messageSpan.Status().Code != codes.Error {
		t.Error("message with a failed response write did not record an error status")
	}
}

func TestPersistenceHistogramOnlyRecordsStoredEvents(t *testing.T) {
	tests := []struct {
		name                 string
		persistenceOutcome   string
		wantMessageOutcome   string
		wantPersistenceError bool
	}{
		{name: "duplicate", persistenceOutcome: "duplicate", wantMessageOutcome: "ok"},
		{name: "superseded", persistenceOutcome: "superseded", wantMessageOutcome: "ok"},
		{name: "not stored", persistenceOutcome: "not_stored", wantMessageOutcome: "ok"},
		{name: "not attempted", persistenceOutcome: "not_attempted", wantMessageOutcome: "invalid"},
		{name: "ephemeral", persistenceOutcome: "ephemeral", wantMessageOutcome: "ok"},
		{name: "error", persistenceOutcome: "error", wantMessageOutcome: "error", wantPersistenceError: true},
		{name: "canceled", persistenceOutcome: "canceled", wantMessageOutcome: "canceled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			telemetry, recorder, reader := newTestTelemetry(t)

			receivedAt := time.Now().Add(-time.Second)
			processingCtx, end := telemetry.messageObserver(context.Background(), &nostr.EventEnvelope{
				Event: nostr.Event{Kind: 1},
			}, receivedAt)
			telemetry.persistenceObserver(processingCtx, engine.PersistenceResult{
				Outcome:    tt.persistenceOutcome,
				FinishedAt: receivedAt.Add(75 * time.Millisecond),
			})
			end(engine.MessageResult{Outcome: tt.wantMessageOutcome, FinishedAt: receivedAt.Add(100 * time.Millisecond)})

			persistenceSpan := waitForEndedSpan(t, recorder, "nostr.event.persist")
			if value, ok := attributeValue(persistenceSpan, "nostr.persistence.outcome"); !ok || value.AsString() != tt.persistenceOutcome {
				t.Errorf("persistence span outcome = %v (present=%v), want %q", value.AsString(), ok, tt.persistenceOutcome)
			}
			if tt.wantPersistenceError {
				if persistenceSpan.Status().Code != codes.Error {
					t.Error("failed persistence span has no error status")
				}
			} else if persistenceSpan.Status().Code == codes.Error {
				t.Errorf("persistence outcome %q recorded an error status", tt.persistenceOutcome)
			}

			messageSpan := waitForEndedSpan(t, recorder, "nostr.event")
			if value, ok := attributeValue(messageSpan, "nostr.outcome"); !ok || value.AsString() != tt.wantMessageOutcome {
				t.Errorf("message outcome = %v (present=%v), want %q", value.AsString(), ok, tt.wantMessageOutcome)
			}

			rm := collectMetrics(t, reader)
			if count, ok := histogramCount(rm, "nostr.relay.event.persist.duration"); ok && count != 0 {
				t.Errorf("persistence histogram count = %d for outcome %q, want no samples", count, tt.persistenceOutcome)
			}
			messagePoint := singleHistogramDataPoint(t, rm, "nostr.relay.message.duration")
			if messagePoint.Count != 1 {
				t.Errorf("message histogram count = %d, want 1", messagePoint.Count)
			}
			assertMetricAttribute(t, messagePoint.Attributes, "nostr.persistence.outcome", tt.persistenceOutcome)
			assertMetricAttribute(t, messagePoint.Attributes, "nostr.outcome", tt.wantMessageOutcome)
		})
	}
}

func TestMessageMetricNormalizesUnknownOutcome(t *testing.T) {
	for _, outcome := range []string{"private reason for pubkey abc", ""} {
		telemetry, recorder, reader := newTestTelemetry(t)
		receivedAt := time.Now().Add(-time.Second)
		_, end := telemetry.messageObserver(context.Background(), &nostr.CountEnvelope{}, receivedAt)
		end(engine.MessageResult{Outcome: outcome, FinishedAt: receivedAt.Add(10 * time.Millisecond)})

		span := waitForEndedSpan(t, recorder, "nostr.count")
		if value, ok := attributeValue(span, "nostr.outcome"); !ok || value.AsString() != "error" {
			t.Errorf("span outcome = %v (present=%v), want error for %q", value.AsString(), ok, outcome)
		}
		if span.Status().Code != codes.Error {
			t.Error("unknown message outcome did not record an error status")
		}

		point := singleHistogramDataPoint(t, collectMetrics(t, reader), "nostr.relay.message.duration")
		assertMetricAttribute(t, point.Attributes, "nostr.outcome", "error")
		for _, attr := range point.Attributes.ToSlice() {
			if strings.Contains(attr.Value.AsString(), "private reason") {
				t.Errorf("arbitrary outcome text leaked into metric attribute %q", attr.Key)
			}
		}
	}
}

func TestStoredReplacementRecordsPersistenceHistogram(t *testing.T) {
	tests := []struct {
		name      string
		kind      int
		wantClass string
	}{
		{name: "replaceable", kind: 3, wantClass: "replaceable"},
		{name: "addressable", kind: 30001, wantClass: "addressable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			telemetry, recorder, reader := newTestTelemetry(t)

			receivedAt := time.Now().Add(-time.Second)
			processingCtx, end := telemetry.messageObserver(context.Background(), &nostr.EventEnvelope{
				Event: nostr.Event{Kind: tt.kind},
			}, receivedAt)
			telemetry.persistenceObserver(processingCtx, engine.PersistenceResult{
				Outcome:    "stored",
				FinishedAt: receivedAt.Add(45 * time.Millisecond),
			})
			end(engine.MessageResult{Outcome: "ok", FinishedAt: receivedAt.Add(55 * time.Millisecond)})

			rm := collectMetrics(t, reader)
			persistencePoint := singleHistogramDataPoint(t, rm, "nostr.relay.event.persist.duration")
			if persistencePoint.Count != 1 {
				t.Errorf("persistence histogram count = %d, want 1 for a stored replacement", persistencePoint.Count)
			}
			assertMetricAttribute(t, persistencePoint.Attributes, "nostr.event.class", tt.wantClass)
			messagePoint := singleHistogramDataPoint(t, rm, "nostr.relay.message.duration")
			assertMetricAttribute(t, messagePoint.Attributes, "nostr.event.class", tt.wantClass)
			waitForEndedSpan(t, recorder, "nostr.event.persist")
		})
	}
}

func TestPersistenceOutcomeNormalizesUnknownValue(t *testing.T) {
	telemetry, recorder, reader := newTestTelemetry(t)

	receivedAt := time.Now().Add(-time.Second)
	processingCtx, end := telemetry.messageObserver(context.Background(), &nostr.EventEnvelope{
		Event: nostr.Event{Kind: 1},
	}, receivedAt)
	telemetry.persistenceObserver(processingCtx, engine.PersistenceResult{
		Outcome:    "private storage reason",
		FinishedAt: receivedAt.Add(10 * time.Millisecond),
	})
	end(engine.MessageResult{Outcome: "error", FinishedAt: receivedAt.Add(20 * time.Millisecond)})

	persistenceSpan := waitForEndedSpan(t, recorder, "nostr.event.persist")
	if value, ok := attributeValue(persistenceSpan, "nostr.persistence.outcome"); !ok || value.AsString() != "error" {
		t.Errorf("persistence span outcome = %v (present=%v), want error", value.AsString(), ok)
	}
	rm := collectMetrics(t, reader)
	messagePoint := singleHistogramDataPoint(t, rm, "nostr.relay.message.duration")
	assertMetricAttribute(t, messagePoint.Attributes, "nostr.persistence.outcome", "error")
	if count, ok := histogramCount(rm, "nostr.relay.event.persist.duration"); ok && count != 0 {
		t.Errorf("persistence histogram count = %d for unknown outcome, want no samples", count)
	}
}

func TestMessageMetricsExcludeIdentifiersAndFilterShape(t *testing.T) {
	const (
		secretEventID = "secret-event-id"
		secretPubKey  = "secret-pubkey"
		secretContent = "secret-content"
		secretTag     = "secret-tag-value"
		secretSubID   = "secret-subscription-id"
	)

	secrets := []string{secretEventID, secretPubKey, secretContent, secretTag, secretSubID}

	t.Run("event", func(t *testing.T) {
		telemetry, _, reader := newTestTelemetry(t)
		receivedAt := time.Now().Add(-time.Second)
		processingCtx, end := telemetry.messageObserver(context.Background(), &nostr.EventEnvelope{
			Event: nostr.Event{
				ID:      secretEventID,
				PubKey:  secretPubKey,
				Kind:    1,
				Content: secretContent,
				Tags:    nostr.Tags{{"t", secretTag}},
			},
		}, receivedAt)
		telemetry.persistenceObserver(processingCtx, engine.PersistenceResult{
			Outcome:    "stored",
			FinishedAt: receivedAt.Add(50 * time.Millisecond),
		})
		end(engine.MessageResult{Outcome: "ok", FinishedAt: receivedAt.Add(60 * time.Millisecond)})

		rm := collectMetrics(t, reader)
		messagePoint := singleHistogramDataPoint(t, rm, "nostr.relay.message.duration")
		assertAllowedMetricAttributes(t, messagePoint.Attributes,
			"nostr.message.type", "nostr.outcome", "nostr.event.class", "nostr.persistence.outcome")
		persistencePoint := singleHistogramDataPoint(t, rm, "nostr.relay.event.persist.duration")
		assertAllowedMetricAttributes(t, persistencePoint.Attributes, "nostr.event.class", "nostr.persistence.outcome")
		assertNoMetricSecret(t, messagePoint.Attributes, secrets)
		assertNoMetricSecret(t, persistencePoint.Attributes, secrets)
	})

	t.Run("request", func(t *testing.T) {
		telemetry, _, reader := newTestTelemetry(t)
		receivedAt := time.Now().Add(-time.Second)
		_, end := telemetry.messageObserver(context.Background(), &nostr.ReqEnvelope{
			SubscriptionID: secretSubID,
			Filters: []nostr.Filter{{
				IDs:     []string{secretEventID},
				Authors: []string{secretPubKey},
				Kinds:   []int{1},
				Tags:    nostr.TagMap{"t": []string{secretTag}},
				Limit:   50,
			}},
		}, receivedAt)
		end(engine.MessageResult{Outcome: "ok", FinishedAt: receivedAt.Add(80 * time.Millisecond)})

		rm := collectMetrics(t, reader)
		messagePoint := singleHistogramDataPoint(t, rm, "nostr.relay.message.duration")
		assertAllowedMetricAttributes(t, messagePoint.Attributes, "nostr.message.type", "nostr.outcome")
		assertNoMetricSecret(t, messagePoint.Attributes, secrets)
	})
}

func TestMessageMetricsIgnoreTraceSampling(t *testing.T) {
	telemetry, recorder, reader := newTestTelemetryWithSampler(t, trace.NeverSample())

	receivedAt := time.Now().Add(-time.Second)
	processingCtx, end := telemetry.messageObserver(context.Background(), &nostr.EventEnvelope{
		Event: nostr.Event{Kind: 1},
	}, receivedAt)
	telemetry.persistenceObserver(processingCtx, engine.PersistenceResult{
		Outcome:    "stored",
		FinishedAt: receivedAt.Add(40 * time.Millisecond),
	})
	end(engine.MessageResult{Outcome: "ok", FinishedAt: receivedAt.Add(50 * time.Millisecond)})

	if spans := recorder.Ended(); len(spans) != 0 {
		t.Fatalf("unsampled trace recorded %d spans, want none", len(spans))
	}

	rm := collectMetrics(t, reader)
	if count, ok := histogramCount(rm, "nostr.relay.message.duration"); !ok || count != 1 {
		t.Errorf("message histogram count = %d (present=%v), want 1 without trace sampling", count, ok)
	}
	if count, ok := histogramCount(rm, "nostr.relay.event.persist.duration"); !ok || count != 1 {
		t.Errorf("persistence histogram count = %d (present=%v), want 1 without trace sampling", count, ok)
	}
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
	operation := telemetry.eventOperation("delete_event", func(context.Context, *nostr.Event) error {
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

func newTestTelemetry(t *testing.T) (*relayTelemetry, *tracetest.SpanRecorder, *metric.ManualReader) {
	t.Helper()
	return newTestTelemetryWithSampler(t, trace.AlwaysSample())
}

func newTestTelemetryWithSampler(t *testing.T, sampler trace.Sampler) (*relayTelemetry, *tracetest.SpanRecorder, *metric.ManualReader) {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	tracerProvider := trace.NewTracerProvider(
		trace.WithSpanProcessor(recorder),
		trace.WithSampler(sampler),
	)
	previousTracer := otel.GetTracerProvider()
	otel.SetTracerProvider(tracerProvider)

	reader := metric.NewManualReader()
	meterProvider := metric.NewMeterProvider(metric.WithReader(reader))
	previousMeter := otel.GetMeterProvider()
	otel.SetMeterProvider(meterProvider)

	t.Cleanup(func() {
		otel.SetTracerProvider(previousTracer)
		otel.SetMeterProvider(previousMeter)
		tracerProvider.Shutdown(context.Background())
		meterProvider.Shutdown(context.Background())
	})

	telemetry, err := newRelayTelemetry()
	if err != nil {
		t.Fatalf("newRelayTelemetry() error = %v", err)
	}
	return telemetry, recorder, reader
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

	rm := collectMetrics(t, reader)

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

func collectMetrics(t *testing.T, reader *metric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	return rm
}

func singleHistogramDataPoint(t *testing.T, rm metricdata.ResourceMetrics, name string) metricdata.HistogramDataPoint[float64] {
	t.Helper()

	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			histogram, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("metric %q data type = %T, want float64 histogram", name, m.Data)
			}
			if len(histogram.DataPoints) != 1 {
				t.Fatalf("metric %q data points = %d, want 1", name, len(histogram.DataPoints))
			}
			return histogram.DataPoints[0]
		}
	}
	t.Fatalf("metric %q was not exported", name)
	return metricdata.HistogramDataPoint[float64]{}
}

func assertMetricAttribute(t *testing.T, attributes attribute.Set, key, want string) {
	t.Helper()

	value, ok := attributes.Value(attribute.Key(key))
	if !ok {
		t.Errorf("metric attribute %q is missing, want %q", key, want)
		return
	}
	if value.AsString() != want {
		t.Errorf("metric attribute %q = %q, want %q", key, value.AsString(), want)
	}
}

func assertAllowedMetricAttributes(t *testing.T, attributes attribute.Set, allowed ...string) {
	t.Helper()

	for _, attr := range attributes.ToSlice() {
		if !slices.Contains(allowed, string(attr.Key)) {
			t.Errorf("unexpected metric attribute %q", attr.Key)
		}
	}
}

func assertNoMetricSecret(t *testing.T, attributes attribute.Set, secrets []string) {
	t.Helper()

	for _, attr := range attributes.ToSlice() {
		value := attr.Value.AsString()
		for _, secret := range secrets {
			if strings.Contains(value, secret) {
				t.Errorf("metric attribute %q leaked %q", attr.Key, secret)
			}
		}
	}
}

func approximately(got, want float64) bool {
	const tolerance = 1e-9
	diff := got - want
	return diff < tolerance && diff > -tolerance
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
	relay.OnEventPersistence = telemetry.persistenceObserver

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
	if value, ok := attributeValue(closeSpan, "nostr.outcome"); !ok || value.AsString() != "ok" {
		t.Errorf("nostr.outcome = %v (present=%v), want explicit ok", value.AsString(), ok)
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

package relay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"go.opentelemetry.io/otel"
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

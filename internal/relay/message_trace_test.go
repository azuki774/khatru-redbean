package relay

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/azuki774/khatru-redbean/internal/nostr"
	"github.com/azuki774/khatru-redbean/internal/relay/engine"
	"github.com/azuki774/khatru-redbean/internal/storage"
	"github.com/coder/websocket"
	"go.opentelemetry.io/otel/attribute"
)

func TestPostedEventsKeepOneTraceWithOperationChildrenAndPersistenceEvent(t *testing.T) {
	telemetry, recorder, reader := newTestTelemetry(t)
	relay := engine.NewRelay()
	finished := make(chan struct{}, 2)
	relay.OnMessage = func(ctx context.Context, envelope any, receivedAt time.Time) (context.Context, func(engine.MessageResult)) {
		ctx, end := telemetry.messageObserver(ctx, envelope, receivedAt)
		return ctx, func(result engine.MessageResult) {
			end(result)
			finished <- struct{}{}
		}
	}
	relay.OnEventPersistence = telemetry.persistenceObserver
	relay.RejectEvent = append(relay.RejectEvent, telemetry.eventPolicy("trace_test", func(context.Context, *nostr.Event) (bool, string) {
		return false, ""
	}))
	relay.StoreEvent = append(relay.StoreEvent, telemetry.writeOperation("save_event", func(context.Context, *nostr.Event) (storage.WriteOutcome, error) {
		return storage.WriteStored, nil
	}))
	server := httptest.NewServer(telemetry.httpMiddleware(relay))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	for _, content := range []string{"first post", "second post"} {
		event := nostr.Event{Kind: 1, CreatedAt: nostr.Now(), Tags: nostr.Tags{}, Content: content}
		if err := event.Sign(strings.Repeat("1", 64)); err != nil {
			t.Fatal(err)
		}
		payload, err := event.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Write(ctx, websocket.MessageText, append(append([]byte(`["EVENT",`), payload...), ']')); err != nil {
			t.Fatal(err)
		}
		if _, response, err := conn.Read(ctx); err != nil {
			t.Fatal(err)
		} else if !strings.HasPrefix(string(response), `["OK"`) {
			t.Fatalf("unexpected response %s", response)
		}
		select {
		case <-finished:
		case <-ctx.Done():
			t.Fatal("message did not finish")
		}
	}

	roots := 0
	traces := make(map[string]bool)
	var responseSeconds, persistenceSeconds float64
	for _, root := range recorder.Ended() {
		if root.Name() != "nostr.event" {
			continue
		}
		roots++
		traceID := root.SpanContext().TraceID().String()
		if traces[traceID] || root.Parent().IsValid() {
			t.Fatal("posts do not have independent root traces")
		}
		traces[traceID] = true
		events := root.Events()
		if len(events) != 1 || events[0].Name != "nostr.persistence.completed" {
			t.Fatalf("message events = %v, want one persistence completion event", events)
		}
		completion := events[0]
		assertMetricAttribute(t, attribute.NewSet(completion.Attributes...), "nostr.persistence.outcome", "stored")
		if completion.Time.Before(root.StartTime()) || completion.Time.After(root.EndTime()) {
			t.Fatal("persistence completion is outside message duration")
		}
		foundPolicy, foundDatabase := false, false
		for _, child := range recorder.Ended() {
			if child.SpanContext().TraceID() != root.SpanContext().TraceID() || child.SpanContext().SpanID() == root.SpanContext().SpanID() {
				continue
			}
			if child.Name() == "nostr.event.persist" || child.Parent().SpanID() != root.SpanContext().SpanID() {
				t.Fatalf("unexpected span hierarchy for %s", child.Name())
			}
			switch child.Name() {
			case "nostr.policy.trace_test":
				foundPolicy = true
			case "postgres.save_event":
				foundDatabase = true
				if child.EndTime().After(completion.Time) {
					t.Fatal("persistence completion precedes database completion")
				}
			}
		}
		if !foundPolicy || !foundDatabase {
			t.Fatal("operation spans are missing from the post trace")
		}
		responseSeconds += root.EndTime().Sub(root.StartTime()).Seconds()
		persistenceSeconds += completion.Time.Sub(root.StartTime()).Seconds()
	}
	if roots != 2 {
		t.Fatalf("message root count = %d, want one per post", roots)
	}
	rm := collectMetrics(t, reader)
	response := singleHistogramDataPoint(t, rm, "nostr.relay.message.duration")
	persistence := singleHistogramDataPoint(t, rm, "nostr.relay.event.persist.duration")
	if response.Count != 2 || persistence.Count != 2 || !approximately(response.Sum, responseSeconds) || !approximately(persistence.Sum, persistenceSeconds) {
		t.Fatalf("histogram intervals do not match post traces: response=%+v persistence=%+v", response, persistence)
	}
}

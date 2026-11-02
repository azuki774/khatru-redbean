package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/azuki774/khatru-redbean/internal/nostr"
	"github.com/azuki774/khatru-redbean/internal/storage"
)

func TestSavedEventWithFailedACKIsNotReportedAsSuccessfulResponse(t *testing.T) {
	storing := make(chan *WebSocket, 1)
	release := make(chan struct{})
	results := make(chan MessageResult, 1)
	persistence := make(chan PersistenceResult, 1)
	_, server, ctx := startTestRelay(t, func(relay *Relay) {
		relay.StoreEvent = append(relay.StoreEvent, func(ctx context.Context, _ *nostr.Event) (storage.WriteOutcome, error) {
			storing <- GetConnection(ctx)
			<-release
			return storage.WriteStored, nil
		})
		relay.OnMessage = func(ctx context.Context, _ any, _ time.Time) (context.Context, func(MessageResult)) {
			return ctx, func(result MessageResult) { results <- result }
		}
		relay.OnEventPersistence = func(_ context.Context, result PersistenceResult) {
			persistence <- result
		}
	})
	conn := dialTestRelay(t, ctx, server)
	event := nostr.Event{Kind: 1, CreatedAt: nostr.Now(), Tags: nostr.Tags{}, Content: "saved before ACK failed"}
	if err := event.Sign(strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(nostr.EventEnvelope{Event: event})
	if err != nil {
		t.Fatal(err)
	}
	writeTestMessage(t, ctx, conn, string(payload))
	select {
	case ws := <-storing:
		// Close the real server socket before allowing storage to complete, so
		// the ACK write must fail without relying on client-close scheduling.
		if err := ws.conn.Close(); err != nil {
			close(release)
			t.Fatal(err)
		}
		close(release)
	case <-ctx.Done():
		close(release)
		t.Fatal("event did not reach storage")
	}
	select {
	case result := <-persistence:
		if result.Outcome != string(storage.WriteStored) {
			t.Fatalf("persistence outcome = %q, want stored", result.Outcome)
		}
	case <-ctx.Done():
		t.Fatal("missing persistence result")
	}
	select {
	case result := <-results:
		if result.Outcome != "write_error" {
			t.Fatalf("message outcome = %q, want write_error", result.Outcome)
		}
	case <-ctx.Done():
		t.Fatal("missing message result")
	}
}

package monitor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/azuki774/khatru-redbean/internal/nostr"
	"github.com/coder/websocket"
)

// TestProbeReadsLargeEvent verifies Probe returns the latest event instead of
// failing the read when the relay sends a 512KiB event.
func TestProbeReadsLargeEvent(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	event := nostr.Event{
		CreatedAt: nostr.Timestamp(now.Add(-time.Hour).Unix()),
		Kind:      1,
		Tags:      nostr.Tags{},
		Content:   strings.Repeat("x", 512<<10),
	}
	if err := event.Sign(nostr.GeneratePrivateKey()); err != nil {
		t.Fatal(err)
	}
	relay := serveProbe(t, func(c *websocket.Conn, ctx context.Context) {
		readRequest(t, c, ctx)
		writeFrame(c, ctx, []any{"EVENT", "monitor", event})
		writeFrame(c, ctx, []any{"EOSE", "monitor"})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := Probe(ctx, relay, now)
	if err != nil {
		t.Fatalf("probe of large event: %v", err)
	}
	if !r.HasEvent || r.LatestAt == nil || !r.LatestAt.Equal(time.Unix(int64(event.CreatedAt), 0)) {
		t.Fatalf("result=%+v", r)
	}
}

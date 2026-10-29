package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/azuki774/khatru-redbean/internal/nostr"
	"github.com/azuki774/khatru-redbean/internal/relay/engine"
	"github.com/coder/websocket"
)

func connectionContext(t *testing.T, country string) context.Context {
	t.Helper()
	contexts := make(chan context.Context, 1)
	relay := engine.NewRelay()
	relay.OnConnect = append(relay.OnConnect, func(ctx context.Context) {
		contexts <- ctx
	})
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	header := http.Header{}
	if country != "" {
		header.Set("CF-IPCountry", country)
	}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	select {
	case connection := <-contexts:
		return connection
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return nil
	}
}

func TestRestrictCountry(t *testing.T) {
	tests := []struct {
		name       string
		allowed    string
		country    string
		connection bool
		reject     bool
	}{
		{name: "unrestricted"},
		{name: "missing connection", allowed: "JP", reject: true},
		{name: "disallowed country", allowed: "JP", country: "US", connection: true, reject: true},
		{name: "allowed country", allowed: "JP", country: "JP", connection: true},
		{name: "missing country", allowed: "JP", connection: true, reject: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.connection {
				ctx = connectionContext(t, tt.country)
			}
			i := &instance{CountryOnly: tt.allowed}
			reject, message := i.RestrictCountry(ctx, &nostr.Event{})
			if reject != tt.reject {
				t.Fatalf("reject = %v, want %v", reject, tt.reject)
			}
			if reject && !strings.HasPrefix(message, "blocked:") {
				t.Errorf("rejection must use the blocked protocol prefix: %q", message)
			}
			if !reject && message != "" {
				t.Errorf("allowed event has a rejection message: %q", message)
			}
		})
	}
}

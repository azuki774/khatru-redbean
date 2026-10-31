package engine

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azuki774/khatru-redbean/internal/nostr"
	"github.com/azuki774/khatru-redbean/internal/nostr/nip77"
	"github.com/coder/websocket"
)

func TestOutcomeFromReason(t *testing.T) {
	cases := map[string]string{
		"blocked: nope":        "blocked",
		"invalid: bad id":      "invalid",
		"auth-required: login": "auth-required",
		"mute: nobody around":  "mute",
		"unsupported: nope":    "unsupported",
		"ok":                   "ok",
		// arbitrary error strings must never leak into the outcome label
		"read tcp 10.0.0.1:4567: connection reset by peer": "error",
		"read tcp 10.0.0.1": "error",
		"":                  "error",
	}

	for reason, want := range cases {
		if got := outcomeFromReason(reason); got != want {
			t.Errorf("outcomeFromReason(%q) = %q, want %q", reason, got, want)
		}
	}
}

func TestOnMessageEVENTOutcomes(t *testing.T) {
	hook := newMessageHook()
	_, server, ctx := startTestRelay(t, func(relay *Relay) {
		relay.OnMessage = hook.observe
	})
	conn := dialTestRelay(t, ctx, server)

	signed := &nostr.Event{Kind: 1, CreatedAt: nostr.Now(), Content: "hello"}
	if err := signed.Sign(strings.Repeat("1", 64)); err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	payload, err := json.Marshal(nostr.EventEnvelope{Event: *signed})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	writeTestMessage(t, ctx, conn, string(payload))
	if response := readTestMessage(t, ctx, conn); !strings.HasPrefix(response, `["OK"`) {
		t.Fatalf("response = %s, want OK", response)
	}
	call := hook.next(t)
	assertEnvelopeType(t, call, (*nostr.EventEnvelope)(nil))
	if outcome := call.wait(t); outcome != "ok" {
		t.Errorf("outcome = %q, want ok", outcome)
	}

	invalid := &nostr.Event{ID: strings.Repeat("0", 64), Kind: 1, CreatedAt: nostr.Now()}
	payload, err = json.Marshal(nostr.EventEnvelope{Event: *invalid})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	writeTestMessage(t, ctx, conn, string(payload))
	if response := readTestMessage(t, ctx, conn); !strings.Contains(response, `"invalid:`) {
		t.Fatalf("response = %s, want invalid reason", response)
	}
	call = hook.next(t)
	assertEnvelopeType(t, call, (*nostr.EventEnvelope)(nil))
	if outcome := call.wait(t); outcome != "invalid" {
		t.Errorf("outcome = %q, want invalid", outcome)
	}
}

func TestOnMessageCOUNTUnsupported(t *testing.T) {
	hook := newMessageHook()
	_, server, ctx := startTestRelay(t, func(relay *Relay) {
		relay.OnMessage = hook.observe
	})
	conn := dialTestRelay(t, ctx, server)

	writeTestMessage(t, ctx, conn, `["COUNT","sub",{"kinds":[1]}]`)
	if response := readTestMessage(t, ctx, conn); !strings.HasPrefix(response, `["CLOSED"`) {
		t.Fatalf("response = %s, want CLOSED", response)
	}

	call := hook.next(t)
	assertEnvelopeType(t, call, (*nostr.CountEnvelope)(nil))
	if outcome := call.wait(t); outcome != "unsupported" {
		t.Errorf("outcome = %q, want unsupported", outcome)
	}
}

func TestOnMessageAUTHInvalid(t *testing.T) {
	hook := newMessageHook()
	_, server, ctx := startTestRelay(t, func(relay *Relay) {
		relay.OnMessage = hook.observe
	})
	conn := dialTestRelay(t, ctx, server)

	payload, err := json.Marshal(nostr.AuthEnvelope{Event: nostr.Event{Kind: 1}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	writeTestMessage(t, ctx, conn, string(payload))
	if response := readTestMessage(t, ctx, conn); !strings.Contains(response, `"error: failed to authenticate"`) {
		t.Fatalf("response = %s, want authentication failure", response)
	}

	call := hook.next(t)
	assertEnvelopeType(t, call, (*nostr.AuthEnvelope)(nil))
	if outcome := call.wait(t); outcome != "invalid" {
		t.Errorf("outcome = %q, want invalid", outcome)
	}
}

func TestOnMessageCLOSE(t *testing.T) {
	hook := newMessageHook()
	_, server, ctx := startTestRelay(t, func(relay *Relay) {
		relay.OnMessage = hook.observe
	})
	conn := dialTestRelay(t, ctx, server)

	writeTestMessage(t, ctx, conn, `["CLOSE","sub"]`)

	call := hook.next(t)
	assertEnvelopeType(t, call, (*nostr.CloseEnvelope)(nil))
	if outcome := call.wait(t); outcome != "ok" {
		t.Errorf("outcome = %q, want ok", outcome)
	}
}

func TestOnMessageParseError(t *testing.T) {
	hook := newMessageHook()
	_, server, ctx := startTestRelay(t, func(relay *Relay) {
		relay.OnMessage = hook.observe
	})
	conn := dialTestRelay(t, ctx, server)

	writeTestMessage(t, ctx, conn, `not json`)
	if response := readTestMessage(t, ctx, conn); !strings.HasPrefix(response, `["NOTICE"`) {
		t.Fatalf("response = %s, want NOTICE", response)
	}

	call := hook.next(t)
	assertEnvelopeType(t, call, nil)
	if outcome := call.wait(t); outcome != "invalid" {
		t.Errorf("outcome = %q, want invalid", outcome)
	}
}

func TestOnMessageNEGENTROPYEnvelope(t *testing.T) {
	hook := newMessageHook()
	_, server, ctx := startTestRelay(t, func(relay *Relay) {
		relay.Negentropy = true
		relay.OnMessage = hook.observe
	})
	conn := dialTestRelay(t, ctx, server)

	writeTestMessage(t, ctx, conn, `["NEG-CLOSE","sub"]`)

	call := hook.next(t)
	assertEnvelopeType(t, call, (*nip77.CloseEnvelope)(nil))
	if outcome := call.wait(t); outcome != "ok" {
		t.Errorf("outcome = %q, want ok", outcome)
	}
}

func TestOnMessageREQSpanEndsAfterEOSE(t *testing.T) {
	hook := newMessageHook()
	source := make(chan *nostr.Event, 1)
	_, server, ctx := startTestRelay(t, func(relay *Relay) {
		relay.OnMessage = hook.observe
		relay.QueryEvents = append(relay.QueryEvents, func(context.Context, nostr.Filter) (chan *nostr.Event, error) {
			return source, nil
		})
	})
	conn := dialTestRelay(t, ctx, server)

	payload, err := json.Marshal(nostr.ReqEnvelope{
		SubscriptionID: "sub",
		Filters:        []nostr.Filter{{Kinds: []int{1}}},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	writeTestMessage(t, ctx, conn, string(payload))

	call := hook.next(t)
	assertEnvelopeType(t, call, (*nostr.ReqEnvelope)(nil))

	// the message is still being processed: no EOSE yet, so the span is open
	select {
	case <-call.ended:
		t.Fatal("message span ended before the stored events were dispatched")
	case <-time.After(100 * time.Millisecond):
	}

	source <- &nostr.Event{ID: strings.Repeat("a", 64), Kind: 1, CreatedAt: nostr.Now()}
	close(source)

	if response := readTestMessage(t, ctx, conn); !strings.HasPrefix(response, `["EVENT"`) {
		t.Fatalf("response = %s, want EVENT", response)
	}
	if response := readTestMessage(t, ctx, conn); strings.TrimSpace(response) != `["EOSE","sub"]` {
		t.Fatalf("response = %s, want EOSE", response)
	}
	if outcome := call.wait(t); outcome != "ok" {
		t.Errorf("outcome = %q, want ok", outcome)
	}
}

func TestOnMessageREQRejectionOutcome(t *testing.T) {
	hook := newMessageHook()
	_, server, ctx := startTestRelay(t, func(relay *Relay) {
		relay.OnMessage = hook.observe
		relay.RejectFilter = append(relay.RejectFilter, func(context.Context, nostr.Filter) (bool, string) {
			return true, "blocked: test filter"
		})
	})
	conn := dialTestRelay(t, ctx, server)

	payload, err := json.Marshal(nostr.ReqEnvelope{
		SubscriptionID: "sub",
		Filters:        []nostr.Filter{{Kinds: []int{1}}},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	writeTestMessage(t, ctx, conn, string(payload))

	if response := readTestMessage(t, ctx, conn); !strings.Contains(response, `"blocked: test filter"`) {
		t.Fatalf("response = %s, want blocked CLOSED", response)
	}

	call := hook.next(t)
	assertEnvelopeType(t, call, (*nostr.ReqEnvelope)(nil))
	if outcome := call.wait(t); outcome != "blocked" {
		t.Errorf("outcome = %q, want blocked", outcome)
	}
}

type messageSpanCall struct {
	envelope any
	outcome  string
	ended    chan struct{}
	once     sync.Once
}

type messageHook struct {
	calls chan *messageSpanCall
}

func newMessageHook() *messageHook {
	return &messageHook{calls: make(chan *messageSpanCall, 16)}
}

func (h *messageHook) observe(ctx context.Context, envelope any) (context.Context, func(string)) {
	call := &messageSpanCall{envelope: envelope, ended: make(chan struct{})}
	h.calls <- call
	return ctx, func(outcome string) {
		call.once.Do(func() {
			call.outcome = outcome
			close(call.ended)
		})
	}
}

func (h *messageHook) next(t *testing.T) *messageSpanCall {
	t.Helper()

	select {
	case call := <-h.calls:
		return call
	case <-time.After(3 * time.Second):
		t.Fatal("OnMessage was not called")
		return nil
	}
}

func (call *messageSpanCall) wait(t *testing.T) string {
	t.Helper()

	select {
	case <-call.ended:
		return call.outcome
	case <-time.After(3 * time.Second):
		t.Fatal("OnMessage end was not called")
		return ""
	}
}

func assertEnvelopeType(t *testing.T, call *messageSpanCall, want any) {
	t.Helper()

	if reflect.TypeOf(call.envelope) != reflect.TypeOf(want) {
		t.Fatalf("OnMessage envelope = %T, want %T", call.envelope, want)
	}
}

func startTestRelay(t *testing.T, configure func(*Relay)) (*Relay, *httptest.Server, context.Context) {
	t.Helper()

	relay := NewRelay()
	if configure != nil {
		configure(relay)
	}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	return relay, server, ctx
}

func dialTestRelay(t *testing.T, ctx context.Context, server *httptest.Server) *websocket.Conn {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket.Dial() error = %v", err)
	}
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "") })

	return conn
}

func writeTestMessage(t *testing.T, ctx context.Context, conn *websocket.Conn, message string) {
	t.Helper()

	if err := conn.Write(ctx, websocket.MessageText, []byte(message)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
}

func readTestMessage(t *testing.T, ctx context.Context, conn *websocket.Conn) string {
	t.Helper()

	if _, message, err := conn.Read(ctx); err != nil {
		t.Fatalf("Read() error = %v", err)
	} else {
		return string(message)
	}
	return ""
}

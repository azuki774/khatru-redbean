package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azuki774/khatru-redbean/internal/nostr"
	"github.com/azuki774/khatru-redbean/internal/nostr/nip77"
	"github.com/azuki774/khatru-redbean/internal/storage"
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

func TestOnEventPersistenceStoredThenDuplicate(t *testing.T) {
	hook := newMessageHook()
	persist := newPersistenceHook()
	var mu sync.Mutex
	seen := map[string]bool{}

	_, server, ctx := startTestRelay(t, func(relay *Relay) {
		relay.OnMessage = hook.observe
		relay.OnEventPersistence = persist.observe
		relay.StoreEvent = append(relay.StoreEvent, func(_ context.Context, evt *nostr.Event) (storage.WriteOutcome, error) {
			mu.Lock()
			defer mu.Unlock()
			if seen[evt.ID] {
				return storage.WriteDuplicate, nil
			}
			seen[evt.ID] = true
			return storage.WriteStored, nil
		})
	})
	conn := dialTestRelay(t, ctx, server)

	// a live subscription lets us observe whether the event is broadcast
	writeTestMessage(t, ctx, conn, `["REQ","sub",{"kinds":[1],"limit":0}]`)
	if response := readTestMessage(t, ctx, conn); strings.TrimSpace(response) != `["EOSE","sub"]` {
		t.Fatalf("response = %s, want EOSE", response)
	}
	reqCall := hook.next(t)
	assertEnvelopeType(t, reqCall, (*nostr.ReqEnvelope)(nil))
	if outcome := reqCall.wait(t); outcome != "ok" {
		t.Errorf("REQ outcome = %q, want ok", outcome)
	}

	signed := signedTestEvent(t, 1, "hello")
	payload := eventPayload(t, signed)

	writeTestMessage(t, ctx, conn, payload)
	if response := readTestMessage(t, ctx, conn); !strings.HasPrefix(response, `["EVENT"`) {
		t.Fatalf("response = %s, want broadcast EVENT", response)
	}
	assertOKResponse(t, readTestMessage(t, ctx, conn), true)

	call := hook.next(t)
	assertEnvelopeType(t, call, (*nostr.EventEnvelope)(nil))
	if outcome := call.wait(t); outcome != "ok" {
		t.Errorf("outcome = %q, want ok", outcome)
	}
	if call.finishedAt.Before(call.receivedAt) || call.finishedAt.IsZero() {
		t.Errorf("FinishedAt = %v, receivedAt = %v", call.finishedAt, call.receivedAt)
	}
	stored := persist.next(t)
	if stored.Outcome != string(storage.WriteStored) || stored.FinishedAt.IsZero() {
		t.Errorf("persistence = %+v, want stored with FinishedAt", stored)
	}
	if stored.FinishedAt.After(call.finishedAt) {
		t.Errorf("persistence finished %v after message %v", stored.FinishedAt, call.finishedAt)
	}

	// the duplicate is acknowledged but must not be broadcast again
	writeTestMessage(t, ctx, conn, payload)
	assertOKResponse(t, readTestMessage(t, ctx, conn), true)

	call = hook.next(t)
	if outcome := call.wait(t); outcome != "ok" {
		t.Errorf("outcome = %q, want ok", outcome)
	}
	if duplicate := persist.next(t); duplicate.Outcome != string(storage.WriteDuplicate) {
		t.Errorf("persistence = %q, want duplicate", duplicate.Outcome)
	}
	expectNoTestMessage(t, ctx, conn)
}

func TestOnEventPersistenceStorageError(t *testing.T) {
	hook := newMessageHook()
	persist := newPersistenceHook()

	_, server, ctx := startTestRelay(t, func(relay *Relay) {
		relay.OnMessage = hook.observe
		relay.OnEventPersistence = persist.observe
		relay.StoreEvent = append(relay.StoreEvent, func(context.Context, *nostr.Event) (storage.WriteOutcome, error) {
			return "", errors.New("db down")
		})
	})
	conn := dialTestRelay(t, ctx, server)

	publishAndExpectOK(t, ctx, conn, signedTestEvent(t, 1, "hello"), false)

	call := hook.next(t)
	if outcome := call.wait(t); outcome != "error" {
		t.Errorf("outcome = %q, want error", outcome)
	}
	if result := persist.next(t); result.Outcome != "error" {
		t.Errorf("persistence = %q, want error", result.Outcome)
	}
}

func TestOnEventPersistenceCanceled(t *testing.T) {
	hook := newMessageHook()
	persist := newPersistenceHook()

	_, server, ctx := startTestRelay(t, func(relay *Relay) {
		relay.OnMessage = hook.observe
		relay.OnEventPersistence = persist.observe
		relay.StoreEvent = append(relay.StoreEvent, func(context.Context, *nostr.Event) (storage.WriteOutcome, error) {
			return "", fmt.Errorf("save: %w", context.Canceled)
		})
	})
	conn := dialTestRelay(t, ctx, server)

	publishAndExpectOK(t, ctx, conn, signedTestEvent(t, 1, "hello"), false)

	call := hook.next(t)
	if outcome := call.wait(t); outcome != "canceled" {
		t.Errorf("outcome = %q, want canceled", outcome)
	}
	if result := persist.next(t); result.Outcome != "canceled" {
		t.Errorf("persistence = %q, want canceled", result.Outcome)
	}
}

func TestOnEventPersistenceNotAttemptedForInvalidEvent(t *testing.T) {
	hook := newMessageHook()
	persist := newPersistenceHook()
	var mu sync.Mutex
	storeCalled := false

	_, server, ctx := startTestRelay(t, func(relay *Relay) {
		relay.OnMessage = hook.observe
		relay.OnEventPersistence = persist.observe
		relay.StoreEvent = append(relay.StoreEvent, func(context.Context, *nostr.Event) (storage.WriteOutcome, error) {
			mu.Lock()
			storeCalled = true
			mu.Unlock()
			return storage.WriteStored, nil
		})
	})
	conn := dialTestRelay(t, ctx, server)

	invalid := &nostr.Event{ID: strings.Repeat("0", 64), Kind: 1, CreatedAt: nostr.Now()}
	payload, err := json.Marshal(nostr.EventEnvelope{Event: *invalid})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	writeTestMessage(t, ctx, conn, string(payload))
	if response := readTestMessage(t, ctx, conn); !strings.Contains(response, `"invalid:`) {
		t.Fatalf("response = %s, want invalid reason", response)
	}

	call := hook.next(t)
	if outcome := call.wait(t); outcome != "invalid" {
		t.Errorf("outcome = %q, want invalid", outcome)
	}
	if result := persist.next(t); result.Outcome != "not_attempted" {
		t.Errorf("persistence = %q, want not_attempted", result.Outcome)
	}

	mu.Lock()
	called := storeCalled
	mu.Unlock()
	if called {
		t.Error("storage was attempted for an invalid event")
	}
}

func TestOnEventPersistenceSuperseded(t *testing.T) {
	hook := newMessageHook()
	persist := newPersistenceHook()

	_, server, ctx := startTestRelay(t, func(relay *Relay) {
		relay.OnMessage = hook.observe
		relay.OnEventPersistence = persist.observe
		relay.ReplaceEvent = append(relay.ReplaceEvent, func(context.Context, *nostr.Event) (storage.WriteOutcome, error) {
			return storage.WriteSuperseded, nil
		})
	})
	conn := dialTestRelay(t, ctx, server)

	// the event is superseded but still acknowledged and broadcast, as before
	publishAndExpectOK(t, ctx, conn, signedTestEvent(t, 10002, "relay list"), true)

	call := hook.next(t)
	if outcome := call.wait(t); outcome != "ok" {
		t.Errorf("outcome = %q, want ok", outcome)
	}
	if result := persist.next(t); result.Outcome != string(storage.WriteSuperseded) {
		t.Errorf("persistence = %q, want superseded", result.Outcome)
	}
}

func TestOnEventPersistenceBeforeBroadcast(t *testing.T) {
	hook := newMessageHook()
	var mu sync.Mutex
	var order []string

	_, server, ctx := startTestRelay(t, func(relay *Relay) {
		relay.OnMessage = hook.observe
		relay.OnEventPersistence = func(ctx context.Context, result PersistenceResult) context.Context {
			mu.Lock()
			order = append(order, "persist:"+result.Outcome)
			mu.Unlock()
			return ctx
		}
		relay.StoreEvent = append(relay.StoreEvent, func(context.Context, *nostr.Event) (storage.WriteOutcome, error) {
			return storage.WriteStored, nil
		})
		relay.PreventBroadcast = append(relay.PreventBroadcast, func(*WebSocket, *nostr.Event) bool {
			mu.Lock()
			order = append(order, "broadcast")
			mu.Unlock()
			return false
		})
	})
	conn := dialTestRelay(t, ctx, server)

	writeTestMessage(t, ctx, conn, `["REQ","sub",{"kinds":[1],"limit":0}]`)
	if response := readTestMessage(t, ctx, conn); strings.TrimSpace(response) != `["EOSE","sub"]` {
		t.Fatalf("response = %s, want EOSE", response)
	}
	reqCall := hook.next(t)
	assertEnvelopeType(t, reqCall, (*nostr.ReqEnvelope)(nil))
	if outcome := reqCall.wait(t); outcome != "ok" {
		t.Errorf("REQ outcome = %q, want ok", outcome)
	}

	writeTestMessage(t, ctx, conn, eventPayload(t, signedTestEvent(t, 1, "hello")))
	if response := readTestMessage(t, ctx, conn); !strings.HasPrefix(response, `["EVENT"`) {
		t.Fatalf("response = %s, want broadcast EVENT", response)
	}
	assertOKResponse(t, readTestMessage(t, ctx, conn), true)
	if outcome := hook.next(t).wait(t); outcome != "ok" {
		t.Errorf("outcome = %q, want ok", outcome)
	}

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	want := []string{"persist:stored", "broadcast"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v (persistence must finish before broadcast)", got, want)
	}
}

func signedTestEvent(t *testing.T, kind int, content string) *nostr.Event {
	t.Helper()

	evt := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Content: content}
	if err := evt.Sign(strings.Repeat("1", 64)); err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	return evt
}

func eventPayload(t *testing.T, evt *nostr.Event) string {
	t.Helper()

	payload, err := json.Marshal(nostr.EventEnvelope{Event: *evt})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return string(payload)
}

func publishAndExpectOK(t *testing.T, ctx context.Context, conn *websocket.Conn, evt *nostr.Event, wantOK bool) {
	t.Helper()

	writeTestMessage(t, ctx, conn, eventPayload(t, evt))
	assertOKResponse(t, readTestMessage(t, ctx, conn), wantOK)
}

func assertOKResponse(t *testing.T, response string, wantOK bool) {
	t.Helper()

	var fields []json.RawMessage
	if err := json.Unmarshal([]byte(response), &fields); err != nil || len(fields) != 4 {
		t.Fatalf("invalid OK response %s: %v", response, err)
	}
	var label string
	var accepted bool
	if err := json.Unmarshal(fields[0], &label); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fields[2], &accepted); err != nil {
		t.Fatal(err)
	}
	if label != "OK" || accepted != wantOK {
		t.Fatalf("response = %s, want OK acceptance %t", response, wantOK)
	}
}

func expectNoTestMessage(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	t.Helper()

	readCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if _, message, err := conn.Read(readCtx); err == nil {
		t.Fatalf("unexpected message %s", message)
	}
}

type persistenceHook struct {
	calls chan PersistenceResult
}

func newPersistenceHook() *persistenceHook {
	return &persistenceHook{calls: make(chan PersistenceResult, 16)}
}

func (h *persistenceHook) observe(ctx context.Context, result PersistenceResult) context.Context {
	h.calls <- result
	return ctx
}

func (h *persistenceHook) next(t *testing.T) PersistenceResult {
	t.Helper()

	select {
	case result := <-h.calls:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("OnEventPersistence was not called")
		return PersistenceResult{}
	}
}

type messageSpanCall struct {
	envelope   any
	receivedAt time.Time
	outcome    string
	finishedAt time.Time
	ended      chan struct{}
	once       sync.Once
}

type messageHook struct {
	calls chan *messageSpanCall
}

func newMessageHook() *messageHook {
	return &messageHook{calls: make(chan *messageSpanCall, 16)}
}

func (h *messageHook) observe(ctx context.Context, envelope any, receivedAt time.Time) (context.Context, func(MessageResult)) {
	call := &messageSpanCall{envelope: envelope, receivedAt: receivedAt, ended: make(chan struct{})}
	h.calls <- call
	return ctx, func(result MessageResult) {
		call.once.Do(func() {
			call.outcome = result.Outcome
			call.finishedAt = result.FinishedAt
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

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/azuki774/khatru-redbean/internal/nostr"
	"github.com/azuki774/khatru-redbean/internal/storage"
)

func TestDeletionFailuresHaveAccuratePersistenceOutcome(t *testing.T) {
	cases := []struct {
		name        string
		deleteError error
		reject      bool
		persistence string
		message     string
	}{
		{name: "database failure", deleteError: errors.New("database unavailable"), persistence: "error", message: "error"},
		{name: "canceled deletion", deleteError: context.Canceled, persistence: "canceled", message: "canceled"},
		{name: "policy rejection", reject: true, persistence: "not_attempted", message: "blocked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hook := newMessageHook()
			persist := newPersistenceHook()
			target := signedTestEvent(t, 1, "deletion target")
			stored := make(chan struct{}, 1)
			_, server, ctx := startTestRelay(t, func(relay *Relay) {
				relay.OnMessage = hook.observe
				relay.OnEventPersistence = persist.observe
				relay.QueryEvents = append(relay.QueryEvents, func(context.Context, nostr.Filter) (chan *nostr.Event, error) {
					ch := make(chan *nostr.Event, 1)
					ch <- target
					close(ch)
					return ch, nil
				})
				relay.DeleteEvent = append(relay.DeleteEvent, func(context.Context, *nostr.Event) error { return tc.deleteError })
				relay.StoreEvent = append(relay.StoreEvent, func(context.Context, *nostr.Event) (storage.WriteOutcome, error) {
					stored <- struct{}{}
					return storage.WriteStored, nil
				})
				if tc.reject {
					relay.OverwriteDeletionOutcome = append(relay.OverwriteDeletionOutcome, func(context.Context, *nostr.Event, *nostr.Event) (bool, string) {
						return false, "deletion prohibited"
					})
				}
			})
			conn := dialTestRelay(t, ctx, server)
			event := nostr.Event{Kind: 5, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"e", target.ID}}}
			if err := event.Sign(strings.Repeat("1", 64)); err != nil {
				t.Fatal(err)
			}
			publishAndExpectOK(t, ctx, conn, &event, false)
			if got := hook.next(t).wait(t); got != tc.message {
				t.Errorf("message outcome = %q, want %q", got, tc.message)
			}
			if got := persist.next(t).Outcome; got != tc.persistence {
				t.Errorf("persistence outcome = %q, want %q", got, tc.persistence)
			}
			select {
			case <-stored:
				t.Error("deletion event was stored after deletion failed")
			default:
			}
		})
	}
}

func TestReadFailuresAreNotReportedAsSuccessfulResponses(t *testing.T) {
	cases := []struct {
		name     string
		request  string
		terminal string
	}{
		{name: "query", request: `["REQ","sub",{"kinds":[1]}]`, terminal: "EOSE"},
		{name: "count", request: `["COUNT","sub",{"kinds":[1]}]`, terminal: "COUNT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hook := newMessageHook()
			_, server, ctx := startTestRelay(t, func(relay *Relay) {
				relay.OnMessage = hook.observe
				relay.QueryEvents = append(relay.QueryEvents, func(context.Context, nostr.Filter) (chan *nostr.Event, error) {
					return nil, errors.New("query failed")
				})
				relay.CountEvents = append(relay.CountEvents, func(context.Context, nostr.Filter) (int64, error) {
					return 0, errors.New("count failed")
				})
			})
			conn := dialTestRelay(t, ctx, server)
			writeTestMessage(t, ctx, conn, tc.request)
			for _, want := range []string{"NOTICE", tc.terminal} {
				var fields []json.RawMessage
				if err := json.Unmarshal([]byte(readTestMessage(t, ctx, conn)), &fields); err != nil {
					t.Fatal(err)
				}
				if len(fields) == 0 {
					t.Fatal("empty response envelope")
				}
				var label string
				if err := json.Unmarshal(fields[0], &label); err != nil {
					t.Fatal(err)
				}
				if label != want {
					t.Fatalf("response = %q, want %q", label, want)
				}
			}
			if got := hook.next(t).wait(t); got != "error" {
				t.Errorf("message outcome = %q, want error", got)
			}
		})
	}
}

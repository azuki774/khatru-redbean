package relay

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/fiatjaf/eventstore"
	"github.com/fiatjaf/eventstore/postgresql"
	"github.com/nbd-wtf/go-nostr"
)

func TestIsDatabaseFailure(t *testing.T) {
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{name: "nil error", ctx: context.Background(), err: nil, want: false},
		{name: "connection error", ctx: context.Background(), err: errors.New("dial tcp: connection refused"), want: true},
		{name: "wrapped connection error", ctx: context.Background(), err: fmt.Errorf("query failed: %w", errors.New("connection reset by peer")), want: true},
		{name: "context canceled", ctx: context.Background(), err: context.Canceled, want: false},
		{name: "context deadline exceeded", ctx: context.Background(), err: context.DeadlineExceeded, want: false},
		{name: "canceled request", ctx: canceledCtx, err: errors.New("canceling statement due to user request"), want: false},
		{name: "duplicate event", ctx: context.Background(), err: eventstore.ErrDupEvent, want: false},
		{name: "wrapped duplicate event", ctx: context.Background(), err: fmt.Errorf("save: %w", eventstore.ErrDupEvent), want: false},
		{name: "too many ids", ctx: context.Background(), err: postgresql.TooManyIDs, want: false},
		{name: "too many authors", ctx: context.Background(), err: postgresql.TooManyAuthors, want: false},
		{name: "too many kinds", ctx: context.Background(), err: postgresql.TooManyKinds, want: false},
		{name: "too many tag values", ctx: context.Background(), err: postgresql.TooManyTagValues, want: false},
		{name: "empty tag set", ctx: context.Background(), err: postgresql.EmptyTagSet, want: false},
		{name: "wrapped invalid filter", ctx: context.Background(), err: fmt.Errorf("query: %w", postgresql.TooManyIDs), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDatabaseFailure(tt.ctx, tt.err); got != tt.want {
				t.Errorf("isDatabaseFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDatabaseOperationReportsFailure(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{name: "database error", ctx: context.Background(), err: errors.New("connection refused"), want: true},
		{name: "expected duplicate", ctx: context.Background(), err: eventstore.ErrDupEvent, want: false},
		{name: "canceled request", ctx: context.Background(), err: context.Canceled, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			telemetry, _ := testRelayTelemetry(t)
			var reported bool
			var reportedOperation string
			telemetry.onDatabaseError = func(operation string, err error) {
				reported = true
				reportedOperation = operation
				if !errors.Is(err, tt.err) {
					t.Errorf("reported error = %v, want %v", err, tt.err)
				}
			}

			operation := telemetry.eventOperation("save_event", func(context.Context, *nostr.Event) error {
				return tt.err
			})
			_ = operation(tt.ctx, &nostr.Event{Kind: 1})

			if reported != tt.want {
				t.Errorf("database failure reported = %v, want %v", reported, tt.want)
			}
			if tt.want && reportedOperation != "save_event" {
				t.Errorf("reported operation = %q, want save_event", reportedOperation)
			}
		})
	}
}

func TestQueryOperationReportsFailure(t *testing.T) {
	telemetry, _ := testRelayTelemetry(t)
	var reported bool
	telemetry.onDatabaseError = func(operation string, err error) {
		reported = true
		if operation != "query_events" {
			t.Errorf("reported operation = %q, want query_events", operation)
		}
	}

	query := telemetry.queryOperation(func(context.Context, nostr.Filter) (chan *nostr.Event, error) {
		return nil, errors.New("connection refused")
	})
	if _, err := query(context.Background(), nostr.Filter{}); err == nil {
		t.Fatal("queryOperation() error = nil, want error")
	}

	if !reported {
		t.Error("database failure was not reported")
	}
}

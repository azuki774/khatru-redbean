package relay

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/azuki774/khatru-redbean/internal/nostr"
	"github.com/azuki774/khatru-redbean/internal/storage"
	"github.com/azuki774/khatru-redbean/internal/storage/postgres"
	"go.opentelemetry.io/otel/codes"
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
		{name: "too many ids", ctx: context.Background(), err: postgres.ErrTooManyIDs, want: false},
		{name: "too many authors", ctx: context.Background(), err: postgres.ErrTooManyAuthors, want: false},
		{name: "too many kinds", ctx: context.Background(), err: postgres.ErrTooManyKinds, want: false},
		{name: "too many tag values", ctx: context.Background(), err: postgres.ErrTooManyTagValues, want: false},
		{name: "empty tag set", ctx: context.Background(), err: postgres.ErrEmptyTagSet, want: false},
		{name: "wrapped invalid filter", ctx: context.Background(), err: fmt.Errorf("query: %w", postgres.ErrTooManyIDs), want: false},
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

			operation := telemetry.eventOperation("delete_event", func(context.Context, *nostr.Event) error {
				return tt.err
			})
			_ = operation(tt.ctx, &nostr.Event{Kind: 1})

			if reported != tt.want {
				t.Errorf("database failure reported = %v, want %v", reported, tt.want)
			}
			if tt.want && reportedOperation != "delete_event" {
				t.Errorf("reported operation = %q, want delete_event", reportedOperation)
			}
		})
	}
}

func TestWriteOperationReportsActualOutcome(t *testing.T) {
	tests := []struct {
		name    string
		outcome storage.WriteOutcome
	}{
		{name: "stored", outcome: storage.WriteStored},
		{name: "duplicate", outcome: storage.WriteDuplicate},
		{name: "superseded", outcome: storage.WriteSuperseded},
		{name: "not stored", outcome: storage.WriteNotStored},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			telemetry, recorder, reader := newTestTelemetry(t)
			var reported bool
			telemetry.onDatabaseError = func(string, error) { reported = true }

			operation := telemetry.writeOperation("save_event", func(context.Context, *nostr.Event) (storage.WriteOutcome, error) {
				return tt.outcome, nil
			})

			outcome, err := operation(context.Background(), &nostr.Event{Kind: 1})
			if err != nil {
				t.Fatalf("writeOperation() error = %v, want nil", err)
			}
			if outcome != tt.outcome {
				t.Errorf("writeOperation() outcome = %q, want %q", outcome, tt.outcome)
			}
			if reported {
				t.Error("write outcome without error was reported as a database failure")
			}

			spans := recorder.Ended()
			if len(spans) != 1 {
				t.Fatalf("ended spans = %d, want 1", len(spans))
			}
			if spans[0].Status().Code == codes.Error {
				t.Error("write outcome without error recorded an error status")
			}
			if value, ok := attributeValue(spans[0], "nostr.persistence.outcome"); !ok || value.AsString() != string(tt.outcome) {
				t.Errorf("nostr.persistence.outcome = %v (present=%v), want %q", value.AsString(), ok, tt.outcome)
			}

			rm := collectMetrics(t, reader)
			if count, ok := sumInt64(rm, "nostr.relay.db.operations"); !ok || count != 1 {
				t.Errorf("nostr.relay.db.operations = %d (present=%v), want 1", count, ok)
			}
			if count, ok := histogramCount(rm, "db.client.operation.duration"); !ok || count != 1 {
				t.Errorf("db.client.operation.duration count = %d (present=%v), want 1", count, ok)
			}
		})
	}
}

func TestWriteOperationPreservesStorageError(t *testing.T) {
	storageErr := errors.New("connection refused")
	telemetry, recorder := testRelayTelemetry(t)
	var reported bool
	telemetry.onDatabaseError = func(operation string, err error) {
		reported = true
		if operation != "replace_event" {
			t.Errorf("reported operation = %q, want replace_event", operation)
		}
		if !errors.Is(err, storageErr) {
			t.Errorf("reported error = %v, want %v", err, storageErr)
		}
	}

	operation := telemetry.writeOperation("replace_event", func(context.Context, *nostr.Event) (storage.WriteOutcome, error) {
		return storage.WriteStored, storageErr
	})
	outcome, err := operation(context.Background(), &nostr.Event{Kind: 30001})
	if !errors.Is(err, storageErr) {
		t.Fatalf("writeOperation() error = %v, want %v", err, storageErr)
	}
	if outcome != storage.WriteStored {
		t.Errorf("writeOperation() outcome = %q, want %q", outcome, storage.WriteStored)
	}
	if !reported {
		t.Error("storage error was not reported as a database failure")
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if spans[0].Status().Code != codes.Error {
		t.Error("failed write did not record an error status")
	}
	if _, ok := attributeValue(spans[0], "nostr.persistence.outcome"); ok {
		t.Error("failed write recorded a persistence outcome")
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

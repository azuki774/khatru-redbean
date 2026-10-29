package relay

import (
	"context"
	"errors"

	"github.com/fiatjaf/eventstore"
	"github.com/fiatjaf/eventstore/postgresql"
)

type databaseFailure struct {
	operation string
	err       error
}

// isDatabaseFailure reports whether err means the application can no longer
// access the database. Expected request errors, such as client cancellation,
// duplicate events and invalid filters, are not database failures.
func isDatabaseFailure(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, eventstore.ErrDupEvent) {
		return false
	}

	expected := []error{
		postgresql.TooManyIDs,
		postgresql.TooManyAuthors,
		postgresql.TooManyKinds,
		postgresql.TooManyTagValues,
		postgresql.EmptyTagSet,
	}
	for _, target := range expected {
		if errors.Is(err, target) {
			return false
		}
	}

	return true
}

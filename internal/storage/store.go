package storage

import (
	"context"

	"github.com/azuki774/khatru-redbean/internal/nostr"
)

// WriteOutcome describes the effect of a completed write against a store.
// A nonnil error is never a write outcome: outcomes describe writes that
// completed, and callers must not treat duplicate or superseded as failures.
type WriteOutcome string

const (
	// WriteStored means a new row was inserted, including a successful replacement.
	WriteStored WriteOutcome = "stored"
	// WriteDuplicate means no row was inserted because this event ID already exists.
	WriteDuplicate WriteOutcome = "duplicate"
	// WriteSuperseded means a more recent or equivalent replaceable or addressable event wins.
	WriteSuperseded WriteOutcome = "superseded"
	// WriteNotStored means no configured store wrote an event.
	WriteNotStored WriteOutcome = "not_stored"
)

// Store is a persistence layer for nostr events handled by a relay.
type Store interface {
	// Init is called at the very beginning by [Server.Start], after [Relay.Init],
	// allowing a storage to initialize its internal resources.
	Init() error

	// Close must be called after you're done using the store, to free up resources and so on.
	Close()

	// QueryEvents should return a channel with the events as they're recovered from a database.
	//   the channel should be closed after the events are all delivered.
	QueryEvents(context.Context, nostr.Filter) (chan *nostr.Event, error)
	// DeleteEvent just deletes an event, no side-effects.
	DeleteEvent(context.Context, *nostr.Event) error
	// SaveEvent just saves an event, no side-effects. It reports whether a row
	// was inserted instead of returning an error for an existing event.
	SaveEvent(context.Context, *nostr.Event) (WriteOutcome, error)
	// ReplaceEvent replaces a replaceable or addressable event when the new
	// event is more recent or equivalent. Conceptually it is like a
	// Query->Delete->Save, but streamlined. It reports whether a row was
	// written, already existed, or was superseded.
	ReplaceEvent(context.Context, *nostr.Event) (WriteOutcome, error)
}

type Counter interface {
	CountEvents(context.Context, nostr.Filter) (int64, error)
}

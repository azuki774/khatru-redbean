package engine

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/azuki774/khatru-redbean/internal/storage"
)

// MessageResult is the terminal state of one websocket message. Outcome uses
// the fixed protocol vocabulary (ok, blocked, error, auth-required, invalid,
// mute, unsupported) plus write_error when the terminal response could not be
// written and canceled when storage processing was canceled. FinishedAt is
// captured once at terminal-write completion and is shared by every observer
// of this message.
type MessageResult struct {
	Outcome    string
	FinishedAt time.Time
}

// PersistenceResult is the terminal state of the storage pipeline for one
// EVENT message. Outcome is one of the storage.WriteOutcome values (stored,
// duplicate, superseded, not_stored) or not_attempted, ephemeral, error or
// canceled. FinishedAt is captured once when ingestion completed, before any
// broadcast or response.
type PersistenceResult struct {
	Outcome    string
	FinishedAt time.Time
}

const (
	persistenceNotAttempted = "not_attempted"
	persistenceEphemeral    = "ephemeral"
	persistenceError        = "error"
	persistenceCanceled     = "canceled"

	messageOutcomeWriteError = "write_error"
	messageOutcomeCanceled   = "canceled"
)

// persistenceOutcomeForError classifies a storage pipeline failure. A canceled
// context is distinct from a failed write; both are errors, never outcomes.
func persistenceOutcomeForError(err error) string {
	if errors.Is(err, context.Canceled) {
		return persistenceCanceled
	}
	return persistenceError
}

// combineWriteOutcomes folds the results of several configured stores into a
// single persistence outcome. Evidence of an actual save is never lost because
// a later store reported a no-op, and superseded (a real decision about a newer
// event) is preferred over duplicate when nothing was written.
func combineWriteOutcomes(accum, next storage.WriteOutcome) storage.WriteOutcome {
	if next == "" {
		next = storage.WriteNotStored
	}
	if accum == "" {
		return next
	}
	if accum == storage.WriteStored || next == storage.WriteStored {
		return storage.WriteStored
	}
	if accum == storage.WriteSuperseded || next == storage.WriteSuperseded {
		return storage.WriteSuperseded
	}
	if accum == storage.WriteDuplicate || next == storage.WriteDuplicate {
		return storage.WriteDuplicate
	}
	return storage.WriteNotStored
}

// messageObservation is the small, synchronized per-message state needed
// because REQ initial results are delivered from worker goroutines while the
// message outcome is decided by the handler goroutine. It is created per
// message and never shared between messages.
type messageObservation struct {
	mu            sync.Mutex
	writeFailed   bool
	internalError bool
	finished      bool
}

// markWriteFailure records that a response write for this message failed.
func (m *messageObservation) markWriteFailure() {
	m.mu.Lock()
	if !m.finished {
		m.writeFailed = true
	}
	m.mu.Unlock()
}

// markInternalError records that a storage query or count failed while
// answering this message.
func (m *messageObservation) markInternalError() {
	m.mu.Lock()
	if !m.finished {
		m.internalError = true
	}
	m.mu.Unlock()
}

// result returns the terminal MessageResult, folding in any write or query
// failures. A message whose response could not be written is never reported ok.
func (m *messageObservation) result(base string) MessageResult {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.finished = true
	if base == "" {
		base = "ok"
	}
	if base == "ok" {
		switch {
		case m.writeFailed:
			base = messageOutcomeWriteError
		case m.internalError:
			base = persistenceError
		}
	}
	return MessageResult{Outcome: base, FinishedAt: time.Now()}
}

func withMessageObservation(ctx context.Context, obs *messageObservation) context.Context {
	return context.WithValue(ctx, messageObservationKey, obs)
}

func messageObservationFrom(ctx context.Context) *messageObservation {
	obs, _ := ctx.Value(messageObservationKey).(*messageObservation)
	return obs
}

// writeResponse writes a message-scoped response and records a write failure on
// the message observation carried by ctx, so the message is not reported ok
// when the client never received the response.
func writeResponse(ctx context.Context, ws *WebSocket, payload any) error {
	err := ws.WriteJSON(payload)
	if err != nil {
		if obs := messageObservationFrom(ctx); obs != nil {
			obs.markWriteFailure()
		}
	}
	return err
}

// markQueryFailure records a storage query or count failure on the message
// carried by ctx, so an unanswered REQ or COUNT is not reported as ok.
func markQueryFailure(ctx context.Context) {
	if obs := messageObservationFrom(ctx); obs != nil {
		obs.markInternalError()
	}
}

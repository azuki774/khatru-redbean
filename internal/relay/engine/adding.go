package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/azuki774/khatru-redbean/internal/nostr"
	"github.com/azuki774/khatru-redbean/internal/storage"
)

// AddEvent sends an event through then normal add pipeline, as if it was received from a websocket.
func (rl *Relay) AddEvent(ctx context.Context, evt *nostr.Event) (skipBroadcast bool, writeError error) {
	if evt == nil {
		return false, errors.New("error: event is nil")
	}

	if nostr.IsEphemeralKind(evt.Kind) {
		return false, rl.handleEphemeral(ctx, evt)
	}

	_, skipBroadcast, writeError = rl.handleNormal(ctx, evt)
	return skipBroadcast, writeError
}

// handleNormal stores a regular or replaceable event. It returns the persistence
// outcome (stored, duplicate, superseded, not_stored, not_attempted, error or
// canceled), whether broadcasting must be skipped, and the write error, if any.
func (rl *Relay) handleNormal(ctx context.Context, evt *nostr.Event) (persistence string, skipBroadcast bool, writeError error) {
	for _, reject := range rl.RejectEvent {
		if reject, msg := reject(ctx, evt); reject {
			if msg == "" {
				return persistenceNotAttempted, true, errors.New("blocked: no reason")
			} else {
				return persistenceNotAttempted, true, errors.New(nostr.NormalizeOKMessage(msg, "blocked"))
			}
		}
	}

	// Check to see if the event has been deleted by ID
	for _, query := range rl.QueryEvents {
		checkCtx, cancelCheck := context.WithCancel(ctx)
		ch, err := query(checkCtx, nostr.Filter{
			Kinds: []int{5},
			Tags:  nostr.TagMap{"#e": []string{evt.ID}},
		})
		if err != nil {
			cancelCheck()
			continue
		}
		target := <-ch
		// we only need the first event; stop the storage scan right away
		cancelCheck()
		if target == nil {
			continue
		}

		return persistenceNotAttempted, true, errors.New("blocked: this event has been deleted")
	}

	// will store
	// regular kinds are just saved directly
	if nostr.IsRegularKind(evt.Kind) {
		outcome, err := rl.storeEvents(ctx, evt)
		if err != nil {
			return persistenceOutcomeForError(err), false, fmt.Errorf("%s", nostr.NormalizeOKMessage(err.Error(), "error"))
		}
		if outcome == storage.WriteDuplicate {
			return string(outcome), true, nil
		}
		rl.expirationManager.trackEvent(evt)
		return string(outcome), false, nil
	}

	// Check to see if the event has been deleted by address
	for _, query := range rl.QueryEvents {
		dTagValue := ""
		for _, tag := range evt.Tags {
			if len(tag) > 0 && tag[0] == "d" {
				dTagValue = tag[1]
				break
			}
		}

		address := fmt.Sprintf("%d:%s:%s", evt.Kind, evt.PubKey, dTagValue)
		checkCtx, cancelCheck := context.WithCancel(ctx)
		ch, err := query(checkCtx, nostr.Filter{
			Kinds: []int{5},
			Since: &evt.CreatedAt,
			Tags:  nostr.TagMap{"#a": []string{address}},
		})
		if err != nil {
			cancelCheck()
			continue
		}
		target := <-ch
		// we only need the first event; stop the storage scan right away
		cancelCheck()
		if target == nil {
			continue
		}

		return persistenceNotAttempted, true, errors.New("blocked: this event has been deleted")
	}

	// otherwise it's a replaceable -- so we'll use the replacer functions if we have any
	if len(rl.ReplaceEvent) > 0 {
		outcome, err := rl.replaceEvents(ctx, evt)
		if err != nil {
			return persistenceOutcomeForError(err), false, fmt.Errorf("%s", nostr.NormalizeOKMessage(err.Error(), "error"))
		}
		if outcome == storage.WriteDuplicate {
			return string(outcome), true, nil
		}
		rl.expirationManager.trackEvent(evt)
		return string(outcome), false, nil
	}

	// otherwise do it the manual way
	filter := nostr.Filter{Limit: 1, Kinds: []int{evt.Kind}, Authors: []string{evt.PubKey}}
	if nostr.IsAddressableKind(evt.Kind) {
		// when addressable, add the "d" tag to the filter
		filter.Tags = nostr.TagMap{"d": []string{evt.Tags.GetD()}}
	}

	// now we fetch old events and delete them
	shouldStore := true
	for _, query := range rl.QueryEvents {
		ch, err := query(ctx, filter)
		if err != nil {
			continue
		}
		for previous := range ch {
			if isOlder(previous, evt) {
				for _, del := range rl.DeleteEvent {
					del(ctx, previous)
				}
			} else {
				// we found a more recent event, so we won't delete it and also will not store this new one
				shouldStore = false
			}
		}
	}

	if !shouldStore {
		rl.expirationManager.trackEvent(evt)
		return string(storage.WriteSuperseded), false, nil
	}

	// store
	outcome, err := rl.storeEvents(ctx, evt)
	if err != nil {
		return persistenceOutcomeForError(err), false, fmt.Errorf("%s", nostr.NormalizeOKMessage(err.Error(), "error"))
	}
	if outcome == storage.WriteDuplicate {
		return string(outcome), true, nil
	}
	rl.expirationManager.trackEvent(evt)
	return string(outcome), false, nil
}

// storeEvents runs every configured store and folds their outcomes. A failure
// is returned as an error, never as a benign outcome.
func (rl *Relay) storeEvents(ctx context.Context, evt *nostr.Event) (storage.WriteOutcome, error) {
	var outcome storage.WriteOutcome
	for _, store := range rl.StoreEvent {
		next, err := store(ctx, evt)
		if err != nil {
			return "", err
		}
		outcome = combineWriteOutcomes(outcome, next)
	}
	if outcome == "" {
		return storage.WriteNotStored, nil
	}
	return outcome, nil
}

// replaceEvents runs every configured replacer and folds their outcomes.
func (rl *Relay) replaceEvents(ctx context.Context, evt *nostr.Event) (storage.WriteOutcome, error) {
	var outcome storage.WriteOutcome
	for _, repl := range rl.ReplaceEvent {
		next, err := repl(ctx, evt)
		if err != nil {
			return "", err
		}
		outcome = combineWriteOutcomes(outcome, next)
	}
	if outcome == "" {
		return storage.WriteNotStored, nil
	}
	return outcome, nil
}

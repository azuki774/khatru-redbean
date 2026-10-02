package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/coder/websocket"
	"github.com/nbd-wtf/go-nostr"
)

var ErrRejected = errors.New("relay rejected subscription")

type ProtocolError struct{ message string }

func (e *ProtocolError) Error() string { return e.message }
func protocol(msg string) error        { return &ProtocolError{message: msg} }

func Probe(ctx context.Context, relay string, now time.Time) (ProbeResult, error) {
	c, _, err := websocket.Dial(ctx, relay, nil)
	if err != nil {
		return ProbeResult{}, err
	}
	defer c.CloseNow()
	now = now.Truncate(time.Second)
	const id = "monitor"
	request, _ := json.Marshal([]any{"REQ", id, map[string]any{"until": now.Unix(), "limit": 1}})
	if err := c.Write(ctx, websocket.MessageText, request); err != nil {
		return ProbeResult{}, err
	}
	var candidate *nostr.Event
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return ProbeResult{}, err
		}
		var raw []json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil || len(raw) < 2 {
			return ProbeResult{}, protocol("invalid relay message")
		}
		var typ string
		if json.Unmarshal(raw[0], &typ) != nil {
			return ProbeResult{}, protocol("invalid relay message type")
		}
		if typ == "AUTH" {
			return ProbeResult{}, ErrRejected
		}
		var sub string
		if json.Unmarshal(raw[1], &sub) != nil {
			return ProbeResult{}, protocol("invalid relay message identifier")
		}
		if sub != id {
			continue
		}
		switch typ {
		case "EVENT":
			if len(raw) < 3 {
				return ProbeResult{}, protocol("invalid EVENT")
			}
			var ev nostr.Event
			if err := json.Unmarshal(raw[2], &ev); err != nil {
				return ProbeResult{}, protocol("invalid event")
			}
			ok, err := ev.CheckSignature()
			if err != nil || !ok {
				return ProbeResult{}, protocol("invalid event signature")
			}
			if ev.GetID() != ev.ID {
				return ProbeResult{}, protocol("invalid event ID")
			}
			if time.Unix(int64(ev.CreatedAt), 0).After(now) {
				return ProbeResult{}, protocol("event timestamp is in the future")
			}
			if candidate == nil || ev.CreatedAt > candidate.CreatedAt {
				candidate = &ev
			}
		case "EOSE":
			if candidate == nil {
				return ProbeResult{}, nil
			}
			latest := time.Unix(int64(candidate.CreatedAt), 0)
			return ProbeResult{LatestAt: &latest, Age: now.Sub(latest), HasEvent: true}, nil
		case "CLOSED":
			return ProbeResult{}, ErrRejected
		case "NOTICE": // Do not retain or log relay-provided text.
		case "OK":
			return ProbeResult{}, protocol("unexpected relay response")
		default:
			return ProbeResult{}, protocol("unexpected relay message")
		}
	}
}

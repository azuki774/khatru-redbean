package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nbd-wtf/go-nostr"
)

func config() Config {
	return Config{Relay: "wss://relay.example", Interval: time.Hour, MaxAge: time.Hour, Timeout: time.Second, Retries: 2, RetryInterval: 0, RemindInterval: time.Hour}
}

func TestCheckRetriesTransportAndClearsErrorAfterSuccess(t *testing.T) {
	calls := 0
	m := Monitor{Config: config(), Probe: func(context.Context, string, time.Time) (ProbeResult, error) {
		calls++
		if calls == 1 {
			return ProbeResult{}, errors.New("private transport detail")
		}
		return ProbeResult{HasEvent: true, Age: time.Minute}, nil
	}}
	r := m.Check(context.Background())
	if r.Status != StatusHealthy || r.Error != "" || r.Attempts != 2 || calls != 2 || r.Relay != m.Config.Relay || r.CheckedAt.IsZero() {
		t.Fatalf("result=%+v calls=%d", r, calls)
	}
}
func TestCheckStaleAndEmptyAreNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name   string
		p      ProbeResult
		status Status
	}{{"stale", ProbeResult{HasEvent: true, Age: time.Hour + time.Second}, StatusStale}, {"empty", ProbeResult{}, StatusEmpty}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			m := Monitor{Config: config(), Probe: func(context.Context, string, time.Time) (ProbeResult, error) { calls++; return tc.p, nil }}
			r := m.Check(context.Background())
			if calls != 1 || r.Attempts != 1 || r.Status != tc.status {
				t.Fatalf("calls=%d result=%+v", calls, r)
			}
		})
	}
}
func TestClassificationRetryPolicy(t *testing.T) {
	tests := []struct {
		err   error
		want  Status
		retry bool
	}{{errors.New("dial"), StatusConnectError, true}, {context.DeadlineExceeded, StatusTimeout, true}, {protocol("bad frame"), StatusProtocolError, true}, {ErrRejected, StatusRejected, false}}
	for _, tc := range tests {
		got := classify(tc.err, nil)
		if got != tc.want || retryable(got) != tc.retry {
			t.Errorf("classify(%v)=%s retry %v", tc.err, got, retryable(got))
		}
	}
}
func TestConfigValidation(t *testing.T) {
	c := config()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Relay = "http://relay"
	if c.Validate() == nil {
		t.Fatal("accepted invalid URL")
	}
}

func TestRunAlertTransitionsAndFailedDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seq := []Status{StatusHealthy, StatusStale, StatusStale, StatusRejected, StatusHealthy}
	var mu sync.Mutex
	var alerts []Alert
	probeN := 0
	m := Monitor{Config: config(), Probe: func(context.Context, string, time.Time) (ProbeResult, error) {
		mu.Lock()
		defer mu.Unlock()
		s := seq[probeN]
		probeN++
		switch s {
		case StatusHealthy:
			return ProbeResult{HasEvent: true, Age: time.Second}, nil
		case StatusStale:
			return ProbeResult{HasEvent: true, Age: 2 * time.Hour}, nil
		case StatusRejected:
			return ProbeResult{}, ErrRejected
		default:
			return ProbeResult{}, errors.New("no detail")
		}
	}, Alert: func(_ context.Context, a Alert) error {
		mu.Lock()
		defer mu.Unlock()
		alerts = append(alerts, a)
		if a.Kind == AlertAbnormal {
			return errors.New("delivery unavailable")
		}
		if a.Kind == AlertCauseChanged {
			return errors.New("delivery unavailable")
		}
		if a.Kind == AlertRecovery {
			cancel()
		}
		return nil
	}}
	m.Config.Interval = time.Millisecond
	m.Run(ctx)
	mu.Lock()
	defer mu.Unlock()
	want := []AlertKind{AlertAbnormal, AlertAbnormal, AlertCauseChanged, AlertRecovery}
	if len(alerts) != len(want) {
		t.Fatalf("alerts=%+v", alerts)
	}
	for i, k := range want {
		if alerts[i].Kind != k {
			t.Errorf("alert[%d]=%s want %s", i, alerts[i].Kind, k)
		}
	}
	if !alerts[0].Since.Equal(alerts[1].Since) || !alerts[0].Since.Equal(alerts[2].Since) || !alerts[0].Since.Equal(alerts[3].Since) {
		t.Errorf("incident timestamp not preserved: %+v", alerts)
	}
}
func TestRunNoStartupHealthyAlert(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	alerts := 0
	m := Monitor{Config: config(), Probe: func(context.Context, string, time.Time) (ProbeResult, error) {
		cancel()
		return ProbeResult{HasEvent: true}, nil
	}, Alert: func(context.Context, Alert) error { alerts++; return nil }}
	m.Run(ctx)
	if alerts != 0 {
		t.Fatalf("startup healthy alerts=%d", alerts)
	}
}

func TestRunSchedulesFromCheckStartAndSkipsMissedSlots(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var starts []time.Time
	m := Monitor{Config: config(), Probe: func(ctx context.Context, _ string, _ time.Time) (ProbeResult, error) {
		mu.Lock()
		starts = append(starts, time.Now())
		n := len(starts)
		mu.Unlock()
		if n == 3 {
			cancel()
		} else {
			time.Sleep(35 * time.Millisecond)
		}
		return ProbeResult{HasEvent: true, Age: time.Second}, nil
	}}
	m.Config.Interval = 15 * time.Millisecond
	m.Run(ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 3 {
		t.Fatalf("checks=%d, want 3", len(starts))
	}
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 35*time.Millisecond {
			t.Errorf("checks overlap (%s)", gap)
		}
	}
}

func TestSkipMissedSlots(t *testing.T) {
	start := time.Unix(1000, 0)
	for _, tc := range []struct{ elapsed, want time.Duration }{
		{5 * time.Minute, time.Hour},
		{time.Hour, 2 * time.Hour},
		{150 * time.Minute, 3 * time.Hour},
	} {
		got := skipMissedSlots(start.Add(time.Hour), start.Add(tc.elapsed), time.Hour)
		if !got.Equal(start.Add(tc.want)) {
			t.Fatalf("elapsed %s: next=%v, want %v", tc.elapsed, got, start.Add(tc.want))
		}
	}
}

func TestProbeFreshnessBoundaryAndMissingEOSE(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	event := signedEvent(t, now.Add(-time.Hour))
	for _, eose := range []bool{true, false} {
		t.Run(fmt.Sprint(eose), func(t *testing.T) {
			relay := serveProbe(t, func(c *websocket.Conn, ctx context.Context) {
				readRequest(t, c, ctx)
				writeFrame(c, ctx, []any{"EVENT", "monitor", event})
				if eose {
					writeFrame(c, ctx, []any{"EOSE", "monitor"})
				} else {
					_, _, _ = c.Read(ctx)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r, err := Probe(ctx, relay, now.Add(500*time.Millisecond))
			if eose {
				if err != nil || !r.HasEvent || r.Age != time.Hour {
					t.Fatalf("boundary result=%+v err=%v", r, err)
				}
				m := Monitor{Config: config(), Probe: func(context.Context, string, time.Time) (ProbeResult, error) { return r, nil }}
				if got := m.Check(ctx); got.Status != StatusHealthy {
					t.Fatalf("boundary status=%s", got.Status)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("event without EOSE must time out: %v", err)
			}
		})
	}
}

func TestRunRetriesFailedRecoveryAndRetainsIncidentSince(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	var alerts []Alert
	m := Monitor{Config: config(), Probe: func(context.Context, string, time.Time) (ProbeResult, error) {
		calls++
		if calls == 1 {
			return ProbeResult{}, ErrRejected
		}
		return ProbeResult{HasEvent: true}, nil
	}, Alert: func(_ context.Context, a Alert) error {
		alerts = append(alerts, a)
		if a.Kind == AlertRecovery && len(alerts) == 2 {
			return errors.New("delivery failed")
		}
		if a.Kind == AlertRecovery && len(alerts) == 3 {
			cancel()
		}
		return nil
	}}
	m.Config.Interval = time.Millisecond
	m.Run(ctx)
	if len(alerts) != 3 || alerts[0].Kind != AlertAbnormal || alerts[1].Kind != AlertRecovery || alerts[2].Kind != AlertRecovery {
		t.Fatalf("alerts=%+v", alerts)
	}
	if !alerts[0].Since.Equal(alerts[1].Since) || !alerts[0].Since.Equal(alerts[2].Since) {
		t.Fatalf("recovery retries did not retain incident time: %+v", alerts)
	}
}
func TestRunAlertContextBoundedAndCancelNoShutdownAlert(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := Monitor{Config: config(), Probe: func(context.Context, string, time.Time) (ProbeResult, error) {
		cancel()
		return ProbeResult{}, errors.New("offline")
	}, Alert: func(context.Context, Alert) error { t.Fatal("alert after cancellation"); return nil }}
	m.Run(ctx)
}

func TestAlertCallbackReceivesDeadline(t *testing.T) {
	called := false
	if !sendAlert(context.Background(), func(ctx context.Context, _ Alert) error {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Errorf("alert context has no bounded deadline")
		}
		return nil
	}, Alert{}) || !called {
		t.Fatal("alert callback not invoked")
	}
}

func serveProbe(t *testing.T, handler func(*websocket.Conn, context.Context)) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		handler(c, r.Context())
	}))
	t.Cleanup(s.Close)
	return "ws" + strings.TrimPrefix(s.URL, "http")
}
func readRequest(t *testing.T, c *websocket.Conn, ctx context.Context) {
	t.Helper()
	_, _, e := c.Read(ctx)
	if e != nil {
		t.Errorf("read REQ: %v", e)
	}
}
func writeFrame(c *websocket.Conn, ctx context.Context, v any) {
	b, _ := json.Marshal(v)
	_ = c.Write(ctx, websocket.MessageText, b)
}
func signedEvent(t *testing.T, at time.Time) nostr.Event {
	t.Helper()
	e := nostr.Event{CreatedAt: nostr.Timestamp(at.Unix()), Kind: 1, Tags: nostr.Tags{}, Content: "probe"}
	if err := e.Sign(nostr.GeneratePrivateKey()); err != nil {
		t.Fatal(err)
	}
	return e
}
func TestProbeEventEOSEAndLatestCandidate(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	older := signedEvent(t, now.Add(-time.Minute))
	newer := signedEvent(t, now.Add(-time.Second))
	relay := serveProbe(t, func(c *websocket.Conn, ctx context.Context) {
		_, req, err := c.Read(ctx)
		if err != nil {
			t.Errorf("read REQ: %v", err)
			return
		}
		var frame []json.RawMessage
		if err := json.Unmarshal(req, &frame); err != nil || len(frame) != 3 {
			t.Errorf("invalid REQ frame: %s", req)
			return
		}
		var typ, sub string
		var filter struct {
			Until int64 `json:"until"`
			Limit int   `json:"limit"`
		}
		if json.Unmarshal(frame[0], &typ) != nil || json.Unmarshal(frame[1], &sub) != nil || json.Unmarshal(frame[2], &filter) != nil || typ != "REQ" || sub != "monitor" || filter.Until != now.Unix() || filter.Limit != 1 {
			t.Errorf("unexpected REQ: %s", req)
			return
		}
		writeFrame(c, ctx, []any{"EVENT", "monitor", newer})
		writeFrame(c, ctx, []any{"EVENT", "monitor", older})
		writeFrame(c, ctx, []any{"EOSE", "monitor"})
	})
	r, e := Probe(context.Background(), relay, now)
	if e != nil || r.LatestAt == nil || !r.LatestAt.Equal(time.Unix(int64(newer.CreatedAt), 0)) {
		t.Fatalf("result=%+v err=%v", r, e)
	}
}
func TestProbeRejectsBadSignatureAndFuture(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for _, tc := range []struct {
		name  string
		event nostr.Event
	}{{"signature", func() nostr.Event { e := signedEvent(t, now); e.Content = "tampered"; return e }()}, {"ID", func() nostr.Event { e := signedEvent(t, now); e.ID = strings.Repeat("0", 64); return e }()}, {"future", signedEvent(t, now.Add(time.Minute))}} {
		t.Run(tc.name, func(t *testing.T) {
			relay := serveProbe(t, func(c *websocket.Conn, ctx context.Context) {
				readRequest(t, c, ctx)
				writeFrame(c, ctx, []any{"EVENT", "monitor", tc.event})
			})
			_, err := Probe(context.Background(), relay, now)
			var pe *ProtocolError
			if !errors.As(err, &pe) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
func TestProbeEOSERequiredAndAUTHClosedRejected(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame any
		want  error
	}{{"auth", []any{"AUTH", "challenge body"}, ErrRejected}, {"closed", []any{"CLOSED", "monitor", "secret reason"}, ErrRejected}, {"missing EOSE", nil, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			relay := serveProbe(t, func(c *websocket.Conn, ctx context.Context) {
				readRequest(t, c, ctx)
				if tc.frame != nil {
					writeFrame(c, ctx, tc.frame)
					return
				}
				<-ctx.Done()
			})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			_, err := Probe(ctx, relay, time.Now())
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want %v", err, tc.want)
			}
			if tc.want == nil && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected timeout, got %v", err)
			}
		})
	}
}
func TestProbeImmediateEOSEEmptyAndIgnoresOtherSubscription(t *testing.T) {
	relay := serveProbe(t, func(c *websocket.Conn, ctx context.Context) {
		readRequest(t, c, ctx)
		writeFrame(c, ctx, []any{"EVENT", "other", signedEvent(t, time.Now())})
		writeFrame(c, ctx, []any{"EOSE", "monitor"})
	})
	r, e := Probe(context.Background(), relay, time.Now())
	if e != nil || r.HasEvent {
		t.Fatalf("result=%+v err=%v", r, e)
	}
}

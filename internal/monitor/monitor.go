package monitor

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"go.uber.org/zap"
)

type Config struct {
	Relay                                                    string
	Interval, MaxAge, Timeout, RetryInterval, RemindInterval time.Duration
	Retries                                                  int
}

func (c Config) Validate() error {
	u, err := url.Parse(c.Relay)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		return fmt.Errorf("relay must be a valid ws:// or wss:// URL")
	}
	if c.Interval <= 0 || c.MaxAge <= 0 || c.Timeout <= 0 || c.RetryInterval < 0 || c.RemindInterval <= 0 || c.Retries < 0 {
		return fmt.Errorf("invalid monitor durations or retry count")
	}
	return nil
}

type AlertKind string

const (
	AlertAbnormal     AlertKind = "abnormal"
	AlertCauseChanged AlertKind = "cause_changed"
	AlertReminder     AlertKind = "reminder"
	AlertRecovery     AlertKind = "recovery"
)

type Alert struct {
	Kind   AlertKind
	Result Result
	Since  time.Time
}
type AlertFunc func(context.Context, Alert) error
type Status string

const (
	StatusHealthy       Status = "healthy"
	StatusStale         Status = "stale"
	StatusEmpty         Status = "empty"
	StatusConnectError  Status = "connect_error"
	StatusTimeout       Status = "timeout"
	StatusRejected      Status = "rejected"
	StatusProtocolError Status = "protocol_error"
)

type Result struct {
	Relay     string
	Status    Status
	CheckedAt time.Time
	LatestAt  *time.Time
	Age       time.Duration
	Attempts  int
	Duration  time.Duration
	Error     string
}
type ProbeResult struct {
	LatestAt *time.Time
	Age      time.Duration
	HasEvent bool
}
type ProbeFunc func(context.Context, string, time.Time) (ProbeResult, error)
type Monitor struct {
	Config Config
	Probe  ProbeFunc
	Alert  AlertFunc
}

func (m *Monitor) Check(ctx context.Context) Result {
	start := time.Now()
	r := Result{Relay: m.Config.Relay, Status: StatusConnectError, CheckedAt: start}
	probe := m.Probe
	if probe == nil {
		probe = Probe
	}
	for i := 1; i <= m.Config.Retries+1; i++ {
		r.Attempts = i
		// A prior attempt's failure is no longer relevant once a retry succeeds.
		r.Error = ""
		at := time.Now()
		ac, cancel := context.WithTimeout(ctx, m.Config.Timeout)
		pr, err := probe(ac, m.Config.Relay, at)
		cancel()
		if err == nil {
			if pr.LatestAt != nil {
				v := *pr.LatestAt
				r.LatestAt = &v
			}
			if pr.HasEvent {
				r.Age = pr.Age
				if r.Age <= m.Config.MaxAge {
					r.Status = StatusHealthy
				} else {
					r.Status = StatusStale
					r.Error = "latest event is stale"
				}
			} else {
				r.Status = StatusEmpty
				r.Error = "relay returned no event"
			}
			break
		}
		r.Status = classify(err, ac.Err())
		r.Error = safeProbeError(r.Status)
		if !retryable(r.Status) || i > m.Config.Retries || ctx.Err() != nil {
			break
		}
		zap.S().Warnw("relay monitor retry", "relay", m.Config.Relay, "attempt", i, "next_attempt", i+1, "status", r.Status)
		if !sleep(ctx, m.Config.RetryInterval) {
			break
		}
	}
	r.Duration = time.Since(start)
	if ctx.Err() != nil {
		return r
	}
	fields := resultFields(r)
	if r.Status == StatusHealthy {
		zap.S().Infow("relay monitor check", fields...)
	} else {
		zap.S().Errorw("relay monitor check", fields...)
	}
	return r
}
func classify(err, ctxErr error) Status {
	if errors.Is(err, ErrRejected) {
		return StatusRejected
	}
	if errors.Is(ctxErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return StatusTimeout
	}
	var pe *ProtocolError
	if errors.As(err, &pe) {
		return StatusProtocolError
	}
	return StatusConnectError
}
func retryable(s Status) bool {
	return s == StatusConnectError || s == StatusTimeout || s == StatusProtocolError
}
func safeProbeError(s Status) string {
	switch s {
	case StatusRejected:
		return "relay rejected subscription"
	case StatusTimeout:
		return "probe timed out"
	case StatusProtocolError:
		return "relay protocol error"
	default:
		return "relay connection error"
	}
}
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func resultFields(r Result) []any {
	fields := []any{"relay", r.Relay, "status", r.Status, "checked_at", r.CheckedAt, "attempts", r.Attempts, "duration", r.Duration}
	if r.LatestAt != nil {
		fields = append(fields, "latest_event_at", *r.LatestAt, "event_age", r.Age)
	}
	if r.Error != "" {
		fields = append(fields, "error", r.Error)
	}
	return fields
}

func LogAlert(ctx context.Context, a Alert) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fields := append(resultFields(a.Result), "kind", a.Kind, "since", a.Since)
	if a.Kind == AlertRecovery {
		zap.S().Infow("relay monitor alert", fields...)
	} else {
		zap.S().Errorw("relay monitor alert", fields...)
	}
	return nil
}

func (m *Monitor) Run(ctx context.Context) {
	next := time.Now()
	var incidentSince time.Time
	var previous Status
	previousCause := ""
	var lastAlert time.Time
	pending := false
	pendingKind := AlertKind("")
	recoveredPending := false
	for ctx.Err() == nil {
		if d := time.Until(next); d > 0 && !sleep(ctx, d) {
			return
		}
		if ctx.Err() != nil {
			return
		}
		r := m.Check(ctx)
		if ctx.Err() != nil {
			return
		}
		next = next.Add(m.Config.Interval)
		now := time.Now()
		unhealthy := r.Status != StatusHealthy
		kind := AlertKind("")
		if unhealthy {
			if previous == StatusHealthy || previous == "" {
				incidentSince = r.CheckedAt
				pending = true
				pendingKind = AlertAbnormal
				kind = pendingKind
			} else if r.Error != previousCause {
				pending = true
				pendingKind = AlertCauseChanged
				kind = pendingKind
			} else if pending {
				kind = pendingKind
			} else if lastAlert.IsZero() || now.Sub(lastAlert) >= m.Config.RemindInterval {
				kind = AlertReminder
			}
			if kind != "" {
				if sendAlert(ctx, m.Alert, Alert{Kind: kind, Result: r, Since: incidentSince}) {
					lastAlert = now
					pending = false
					pendingKind = ""
				}
			}
			recoveredPending = true
		} else {
			if recoveredPending {
				if sendAlert(ctx, m.Alert, Alert{Kind: AlertRecovery, Result: r, Since: incidentSince}) {
					recoveredPending = false
					incidentSince = time.Time{}
				}
			}
			if !recoveredPending {
				incidentSince = time.Time{}
				lastAlert = time.Time{}
				pending = false
				pendingKind = ""
			}
		}
		previous = r.Status
		previousCause = r.Error
		next = skipMissedSlots(next, time.Now(), m.Config.Interval)
	}
}

func skipMissedSlots(next, now time.Time, interval time.Duration) time.Time {
	if !next.After(now) {
		next = next.Add((now.Sub(next)/interval + 1) * interval)
	}
	return next
}

func sendAlert(ctx context.Context, fn AlertFunc, a Alert) bool {
	if fn == nil {
		fn = LogAlert
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if c.Err() != nil {
		return false
	}
	if err := fn(c, a); err != nil {
		zap.S().Warnw("relay monitor alert failed", "error", err)
		return false
	}
	return true
}

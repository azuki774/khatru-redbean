package relay

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/azuki774/khatru-redbean/internal/nostr"
	"github.com/azuki774/khatru-redbean/internal/nostr/nip77"
	"github.com/azuki774/khatru-redbean/internal/relay/engine"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/azuki774/khatru-redbean/relay"

type relayTelemetry struct {
	tracer           trace.Tracer
	databaseOps      metric.Int64Counter
	databaseDuration metric.Float64Histogram
	policyDecisions  metric.Int64Counter

	connections         metric.Int64UpDownCounter
	connectionCount     metric.Int64Counter
	connectionDuration  metric.Float64Histogram
	rejectedConnections metric.Int64Counter

	connectionStarts sync.Map

	onDatabaseError func(operation string, err error)
}

func newRelayTelemetry() (*relayTelemetry, error) {
	meter := otel.Meter(instrumentationName)
	databaseOps, err := meter.Int64Counter("nostr.relay.db.operations")
	if err != nil {
		return nil, err
	}
	databaseDuration, err := meter.Float64Histogram(
		"db.client.operation.duration",
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10),
	)
	if err != nil {
		return nil, err
	}
	policyDecisions, err := meter.Int64Counter("nostr.relay.policy.decisions")
	if err != nil {
		return nil, err
	}
	connections, err := meter.Int64UpDownCounter("nostr.relay.connections")
	if err != nil {
		return nil, err
	}
	connectionCount, err := meter.Int64Counter("nostr.relay.connection.count")
	if err != nil {
		return nil, err
	}
	connectionDuration, err := meter.Float64Histogram(
		"nostr.relay.connection.duration",
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(1, 5, 10, 30, 60, 300, 600, 1800, 3600),
	)
	if err != nil {
		return nil, err
	}
	rejectedConnections, err := meter.Int64Counter("nostr.relay.connection.rejected")
	if err != nil {
		return nil, err
	}

	return &relayTelemetry{
		tracer:              otel.Tracer(instrumentationName),
		databaseOps:         databaseOps,
		databaseDuration:    databaseDuration,
		policyDecisions:     policyDecisions,
		connections:         connections,
		connectionCount:     connectionCount,
		connectionDuration:  connectionDuration,
		rejectedConnections: rejectedConnections,
	}, nil
}

func (t *relayTelemetry) eventOperation(operation string, next func(context.Context, *nostr.Event) error) func(context.Context, *nostr.Event) error {
	return func(ctx context.Context, event *nostr.Event) error {
		return t.database(ctx, operation, eventAttributes(event), func(ctx context.Context) error {
			return next(ctx, event)
		})
	}
}

// queryPendingLimit bounds how many events the wrapper buffers ahead of the
// consumer before it stops draining the storage channel (backpressure).
const queryPendingLimit = 1024

func (t *relayTelemetry) queryOperation(next func(context.Context, nostr.Filter) (chan *nostr.Event, error)) func(context.Context, nostr.Filter) (chan *nostr.Event, error) {
	return func(ctx context.Context, filter nostr.Filter) (chan *nostr.Event, error) {
		ctx, span, started := t.startDatabase(ctx, "query_events", filterAttributes(filter))
		events, err := next(ctx, filter)
		if err != nil || events == nil {
			t.finishDatabase(ctx, span, started, "query_events", err)
			return events, err
		}

		wrapped := make(chan *nostr.Event)
		go func() {
			defer close(wrapped)

			var queue []*nostr.Event
			spanEnded := false
			endSpan := func() {
				if !spanEnded {
					spanEnded = true
					t.finishDatabase(ctx, span, started, "query_events", nil)
				}
			}
			defer endSpan()

			sourceOpen := true
			for sourceOpen || len(queue) > 0 {
				// enable each case dynamically: a nil channel blocks forever, so
				// receiving is disabled while the queue is full and sending is
				// disabled while the queue is empty
				var recvCh chan *nostr.Event
				if sourceOpen && len(queue) < queryPendingLimit {
					recvCh = events
				}
				var sendCh chan *nostr.Event
				var head *nostr.Event
				if len(queue) > 0 {
					sendCh = wrapped
					head = queue[0]
				}

				select {
				case event, ok := <-recvCh:
					if !ok {
						sourceOpen = false
						// the database scan is done: the span ends here even if
						// delivering the remaining events takes longer
						endSpan()
						continue
					}
					queue = append(queue, event)
				case sendCh <- head:
					queue = queue[1:]
				case <-ctx.Done():
					return
				}
			}
		}()
		return wrapped, nil
	}
}

// messageObserver starts the root span for a single websocket message. The
// message context is derived from the connection context (which is not tied to
// the hijacked HTTP request) and links back to the handshake request span.
func (t *relayTelemetry) messageObserver(ctx context.Context, envelope any) (context.Context, func(string)) {
	spanName := messageSpanName(envelope)
	attributes := []attribute.KeyValue{
		attribute.String("nostr.message.type", messageTypeLabel(spanName)),
	}
	attributes = append(attributes, messageAttributes(envelope)...)
	opts := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindServer)}
	if ws := engine.GetConnection(ctx); ws != nil {
		attributes = append(attributes, attribute.String("nostr.connection.id", ws.Challenge))
		if ws.Request != nil {
			if sc := trace.SpanContextFromContext(ws.Request.Context()); sc.IsValid() {
				opts = append(opts, trace.WithLinks(trace.Link{SpanContext: sc}))
			}
		}
	}
	opts = append(opts, trace.WithAttributes(attributes...))

	ctx, span := t.tracer.Start(ctx, spanName, opts...)
	var once sync.Once
	done := func(outcome string) {
		once.Do(func() {
			if outcome != "" && outcome != "ok" {
				span.SetAttributes(attribute.String("nostr.outcome", outcome))
				span.SetStatus(codes.Error, outcome)
			}
			span.End()
		})
	}
	return ctx, done
}

// messageSpanName returns the span-ready name for a parsed message envelope.
func messageSpanName(envelope any) string {
	switch envelope.(type) {
	case *nostr.EventEnvelope:
		return "nostr.event"
	case *nostr.ReqEnvelope:
		return "nostr.req"
	case *nostr.CountEnvelope:
		return "nostr.count"
	case *nostr.CloseEnvelope:
		return "nostr.close"
	case *nostr.AuthEnvelope:
		return "nostr.auth"
	case *nip77.OpenEnvelope, *nip77.MessageEnvelope, *nip77.CloseEnvelope:
		return "nostr.negentropy"
	case nil:
		return "ws.parse"
	default:
		return "ws.message"
	}
}

// messageAttributes returns the low-cardinality summary attributes carried by
// the message envelope (event class for EVENT, filter shape for REQ/COUNT).
func messageAttributes(envelope any) []attribute.KeyValue {
	switch env := envelope.(type) {
	case *nostr.EventEnvelope:
		if env != nil {
			return eventAttributes(&env.Event)
		}
	case *nostr.ReqEnvelope:
		if env != nil && len(env.Filters) > 0 {
			return filterAttributes(env.Filters[0])
		}
	case *nostr.CountEnvelope:
		if env != nil {
			return filterAttributes(env.Filter)
		}
	}
	return nil
}

func messageTypeLabel(messageType string) string {
	label := strings.TrimPrefix(messageType, "nostr.")
	return strings.TrimPrefix(label, "ws.")
}

func (t *relayTelemetry) countOperation(next func(context.Context, nostr.Filter) (int64, error)) func(context.Context, nostr.Filter) (int64, error) {
	return func(ctx context.Context, filter nostr.Filter) (int64, error) {
		var count int64
		err := t.database(ctx, "count_events", filterAttributes(filter), func(ctx context.Context) error {
			var err error
			count, err = next(ctx, filter)
			return err
		})
		return count, err
	}
}

func (t *relayTelemetry) eventPolicy(name string, next func(context.Context, *nostr.Event) (bool, string)) func(context.Context, *nostr.Event) (bool, string) {
	return func(ctx context.Context, event *nostr.Event) (bool, string) {
		ctx, span := t.tracer.Start(ctx, "nostr.policy."+name)
		defer span.End()

		reject, message := next(ctx, event)
		decision := "allow"
		if reject {
			decision = "reject"
		}
		attributes := append(eventAttributes(event),
			attribute.String("nostr.policy.name", name),
			attribute.String("nostr.policy.decision", decision),
		)
		span.SetAttributes(attributes...)
		t.policyDecisions.Add(ctx, 1, metric.WithAttributes(
			attribute.String("nostr.policy.name", name),
			attribute.String("nostr.policy.decision", decision),
		))
		return reject, message
	}
}

func (t *relayTelemetry) filterPolicy(name string, next func(context.Context, nostr.Filter) (bool, string)) func(context.Context, nostr.Filter) (bool, string) {
	return func(ctx context.Context, filter nostr.Filter) (bool, string) {
		ctx, span := t.tracer.Start(ctx, "nostr.policy."+name)
		defer span.End()

		reject, message := next(ctx, filter)
		decision := "allow"
		if reject {
			decision = "reject"
		}
		attributes := append(filterAttributes(filter),
			attribute.String("nostr.policy.name", name),
			attribute.String("nostr.policy.decision", decision),
		)
		span.SetAttributes(attributes...)
		t.policyDecisions.Add(ctx, 1, metric.WithAttributes(
			attribute.String("nostr.policy.name", name),
			attribute.String("nostr.policy.decision", decision),
		))
		return reject, message
	}
}

func (t *relayTelemetry) database(ctx context.Context, operation string, attributes []attribute.KeyValue, next func(context.Context) error) error {
	ctx, span, started := t.startDatabase(ctx, operation, attributes)
	err := next(ctx)
	t.finishDatabase(ctx, span, started, operation, err)
	return err
}

func (t *relayTelemetry) startDatabase(ctx context.Context, operation string, attributes []attribute.KeyValue) (context.Context, trace.Span, time.Time) {
	attributes = append(attributes,
		attribute.String("db.system.name", "postgresql"),
		attribute.String("db.operation.name", operation),
	)
	ctx, span := t.tracer.Start(ctx, "postgres."+operation, trace.WithSpanKind(trace.SpanKindClient))
	span.SetAttributes(attributes...)
	return ctx, span, time.Now()
}

func (t *relayTelemetry) finishDatabase(ctx context.Context, span trace.Span, started time.Time, operation string, err error) {
	metricAttributes := []attribute.KeyValue{
		attribute.String("db.system.name", "postgresql"),
		attribute.String("db.operation.name", operation),
	}
	// cancellations caused by an abandoned consumer or a canceled message are
	// benign: they are not database failures and carry no error information
	if err != nil && !isBenignDatabaseCancel(ctx, err) {
		errorType := reflect.TypeOf(err).String()
		span.SetAttributes(attribute.String("error.type", errorType))
		span.SetStatus(codes.Error, "database operation failed")
		metricAttributes = append(metricAttributes, attribute.String("error.type", errorType))
	}
	span.End()

	t.databaseOps.Add(ctx, 1, metric.WithAttributes(metricAttributes...))
	t.databaseDuration.Record(ctx, time.Since(started).Seconds(), metric.WithAttributes(metricAttributes...))

	if t.onDatabaseError != nil && isDatabaseFailure(ctx, err) {
		t.onDatabaseError(operation, err)
	}
}

func isBenignDatabaseCancel(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) || ctx.Err() != nil
}

// httpMiddleware wraps the relay HTTP handler to record a server span per
// request. For WebSocket connections this captures the upgrade request; the
// long-lived connection itself is covered by the connection metrics below.
func (t *relayTelemetry) httpMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isWebSocket := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
		attributes := []attribute.KeyValue{
			attribute.String("http.request.method", r.Method),
			attribute.String("url.path", r.URL.Path),
		}
		if isWebSocket {
			attributes = append(attributes, attribute.Bool("nostr.websocket", true))
		}

		ctx, span := t.tracer.Start(r.Context(), "http.request",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attributes...),
		)
		defer span.End()

		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r.WithContext(ctx))

		// a hijacked websocket connection never produces an HTTP status
		if !isWebSocket || sw.wroteHeader {
			span.SetAttributes(attribute.Int("http.response.status_code", sw.status))
		}
		if sw.wroteHeader && sw.status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, "server error")
		}
	})
}

func (t *relayTelemetry) onConnect(ctx context.Context) {
	t.connectionStarts.Store(ctx, time.Now())
	t.connections.Add(ctx, 1)
	t.connectionCount.Add(ctx, 1, metric.WithAttributes(attribute.String("nostr.connection.state", "open")))
}

func (t *relayTelemetry) onDisconnect(ctx context.Context) {
	if value, ok := t.connectionStarts.LoadAndDelete(ctx); ok {
		if started, ok := value.(time.Time); ok {
			t.connectionDuration.Record(ctx, time.Since(started).Seconds())
		}
	}
	t.connections.Add(ctx, -1)
	t.connectionCount.Add(ctx, 1, metric.WithAttributes(attribute.String("nostr.connection.state", "close")))
}

func (t *relayTelemetry) rejectConnection(r *http.Request) bool {
	t.rejectedConnections.Add(r.Context(), 1)
	return false
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func eventAttributes(event *nostr.Event) []attribute.KeyValue {
	if event == nil {
		return nil
	}

	return []attribute.KeyValue{
		attribute.String("nostr.event.class", eventClass(event)),
		attribute.String("nostr.event.tags_count_bucket", cardinalityBucket(len(event.Tags))),
	}
}

func eventClass(event *nostr.Event) string {
	switch {
	case event.Kind == 5:
		return "deletion"
	case nostr.IsEphemeralKind(event.Kind):
		return "ephemeral"
	case nostr.IsRegularKind(event.Kind):
		return "regular"
	default:
		return "replaceable"
	}
}

func filterAttributes(filter nostr.Filter) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("nostr.filter.kinds_count_bucket", cardinalityBucket(len(filter.Kinds))),
		attribute.String("nostr.filter.authors_count_bucket", cardinalityBucket(len(filter.Authors))),
		attribute.String("nostr.filter.ids_count_bucket", cardinalityBucket(len(filter.IDs))),
		attribute.String("nostr.filter.tags_count_bucket", cardinalityBucket(len(filter.Tags))),
		attribute.String("nostr.filter.limit_bucket", cardinalityBucket(filter.Limit)),
	}
}

func cardinalityBucket(value int) string {
	switch {
	case value == 0:
		return "0"
	case value == 1:
		return "1"
	case value <= 5:
		return "2-5"
	case value <= 10:
		return "6-10"
	case value <= 100:
		return "11-100"
	default:
		return "101+"
	}
}

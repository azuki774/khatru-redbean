package relay

import (
	"context"
	"reflect"
	"time"

	"github.com/nbd-wtf/go-nostr"
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

	return &relayTelemetry{
		tracer:           otel.Tracer(instrumentationName),
		databaseOps:      databaseOps,
		databaseDuration: databaseDuration,
		policyDecisions:  policyDecisions,
	}, nil
}

func (t *relayTelemetry) eventOperation(operation string, next func(context.Context, *nostr.Event) error) func(context.Context, *nostr.Event) error {
	return func(ctx context.Context, event *nostr.Event) error {
		return t.database(ctx, operation, eventAttributes(event), func(ctx context.Context) error {
			return next(ctx, event)
		})
	}
}

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
			defer t.finishDatabase(ctx, span, started, "query_events", nil)

			for {
				select {
				case event, ok := <-events:
					if !ok {
						return
					}
					select {
					case wrapped <- event:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
		return wrapped, nil
	}
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
	if err != nil {
		errorType := reflect.TypeOf(err).String()
		span.SetAttributes(attribute.String("error.type", errorType))
		span.SetStatus(codes.Error, "database operation failed")
		metricAttributes = append(metricAttributes, attribute.String("error.type", errorType))
	}
	span.End()

	t.databaseOps.Add(ctx, 1, metric.WithAttributes(metricAttributes...))
	t.databaseDuration.Record(ctx, time.Since(started).Seconds(), metric.WithAttributes(metricAttributes...))
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

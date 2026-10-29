package storage

import "context"

type negentropyContextKey struct{}

var negentropySessionKey = negentropyContextKey{}

func IsNegentropySession(ctx context.Context) bool {
	return ctx.Value(negentropySessionKey) != nil
}

func SetNegentropy(ctx context.Context) context.Context {
	return context.WithValue(ctx, negentropySessionKey, struct{}{})
}

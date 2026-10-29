package policies

import (
	"context"
	"net/http"
	"time"

	"github.com/azuki774/khatru-redbean/internal/nostr"
	"github.com/azuki774/khatru-redbean/internal/relay/engine"
)

func EventIPRateLimiter(tokensPerInterval int, interval time.Duration, maxTokens int) func(ctx context.Context, _ *nostr.Event) (reject bool, msg string) {
	rl := startRateLimitSystem[string](tokensPerInterval, interval, maxTokens)

	return func(ctx context.Context, _ *nostr.Event) (reject bool, msg string) {
		ip := engine.GetIP(ctx)
		if ip == "" {
			return false, ""
		}
		return rl(ip), "rate-limited: slow down, please"
	}
}

func EventPubKeyRateLimiter(tokensPerInterval int, interval time.Duration, maxTokens int) func(ctx context.Context, _ *nostr.Event) (reject bool, msg string) {
	rl := startRateLimitSystem[string](tokensPerInterval, interval, maxTokens)

	return func(ctx context.Context, evt *nostr.Event) (reject bool, msg string) {
		return rl(evt.PubKey), "rate-limited: slow down, please"
	}
}

func EventAuthedPubKeyRateLimiter(tokensPerInterval int, interval time.Duration, maxTokens int) func(ctx context.Context, _ *nostr.Event) (reject bool, msg string) {
	rl := startRateLimitSystem[string](tokensPerInterval, interval, maxTokens)

	return func(ctx context.Context, _ *nostr.Event) (reject bool, msg string) {
		user := engine.GetAuthed(ctx)
		if user == "" {
			return false, ""
		}
		return rl(user), "rate-limited: slow down, please"
	}
}

func ConnectionRateLimiter(tokensPerInterval int, interval time.Duration, maxTokens int) func(r *http.Request) bool {
	rl := startRateLimitSystem[string](tokensPerInterval, interval, maxTokens)

	return func(r *http.Request) bool {
		return rl(engine.GetIPFromRequest(r))
	}
}

func FilterIPRateLimiter(tokensPerInterval int, interval time.Duration, maxTokens int) func(ctx context.Context, _ nostr.Filter) (reject bool, msg string) {
	rl := startRateLimitSystem[string](tokensPerInterval, interval, maxTokens)

	return func(ctx context.Context, _ nostr.Filter) (reject bool, msg string) {
		return rl(engine.GetIP(ctx)), "rate-limited: there is a bug in the client, no one should be making so many requests"
	}
}

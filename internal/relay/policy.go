package relay

import (
	"context"
	"fmt"

	"github.com/azuki774/khatru-redbean/internal/nostr"
	"github.com/azuki774/khatru-redbean/internal/relay/engine"
	"go.uber.org/zap"
)

func (i *instance) RestrictCountry(ctx context.Context, event *nostr.Event) (reject bool, msg string) {
	if i.CountryOnly == "" {
		return false, "" // 未設定なら無条件OK
	}

	conn := engine.GetConnection(ctx)
	if conn == nil {
		zap.S().Warnw("connection information not available, rejecting event", "countryOnly", i.CountryOnly)
		return true, "blocked: unable to verify country information"
	}
	country := conn.Request.Header.Get("CF-IPCountry")

	// Please set the Cloudflare country tag here to restrict access to a specific country.
	if country != i.CountryOnly {
		return true, fmt.Sprintf("blocked: only access from %v is allowed", i.CountryOnly)
	}
	return false, ""
}

package postgres

import (
	"context"

	"github.com/azuki774/khatru-redbean/internal/nostr"
)

func (b *PostgresBackend) DeleteEvent(ctx context.Context, evt *nostr.Event) error {
	_, err := b.DB.ExecContext(ctx, "DELETE FROM event WHERE id = $1", evt.ID)
	return err
}

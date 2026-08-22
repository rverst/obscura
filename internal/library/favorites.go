package library

import (
	"context"
	"fmt"
	"time"
)

// SetFavorite marks or unmarks a photo.
//
// Idempotent on purpose: a double tap, or two devices marking the same photo,
// must not be an error. Marking an already-marked photo keeps the original
// timestamp rather than moving it, so "when did I favourite this" stays true.
func SetFavorite(ctx context.Context, v Vault, mediaID int64, favorite bool) (*time.Time, error) {
	var (
		res interface{ RowsAffected() (int64, error) }
		err error
	)
	if favorite {
		res, err = v.DB.ExecContext(ctx, `
			UPDATE media SET favorited_at = coalesce(favorited_at, unixepoch())
			 WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, mediaID, v.UserID)
	} else {
		res, err = v.DB.ExecContext(ctx, `
			UPDATE media SET favorited_at = NULL
			 WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, mediaID, v.UserID)
	}
	if err != nil {
		return nil, fmt.Errorf("library: set favourite: %w", err)
	}
	// A trashed photo is not in the library as far as everything else is
	// concerned, so it cannot be favourited either.
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	if !favorite {
		return nil, nil
	}

	var at int64
	if err := v.DB.QueryRowContext(ctx,
		`SELECT favorited_at FROM media WHERE id = ? AND user_id = ?`,
		mediaID, v.UserID).Scan(&at); err != nil {
		return nil, fmt.Errorf("library: read favourite: %w", err)
	}
	t := time.Unix(at, 0)
	return &t, nil
}

// FavoriteCount reports how many photos are marked.
func FavoriteCount(ctx context.Context, v Vault) (int, error) {
	var n int
	if err := v.DB.QueryRowContext(ctx, `
		SELECT count(*) FROM media
		 WHERE user_id = ? AND deleted_at IS NULL AND favorited_at IS NOT NULL`,
		v.UserID).Scan(&n); err != nil {
		return 0, fmt.Errorf("library: count favourites: %w", err)
	}
	return n, nil
}

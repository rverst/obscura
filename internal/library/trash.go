package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TrashRetention is how long a deleted photo can be recovered.
//
// A self-hosted library often has no backup behind it, so a mis-click has to be
// reversible for long enough that someone notices it happened.
const TrashRetention = 30 * 24 * time.Hour

// Trash moves media to the trash.
//
// Album membership is kept. Album views filter out trashed media anyway, so the
// photo disappears from every album and comes back where it was on restore --
// dropping the rows would silently destroy the curation instead.
func Trash(ctx context.Context, v Vault, mediaID int64) error {
	res, err := v.DB.ExecContext(ctx, `
		UPDATE media SET deleted_at = unixepoch()
		 WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, mediaID, v.UserID)
	if err != nil {
		return fmt.Errorf("library: trash media: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Restore brings media back out of the trash.
func Restore(ctx context.Context, v Vault, mediaID int64) error {
	res, err := v.DB.ExecContext(ctx, `
		UPDATE media SET deleted_at = NULL
		 WHERE id = ? AND user_id = ? AND deleted_at IS NOT NULL`, mediaID, v.UserID)
	if err != nil {
		return fmt.Errorf("library: restore media: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TrashedItem is a photo awaiting deletion, with the deadline attached.
type TrashedItem struct {
	*Media
	DeletedAt time.Time
	PurgeAt   time.Time
}

// ListTrash returns trashed media, most recently deleted first.
func ListTrash(ctx context.Context, v Vault) ([]*TrashedItem, error) {
	rows, err := v.DB.QueryContext(ctx,
		`SELECT deleted_at, `+mediaColumns+` FROM media
		  WHERE user_id = ? AND deleted_at IS NOT NULL
		  ORDER BY deleted_at DESC, id DESC`, v.UserID)
	if err != nil {
		return nil, fmt.Errorf("library: list trash: %w", err)
	}
	defer rows.Close()

	var out []*TrashedItem
	for rows.Next() {
		var deletedAt int64
		m, err := scanMediaWithPrefix(rows, &deletedAt)
		if err != nil {
			return nil, fmt.Errorf("library: scan trashed media: %w", err)
		}
		t := time.Unix(deletedAt, 0)
		out = append(out, &TrashedItem{Media: m, DeletedAt: t, PurgeAt: t.Add(TrashRetention)})
	}
	return out, rows.Err()
}

// Purge deletes media permanently, including its blobs.
//
// This is the only place blobs are removed. Ordering matters: the row goes
// first, then the blobs. A crash between the two leaves unreferenced files,
// which waste space; the reverse would leave a row pointing at content that no
// longer exists, which looks like corruption to everything that reads it.
func (l *Library) Purge(ctx context.Context, v Vault, mediaID int64) error {
	var blobs [3]sql.NullString
	err := v.DB.QueryRowContext(ctx, `
		SELECT blob_orig, blob_preview, blob_thumb FROM media
		 WHERE id = ? AND user_id = ? AND deleted_at IS NOT NULL`, mediaID, v.UserID).
		Scan(&blobs[0], &blobs[1], &blobs[2])
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("library: read media for purge: %w", err)
	}

	if _, err := v.DB.ExecContext(ctx,
		`DELETE FROM media WHERE id = ? AND user_id = ?`, mediaID, v.UserID); err != nil {
		return fmt.Errorf("library: delete media row: %w", err)
	}
	for _, b := range blobs {
		if b.Valid && b.String != "" {
			if err := l.blobs.Remove(b.String); err != nil {
				// The row is already gone, so the library is consistent. Report
				// it, but do not pretend the deletion failed.
				return fmt.Errorf("library: media %d deleted, but a blob remains: %w", mediaID, err)
			}
		}
	}
	return nil
}

// PurgeExpired permanently deletes everything past its retention deadline.
//
// Called after unlocking rather than on a timer: the key exists only while
// someone is logged in, so there is no moment at which a background job could
// open the database on its own.
func (l *Library) PurgeExpired(ctx context.Context, v Vault) (int, error) {
	cutoff := time.Now().Add(-TrashRetention).Unix()

	rows, err := v.DB.QueryContext(ctx,
		`SELECT id FROM media
		  WHERE user_id = ? AND deleted_at IS NOT NULL AND deleted_at < ?`, v.UserID, cutoff)
	if err != nil {
		return 0, fmt.Errorf("library: find expired media: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	purged := 0
	for _, id := range ids {
		if err := l.Purge(ctx, v, id); err != nil {
			// One stuck item must not stop the rest from being cleared.
			continue
		}
		purged++
	}
	return purged, nil
}

// EmptyTrash purges everything currently in the trash.
func (l *Library) EmptyTrash(ctx context.Context, v Vault) (int, error) {
	items, err := ListTrash(ctx, v)
	if err != nil {
		return 0, err
	}
	purged := 0
	for _, it := range items {
		if err := l.Purge(ctx, v, it.ID); err != nil {
			continue
		}
		purged++
	}
	return purged, nil
}

// findTrashedBySHA256 looks for trashed media with the same content.
//
// Needed because the unique index on (user_id, sha256) covers trashed rows too.
// Without this, re-uploading a photo that is sitting in the trash would fail on
// a constraint violation -- and from the outside that looks like the upload
// being broken rather than the photo already being there.
func findTrashedBySHA256(ctx context.Context, v Vault, sum []byte) (int64, error) {
	var id int64
	err := v.DB.QueryRowContext(ctx, `
		SELECT id FROM media
		 WHERE user_id = ? AND sha256 = ? AND deleted_at IS NOT NULL`, v.UserID, sum).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("library: look for trashed duplicate: %w", err)
	}
	return id, nil
}

// TrashCount reports how many items are in the trash.
func TrashCount(ctx context.Context, v Vault) (int, error) {
	var n int
	if err := v.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM media WHERE user_id = ? AND deleted_at IS NOT NULL`,
		v.UserID).Scan(&n); err != nil {
		return 0, fmt.Errorf("library: count trash: %w", err)
	}
	return n, nil
}

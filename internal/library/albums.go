package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rverst/obscura/internal/order"
)

// ErrAlreadyInAlbum reports that a photo is already a member.
var ErrAlreadyInAlbum = errors.New("library: already in this album")

// Album is a user-curated collection with its own order.
type Album struct {
	ID          int64
	Title       string
	Description string
	// ParentID is nil at the top level. Albums nest: Croatia -> 2024, 2025.
	ParentID     *int64
	CoverMediaID *int64
	// ItemCount is this album's own photos; TotalCount includes sub-albums.
	// A parent that holds only sub-albums would otherwise report 0.
	ItemCount  int
	TotalCount int
	// Cover is the picture to show for this album, resolved rather than
	// stored: chosen by hand if there is one, else the newest photo, else the
	// best cover among the sub-albums. See resolveCovers.
	Cover     *Media
	CreatedAt time.Time
}

// AlbumItem is one photo in an album, carrying its position.
type AlbumItem struct {
	Media   *Media
	SortKey string
}

// CreateAlbum adds an album, optionally inside another.
func CreateAlbum(ctx context.Context, v Vault, title, description string, parentID *int64) (*Album, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, errors.New("library: album needs a title")
	}
	var parent any
	if parentID != nil {
		if _, err := albumRow(ctx, v, *parentID); err != nil {
			return nil, fmt.Errorf("library: no such parent album: %w", err)
		}
		parent = *parentID
	}
	a := &Album{Title: title, Description: description, ParentID: parentID, CreatedAt: time.Now()}
	if err := v.DB.QueryRowContext(ctx, `
		INSERT INTO albums(user_id, title, description, parent_id, created_at)
		VALUES(?, ?, ?, ?, ?) RETURNING id`,
		v.UserID, title, description, parent, a.CreatedAt.Unix()).Scan(&a.ID); err != nil {
		return nil, fmt.Errorf("library: create album: %w", err)
	}
	return a, nil
}

// ListAlbums returns every album with its item count.
//
// The count comes from a join rather than a stored column: a denormalised
// counter is one more thing to keep correct across adds, removals and deleted
// media, and albums are few enough that the join costs nothing.
func ListAlbums(ctx context.Context, v Vault) ([]*Album, error) {
	rows, err := v.DB.QueryContext(ctx, `
		SELECT a.id, a.title, a.description, a.parent_id, a.cover_media_id, a.created_at,
		       count(ai.media_id)
		  FROM albums a
		  LEFT JOIN album_items ai ON ai.album_id = a.id
		  LEFT JOIN media m ON m.id = ai.media_id AND m.deleted_at IS NULL
		 WHERE a.user_id = ? AND a.deleted_at IS NULL AND (ai.media_id IS NULL OR m.id IS NOT NULL)
		 GROUP BY a.id
		 ORDER BY a.title COLLATE NOCASE`, v.UserID)
	if err != nil {
		return nil, fmt.Errorf("library: list albums: %w", err)
	}
	defer rows.Close()

	var out []*Album
	for rows.Next() {
		a := &Album{}
		var parent, cover sql.NullInt64
		var created int64
		if err := rows.Scan(&a.ID, &a.Title, &a.Description, &parent, &cover, &created,
			&a.ItemCount); err != nil {
			return nil, err
		}
		if parent.Valid {
			id := parent.Int64
			a.ParentID = &id
		}
		if cover.Valid {
			id := cover.Int64
			a.CoverMediaID = &id
		}
		a.CreatedAt = time.Unix(created, 0)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Covers and subtree totals are worked out over the whole set at once,
	// which is why they live here rather than in GetAlbum: both need to see
	// the sub-albums to answer for a parent.
	explicit, err := explicitCovers(ctx, v)
	if err != nil {
		return nil, err
	}
	newest, err := newestPerAlbum(ctx, v)
	if err != nil {
		return nil, err
	}
	resolveCovers(out, explicit, newest)
	totalCounts(out)
	return out, nil
}

// GetAlbum returns one album, resolved the same way the list resolves it.
//
// It goes through the whole set rather than reading a single row, because a
// cover and a subtree total are answers about an album's descendants, not about
// its own row. Reading the row alone gave an album one cover as a tile and no
// cover when opened -- the same photo described two ways depending on which
// endpoint asked.
func GetAlbum(ctx context.Context, v Vault, id int64) (*Album, error) {
	all, err := ListAlbums(ctx, v)
	if err != nil {
		return nil, err
	}
	for _, a := range all {
		if a.ID == id {
			return a, nil
		}
	}
	return nil, ErrNotFound
}

// albumRow reads one album without resolving its cover or subtree total.
//
// This is the cheap existence check the mutations want, and the one the
// ancestor walk in AlbumPath needs: resolving the whole set once per level
// would turn a sixteen-deep path into sixteen full passes.
func albumRow(ctx context.Context, v Vault, id int64) (*Album, error) {
	a := &Album{}
	var cover sql.NullInt64
	var created int64
	var parent sql.NullInt64
	err := v.DB.QueryRowContext(ctx, `
		SELECT a.id, a.title, a.description, a.parent_id, a.cover_media_id, a.created_at,
		       (SELECT count(*) FROM album_items ai
		          JOIN media m ON m.id = ai.media_id AND m.deleted_at IS NULL
		         WHERE ai.album_id = a.id)
		  FROM albums a
		 WHERE a.id = ? AND a.user_id = ? AND a.deleted_at IS NULL`, id, v.UserID).
		Scan(&a.ID, &a.Title, &a.Description, &parent, &cover, &created, &a.ItemCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("library: get album: %w", err)
	}
	if parent.Valid {
		p := parent.Int64
		a.ParentID = &p
	}
	if cover.Valid {
		c := cover.Int64
		a.CoverMediaID = &c
	}
	a.CreatedAt = time.Unix(created, 0)
	a.TotalCount = a.ItemCount
	return a, nil
}

// UpdateAlbum changes the title and description.
func UpdateAlbum(ctx context.Context, v Vault, id int64, title, description string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("library: album needs a title")
	}
	res, err := v.DB.ExecContext(ctx, `
		UPDATE albums SET title = ?, description = ?
		 WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
		title, description, id, v.UserID)
	if err != nil {
		return fmt.Errorf("library: update album: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAlbum removes an album and everything nested inside it, but never a
// photograph.
//
// An album is a view onto the library, so discarding one must not discard the
// pictures in it -- that would turn a tidy-up into data loss. Sub-albums go
// with the parent: leaving them behind would strand them, still holding
// photos and reachable from nowhere.
func DeleteAlbum(ctx context.Context, v Vault, id int64) (int, error) {
	if _, err := albumRow(ctx, v, id); err != nil {
		return 0, err
	}

	tx, err := v.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("library: begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Breadth-first over the subtree, bounded by the same depth guard as every
	// other walk so a cycle cannot spin here.
	level := []int64{id}
	var doomed []int64
	for depth := 0; depth < maxAlbumDepth && len(level) > 0; depth++ {
		doomed = append(doomed, level...)
		var next []int64
		for _, parent := range level {
			rows, err := tx.QueryContext(ctx, `
				SELECT id FROM albums
				 WHERE parent_id = ? AND user_id = ? AND deleted_at IS NULL`,
				parent, v.UserID)
			if err != nil {
				return 0, fmt.Errorf("library: find sub-albums: %w", err)
			}
			for rows.Next() {
				var child int64
				if err := rows.Scan(&child); err != nil {
					rows.Close()
					return 0, err
				}
				next = append(next, child)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return 0, err
			}
		}
		level = next
	}

	for _, albumID := range doomed {
		if _, err := tx.ExecContext(ctx, `
			UPDATE albums SET deleted_at = unixepoch()
			 WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
			albumID, v.UserID); err != nil {
			return 0, fmt.Errorf("library: delete album %d: %w", albumID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("library: commit: %w", err)
	}
	return len(doomed), nil
}

// AlbumItems returns the album's photos in their custom order.
func AlbumItems(ctx context.Context, v Vault, albumID int64) ([]*AlbumItem, error) {
	if _, err := albumRow(ctx, v, albumID); err != nil {
		return nil, err
	}
	rows, err := v.DB.QueryContext(ctx, `
		SELECT ai.sort_key, `+prefixed("m", mediaColumns)+`
		  FROM album_items ai
		  JOIN media m ON m.id = ai.media_id
		 WHERE ai.album_id = ? AND m.deleted_at IS NULL
		 ORDER BY ai.sort_key`, albumID)
	if err != nil {
		return nil, fmt.Errorf("library: list album items: %w", err)
	}
	defer rows.Close()

	var out []*AlbumItem
	for rows.Next() {
		var key string
		// sort_key is scanned first, so the media columns need an offset scan.
		m, err := scanMediaWithPrefix(rows, &key)
		if err != nil {
			return nil, fmt.Errorf("library: scan album item: %w", err)
		}
		out = append(out, &AlbumItem{Media: m, SortKey: key})
	}
	return out, rows.Err()
}

// AddToAlbum appends photos to the end of an album.
//
// Media already present is skipped rather than rejected: adding a selection
// that partly overlaps what is already there should add the rest, not fail.
func AddToAlbum(ctx context.Context, v Vault, albumID int64, mediaIDs []int64) (int, error) {
	if _, err := albumRow(ctx, v, albumID); err != nil {
		return 0, err
	}
	if len(mediaIDs) == 0 {
		return 0, nil
	}

	tx, err := v.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("library: begin transaction: %w", err)
	}
	defer tx.Rollback()

	last, err := lastSortKey(ctx, tx, albumID)
	if err != nil {
		return 0, err
	}

	added := 0
	for _, mediaID := range mediaIDs {
		// Scoped by user, so an id belonging to someone else simply is not
		// found rather than being silently added.
		var exists int
		err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM media WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
			mediaID, v.UserID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("library: check media %d: %w", mediaID, err)
		}

		key, err := order.Between(last, "")
		if err != nil {
			return 0, fmt.Errorf("library: next sort key: %w", err)
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO album_items(album_id, media_id, sort_key, added_at)
			VALUES(?, ?, ?, unixepoch())
			ON CONFLICT(album_id, media_id) DO NOTHING`, albumID, mediaID, key)
		if err != nil {
			return 0, fmt.Errorf("library: add media %d: %w", mediaID, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
			last = key
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("library: commit: %w", err)
	}
	return added, nil
}

// RemoveFromAlbum removes a photo from an album, leaving the photo itself alone.
func RemoveFromAlbum(ctx context.Context, v Vault, albumID, mediaID int64) error {
	if _, err := albumRow(ctx, v, albumID); err != nil {
		return err
	}
	res, err := v.DB.ExecContext(ctx,
		`DELETE FROM album_items WHERE album_id = ? AND media_id = ?`, albumID, mediaID)
	if err != nil {
		return fmt.Errorf("library: remove from album: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveInAlbum places a photo between two neighbours.
//
// The caller names the neighbours rather than an index, because an index would
// be ambiguous the moment two clients reorder concurrently. Passing the
// neighbours makes the request describe an intent that survives the album
// having changed underneath it.
//
// This is one UPDATE of one row. That is the entire point of the sort key being
// a string.
func MoveInAlbum(ctx context.Context, v Vault, albumID, mediaID int64, afterID, beforeID *int64) (string, error) {
	if _, err := albumRow(ctx, v, albumID); err != nil {
		return "", err
	}

	tx, err := v.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("library: begin transaction: %w", err)
	}
	defer tx.Rollback()

	var current string
	err = tx.QueryRowContext(ctx,
		`SELECT sort_key FROM album_items WHERE album_id = ? AND media_id = ?`,
		albumID, mediaID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("library: read position: %w", err)
	}

	after, err := neighbourKey(ctx, tx, albumID, afterID)
	if err != nil {
		return "", err
	}
	before, err := neighbourKey(ctx, tx, albumID, beforeID)
	if err != nil {
		return "", err
	}

	key, err := order.Between(after, before)
	if err != nil {
		return "", fmt.Errorf("library: position between %q and %q: %w", after, before, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE album_items SET sort_key = ? WHERE album_id = ? AND media_id = ?`,
		key, albumID, mediaID); err != nil {
		return "", fmt.Errorf("library: write position: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("library: commit: %w", err)
	}
	return key, nil
}

// SetAlbumCover picks the thumbnail shown for the album.
func SetAlbumCover(ctx context.Context, v Vault, albumID int64, mediaID *int64) error {
	if _, err := albumRow(ctx, v, albumID); err != nil {
		return err
	}
	var arg any
	if mediaID != nil {
		var exists int
		err := v.DB.QueryRowContext(ctx,
			`SELECT 1 FROM album_items ai JOIN media m ON m.id = ai.media_id
			  WHERE ai.album_id = ? AND ai.media_id = ? AND m.deleted_at IS NULL`,
			albumID, *mediaID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("library: media %d is not in this album", *mediaID)
		}
		if err != nil {
			return fmt.Errorf("library: check cover: %w", err)
		}
		arg = *mediaID
	}
	if _, err := v.DB.ExecContext(ctx,
		`UPDATE albums SET cover_media_id = ? WHERE id = ? AND user_id = ?`,
		arg, albumID, v.UserID); err != nil {
		return fmt.Errorf("library: set cover: %w", err)
	}
	return nil
}

// --- helpers ----------------------------------------------------------------

func lastSortKey(ctx context.Context, tx *sql.Tx, albumID int64) (string, error) {
	var key sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT max(sort_key) FROM album_items WHERE album_id = ?`, albumID).Scan(&key); err != nil {
		return "", fmt.Errorf("library: last sort key: %w", err)
	}
	return key.String, nil
}

// neighbourKey resolves a neighbouring media id to its sort key. A nil id means
// "no neighbour on that side", i.e. the very start or end of the album.
func neighbourKey(ctx context.Context, tx *sql.Tx, albumID int64, mediaID *int64) (string, error) {
	if mediaID == nil {
		return "", nil
	}
	var key string
	err := tx.QueryRowContext(ctx,
		`SELECT sort_key FROM album_items WHERE album_id = ? AND media_id = ?`,
		albumID, *mediaID).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: neighbour %d is not in this album", ErrNotFound, *mediaID)
	}
	if err != nil {
		return "", fmt.Errorf("library: read neighbour position: %w", err)
	}
	return key, nil
}

// prefixed qualifies a bare column list with a table alias.
func prefixed(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

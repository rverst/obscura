package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// maxAlbumDepth bounds how deeply albums may nest.
//
// Not a storage limit -- it is a guard. Every walk over the tree is recursive,
// and a limit means a cycle that slipped past SetAlbumParent produces an error
// instead of an unresponsive server.
const maxAlbumDepth = 16

// ErrAlbumCycle reports a move that would make an album its own ancestor.
var ErrAlbumCycle = errors.New("library: an album cannot contain itself")

// SetAlbumParent moves an album under another, or to the top level when parent
// is nil.
//
// The cycle check is here rather than in the schema because SQLite has no
// constraint that can express "not an ancestor of itself". Without it, dragging
// Croatia into its own 2025 sub-album would detach both from the tree: they
// would still exist, still hold photos, and never appear anywhere again.
func SetAlbumParent(ctx context.Context, v Vault, albumID int64, parentID *int64) error {
	if _, err := GetAlbum(ctx, v, albumID); err != nil {
		return err
	}
	if parentID != nil {
		if *parentID == albumID {
			return ErrAlbumCycle
		}
		if _, err := GetAlbum(ctx, v, *parentID); err != nil {
			return fmt.Errorf("library: no such parent album: %w", err)
		}
		// Walk up from the proposed parent. Meeting albumID on the way means
		// the move would close a loop.
		cur := *parentID
		for depth := 0; depth < maxAlbumDepth; depth++ {
			var next sql.NullInt64
			err := v.DB.QueryRowContext(ctx,
				`SELECT parent_id FROM albums WHERE id = ? AND user_id = ?`,
				cur, v.UserID).Scan(&next)
			if errors.Is(err, sql.ErrNoRows) || !next.Valid {
				break
			}
			if err != nil {
				return fmt.Errorf("library: walk album ancestors: %w", err)
			}
			if next.Int64 == albumID {
				return ErrAlbumCycle
			}
			cur = next.Int64
		}
	}

	var arg any
	if parentID != nil {
		arg = *parentID
	}
	res, err := v.DB.ExecContext(ctx, `
		UPDATE albums SET parent_id = ?
		 WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, arg, albumID, v.UserID)
	if err != nil {
		return fmt.Errorf("library: move album: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AlbumPath returns the albums from the root down to id, inclusive.
//
// This is the breadcrumb. Returned root-first because that is the order it is
// read in, and because reversing it at three call sites is three chances to get
// it wrong.
func AlbumPath(ctx context.Context, v Vault, id int64) ([]*Album, error) {
	var path []*Album
	cur := id
	for depth := 0; depth < maxAlbumDepth; depth++ {
		a, err := GetAlbum(ctx, v, cur)
		if err != nil {
			return nil, err
		}
		path = append([]*Album{a}, path...)
		if a.ParentID == nil {
			return path, nil
		}
		cur = *a.ParentID
	}
	return nil, fmt.Errorf("library: album %d is nested deeper than %d levels", id, maxAlbumDepth)
}

// --- cover resolution -------------------------------------------------------

// newestPerAlbum returns each album's most recent photo, one query for all of
// them.
//
// A window function rather than a query per album: the tile grid asks for every
// album at once, and N+1 queries against an encrypted database on a Pi is
// exactly the shape of thing that feels slow for no reason.
func newestPerAlbum(ctx context.Context, v Vault) (map[int64]*Media, error) {
	rows, err := v.DB.QueryContext(ctx, `
		SELECT album_id, `+mediaColumns+` FROM (
			SELECT ai.album_id, `+prefixed("m", mediaColumns)+`,
			       row_number() OVER (
			           PARTITION BY ai.album_id
			           ORDER BY coalesce(m.taken_at, m.created_at) DESC, m.id DESC
			       ) AS rn
			  FROM album_items ai
			  JOIN media m ON m.id = ai.media_id
			 WHERE m.user_id = ? AND m.deleted_at IS NULL
		) WHERE rn = 1`, v.UserID)
	if err != nil {
		return nil, fmt.Errorf("library: newest per album: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]*Media)
	for rows.Next() {
		var albumID int64
		m, err := scanMediaWithPrefix(rows, &albumID)
		if err != nil {
			return nil, fmt.Errorf("library: scan album cover candidate: %w", err)
		}
		out[albumID] = m
	}
	return out, rows.Err()
}

// explicitCovers returns the covers someone chose by hand.
//
// A trashed photo is skipped by the join, so a cover that has since been
// deleted falls back to the automatic choice instead of leaving a hole.
func explicitCovers(ctx context.Context, v Vault) (map[int64]*Media, error) {
	rows, err := v.DB.QueryContext(ctx, `
		SELECT a.id, `+prefixed("m", mediaColumns)+`
		  FROM albums a
		  JOIN media m ON m.id = a.cover_media_id AND m.deleted_at IS NULL
		 WHERE a.user_id = ? AND a.deleted_at IS NULL`, v.UserID)
	if err != nil {
		return nil, fmt.Errorf("library: explicit covers: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]*Media)
	for rows.Next() {
		var albumID int64
		m, err := scanMediaWithPrefix(rows, &albumID)
		if err != nil {
			return nil, fmt.Errorf("library: scan explicit cover: %w", err)
		}
		out[albumID] = m
	}
	return out, rows.Err()
}

// resolveCovers fills in Cover for every album.
//
// The order is: whatever was chosen by hand, else this album's newest photo,
// else the best cover of a sub-album. That last step is what stops a parent
// like "Croatia" -- which holds no photos itself, only the years -- from
// showing an empty tile.
func resolveCovers(albums []*Album, explicit, newest map[int64]*Media) {
	children := make(map[int64][]*Album, len(albums))
	for _, a := range albums {
		if a.ParentID != nil {
			children[*a.ParentID] = append(children[*a.ParentID], a)
		}
	}

	resolved := make(map[int64]*Media, len(albums))
	var resolve func(a *Album, depth int) *Media
	resolve = func(a *Album, depth int) *Media {
		if m, done := resolved[a.ID]; done {
			return m
		}
		if depth >= maxAlbumDepth {
			return nil
		}
		// Marked before recursing: a cycle that reached the database anyway
		// would otherwise recurse until the stack gives out.
		resolved[a.ID] = nil

		var cover *Media
		if m, ok := explicit[a.ID]; ok {
			cover = m
		} else if m, ok := newest[a.ID]; ok {
			cover = m
		} else {
			// The newest photo among the sub-albums, so a parent shows the
			// most recent thing under it rather than an arbitrary child.
			for _, c := range children[a.ID] {
				if m := resolve(c, depth+1); m != nil {
					if cover == nil || m.SortTime().After(cover.SortTime()) {
						cover = m
					}
				}
			}
		}
		resolved[a.ID] = cover
		return cover
	}

	for _, a := range albums {
		a.Cover = resolve(a, 0)
	}
}

// totalCounts adds each album's own count to those of everything beneath it.
//
// A tile for "Croatia" showing 0 because the photos live in "2024" and "2025"
// would be technically true and useless.
func totalCounts(albums []*Album) {
	children := make(map[int64][]*Album, len(albums))
	for _, a := range albums {
		if a.ParentID != nil {
			children[*a.ParentID] = append(children[*a.ParentID], a)
		}
	}
	counted := make(map[int64]bool, len(albums))
	var total func(a *Album, depth int) int
	total = func(a *Album, depth int) int {
		if counted[a.ID] || depth >= maxAlbumDepth {
			return 0
		}
		counted[a.ID] = true
		n := a.ItemCount
		for _, c := range children[a.ID] {
			n += total(c, depth+1)
		}
		a.TotalCount = n
		return n
	}
	for _, a := range albums {
		if a.ParentID == nil {
			total(a, 0)
		}
	}
	// Anything not reached from a root -- only possible with a cycle -- still
	// needs a sensible number rather than zero.
	for _, a := range albums {
		if !counted[a.ID] {
			a.TotalCount = a.ItemCount
		}
	}
}

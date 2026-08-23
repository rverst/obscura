package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/rverst/obscura/internal/order"
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
	if _, err := albumRow(ctx, v, albumID); err != nil {
		return err
	}
	if parentID != nil {
		if *parentID == albumID {
			return ErrAlbumCycle
		}
		if _, err := albumRow(ctx, v, *parentID); err != nil {
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

	// A position only means something among siblings, so arriving in a new
	// level means taking a place at the end of it rather than carrying a key
	// that was ordered against albums it no longer sits beside.
	key, err := nextAlbumKey(ctx, v, parentID)
	if err != nil {
		return err
	}

	var arg any
	if parentID != nil {
		arg = *parentID
	}
	res, err := v.DB.ExecContext(ctx, `
		UPDATE albums SET parent_id = ?, sort_key = ?
		 WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, arg, key, albumID, v.UserID)
	if err != nil {
		return fmt.Errorf("library: move album: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveAlbumTo places an album between two of its siblings.
//
// Neighbours rather than an index, for the same reason photos are moved that
// way: an index describes a list that may already have changed, a pair of
// neighbours describes an intention that survives it.
//
// Unlike a photo move this rewrites every album in the level. Albums that
// predate the sort_key column all share the empty key, so there is frequently
// no gap to bisect; and a level holds tens of albums, not thousands, which is
// the whole reason the cheaper trick was needed for photos in the first place.
// Numbering the level outright is one pass that always leaves a total order.
func MoveAlbumTo(ctx context.Context, v Vault, albumID int64, afterID, beforeID *int64) error {
	album, err := albumRow(ctx, v, albumID)
	if err != nil {
		return err
	}

	siblings, err := albumLevel(ctx, v, album.ParentID)
	if err != nil {
		return err
	}

	// A neighbour from another level would describe a position that does not
	// exist here, and silently ignoring it would drop the album somewhere the
	// user did not point at.
	indexOf := func(id *int64) (int, error) {
		if id == nil {
			return -1, nil
		}
		if *id == albumID {
			return -1, fmt.Errorf("library: an album cannot be its own neighbour")
		}
		for i, s := range siblings {
			if s == *id {
				return i, nil
			}
		}
		return -1, ErrNotFound
	}
	afterAt, err := indexOf(afterID)
	if err != nil {
		return err
	}
	beforeAt, err := indexOf(beforeID)
	if err != nil {
		return err
	}

	was := -1
	ordered := make([]int64, 0, len(siblings))
	for i, s := range siblings {
		if s == albumID {
			was = i
			continue
		}
		ordered = append(ordered, s)
	}

	// The neighbour indices were read from the level with the album still in
	// it, so anything that sat behind it shifts by one once it is lifted out.
	shift := func(at int) int {
		if was >= 0 && at > was {
			return at - 1
		}
		return at
	}

	var at int
	switch {
	case afterAt >= 0:
		at = shift(afterAt) + 1
	case beforeAt >= 0:
		at = shift(beforeAt)
	default:
		// Neither neighbour means the very end -- what a drop past the last
		// tile and a drop into an empty level have in common.
		at = len(ordered)
	}
	if at > len(ordered) {
		at = len(ordered)
	}
	ordered = append(ordered[:at:at], append([]int64{albumID}, ordered[at:]...)...)

	tx, err := v.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("library: begin transaction: %w", err)
	}
	defer tx.Rollback()

	key := ""
	for _, id := range ordered {
		key, err = order.Between(key, "")
		if err != nil {
			return fmt.Errorf("library: album key after %q: %w", key, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE albums SET sort_key = ? WHERE id = ? AND user_id = ?`,
			key, id, v.UserID); err != nil {
			return fmt.Errorf("library: write album position: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("library: commit: %w", err)
	}
	return nil
}

// albumLevel returns the ids of one level, in the order it is displayed in.
func albumLevel(ctx context.Context, v Vault, parentID *int64) ([]int64, error) {
	var rows *sql.Rows
	var err error
	if parentID == nil {
		rows, err = v.DB.QueryContext(ctx, `
			SELECT id FROM albums
			 WHERE user_id = ? AND parent_id IS NULL AND deleted_at IS NULL
			 ORDER BY sort_key, title COLLATE NOCASE`, v.UserID)
	} else {
		rows, err = v.DB.QueryContext(ctx, `
			SELECT id FROM albums
			 WHERE user_id = ? AND parent_id = ? AND deleted_at IS NULL
			 ORDER BY sort_key, title COLLATE NOCASE`, v.UserID, *parentID)
	}
	if err != nil {
		return nil, fmt.Errorf("library: read album level: %w", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
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
		a, err := albumRow(ctx, v, cur)
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
// else the cover of the first sub-album. That last step is what stops a parent
// like "Croatia" -- which holds no photos itself, only the years -- from
// showing an empty tile.
//
// First rather than newest, because the sub-albums are shown in that order
// directly beneath the tile: the picture on the parent is then the picture on
// the tile the eye lands on next, instead of one from somewhere further down
// that looks arbitrary until you go looking for it.
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
			// children preserves the order the albums were listed in, so the
			// first sub-album that has a cover to give is the one on screen
			// first.
			for _, c := range children[a.ID] {
				if m := resolve(c, depth+1); m != nil {
					cover = m
					break
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

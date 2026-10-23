package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// mediaColumns is the single source of truth for scan order. Keeping the
// SELECT list and scanMedia next to each other is what stops a column being
// added in one place and silently misread in the other.
const mediaColumns = `
	id, kind, orig_filename, mime, bytes, sha256,
	width, height, orientation, duration_ms,
	taken_at, camera_make, camera_model, lens, iso, f_number, exposure, focal_len,
	gps_lat, gps_lon,
	blob_orig, blob_preview, blob_thumb, created_at, favorited_at`

func scanMedia(sc interface{ Scan(...any) error }) (*Media, error) {
	return scanMediaWithPrefix(sc)
}

// scanMediaWithPrefix scans a row whose media columns are preceded by extra
// destinations, for queries that select something alongside them -- an album's
// sort key, for instance.
func scanMediaWithPrefix(sc interface{ Scan(...any) error }, leading ...any) (*Media, error) {
	var (
		m           Media
		mime        sql.NullString
		width       sql.NullInt64
		height      sql.NullInt64
		orientation sql.NullInt64
		duration    sql.NullInt64
		takenAt     sql.NullInt64
		cameraMake  sql.NullString
		cameraModel sql.NullString
		lens        sql.NullString
		iso         sql.NullInt64
		fNumber     sql.NullFloat64
		exposure    sql.NullString
		focalLen    sql.NullFloat64
		gpsLat      sql.NullFloat64
		gpsLon      sql.NullFloat64
		preview     sql.NullString
		thumb       sql.NullString
		createdAt   int64
		favoritedAt sql.NullInt64
	)
	dest := append(leading,
		&m.ID, &m.Kind, &m.Filename, &mime, &m.Bytes, &m.SHA256,
		&width, &height, &orientation, &duration,
		&takenAt, &cameraMake, &cameraModel, &lens, &iso, &fNumber, &exposure, &focalLen,
		&gpsLat, &gpsLon,
		&m.BlobOrig, &preview, &thumb, &createdAt, &favoritedAt,
	)
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}

	m.MIME = mime.String
	m.Width = int(width.Int64)
	m.Height = int(height.Int64)
	m.Orientation = int(orientation.Int64)
	m.DurationMs = int(duration.Int64)
	m.CameraMake = cameraMake.String
	m.CameraModel = cameraModel.String
	m.Lens = lens.String
	m.ISO = int(iso.Int64)
	m.FNumber = fNumber.Float64
	m.Exposure = exposure.String
	m.FocalLen = focalLen.Float64
	m.BlobPreview = preview.String
	m.BlobThumb = thumb.String
	m.CreatedAt = time.Unix(createdAt, 0)

	if favoritedAt.Valid {
		t := time.Unix(favoritedAt.Int64, 0)
		m.FavoritedAt = &t
	}
	if takenAt.Valid {
		t := time.Unix(takenAt.Int64, 0).UTC()
		m.TakenAt = &t
	}
	// Only meaningful as a pair, and 0,0 is a real location, so both must be
	// present before either is reported.
	if gpsLat.Valid && gpsLon.Valid {
		lat, lon := gpsLat.Float64, gpsLon.Float64
		m.GPSLat, m.GPSLon = &lat, &lon
	}
	return &m, nil
}

func insertMedia(ctx context.Context, v Vault, m *Media) error {
	err := v.DB.QueryRowContext(ctx, `
		INSERT INTO media(
			user_id, kind, sha256, orig_filename, bytes, mime,
			width, height, duration_ms, orientation,
			taken_at, camera_make, camera_model, lens, iso, f_number, exposure, focal_len,
			gps_lat, gps_lon,
			blob_orig, blob_preview, blob_thumb, created_at)
		VALUES(?,?,?,?,?,?, ?,?,?,?, ?,?,?,?,?,?,?,?, ?,?, ?,?,?,?)
		RETURNING id`,
		v.UserID, m.Kind, m.SHA256, m.Filename, m.Bytes, m.MIME,
		nullInt(m.Width), nullInt(m.Height), nullInt(m.DurationMs), nullInt(m.Orientation),
		nullTime(m.TakenAt), nullStr(m.CameraMake), nullStr(m.CameraModel), nullStr(m.Lens),
		nullInt(m.ISO), nullFloat(m.FNumber), nullStr(m.Exposure), nullFloat(m.FocalLen),
		nullFloatPtr(m.GPSLat), nullFloatPtr(m.GPSLon),
		m.BlobOrig, nullStr(m.BlobPreview), nullStr(m.BlobThumb), m.CreatedAt.Unix(),
	).Scan(&m.ID)
	if err != nil {
		return fmt.Errorf("library: insert media: %w", err)
	}
	return nil
}

// FindBySHA256 looks up media by the hash of its plaintext.
//
// This is what makes re-uploading the same photo cheap: the client hashes
// locally and asks before sending a single byte.
func FindBySHA256(ctx context.Context, v Vault, sum []byte) (*Media, error) {
	m, err := scanMedia(v.DB.QueryRowContext(ctx,
		`SELECT `+mediaColumns+` FROM media
		  WHERE user_id = ? AND sha256 = ? AND deleted_at IS NULL`, v.UserID, sum))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("library: lookup by hash: %w", err)
	}
	return m, nil
}

// Get returns one media record.
func Get(ctx context.Context, v Vault, id int64) (*Media, error) {
	m, err := scanMedia(v.DB.QueryRowContext(ctx,
		`SELECT `+mediaColumns+` FROM media
		  WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, id, v.UserID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("library: get media: %w", err)
	}
	return m, nil
}

// ListOptions controls paging through the library.
type ListOptions struct {
	Limit int
	// Before pages backwards through the timeline. Keyset paging rather than
	// OFFSET: OFFSET has to walk every skipped row, which on a Pi is felt by
	// the time someone scrolls into a five-figure library.
	Before   *time.Time
	BeforeID int64

	// FavoritesOnly narrows the timeline to marked photos. A filter on the
	// query rather than something the client sifts out of a page: paging and
	// filtering client-side would return short pages of unpredictable length
	// and break the "load more" trigger.
	FavoritesOnly bool

	// UnfiledOnly narrows the timeline to media that is in no album at all:
	// the photographs still waiting to be sorted.
	UnfiledOnly bool
}

// filter is the WHERE clause the filters in opt share between List and
// CountList, so the total shown beside a page always counts the same set.
func (opt ListOptions) filter(v Vault) (string, []any) {
	where := `user_id = ? AND deleted_at IS NULL`
	args := []any{v.UserID}
	if opt.FavoritesOnly {
		where += ` AND favorited_at IS NOT NULL`
	}
	if opt.UnfiledOnly {
		// Deleting an album cascades its album_items away, so its photos
		// count as unfiled again without anything having to notice.
		where += ` AND NOT EXISTS (SELECT 1 FROM album_items ai WHERE ai.media_id = media.id)`
	}
	return where, args
}

// List returns media newest first.
//
// Sorted by taken_at with created_at as the fallback, because a photo imported
// today may well have been taken in 2009 and belongs in 2009 in the timeline.
// id breaks ties so paging cannot loop on a burst of shots sharing a timestamp.
func List(ctx context.Context, v Vault, opt ListOptions) ([]*Media, error) {
	if opt.Limit <= 0 || opt.Limit > 500 {
		opt.Limit = 100
	}

	where, args := opt.filter(v)
	if opt.Before != nil {
		where += ` AND (coalesce(taken_at, created_at), id) < (?, ?)`
		args = append(args, opt.Before.Unix(), opt.BeforeID)
	}
	args = append(args, opt.Limit)

	rows, err := v.DB.QueryContext(ctx,
		`SELECT `+mediaColumns+` FROM media
		  WHERE `+where+`
		  ORDER BY coalesce(taken_at, created_at) DESC, id DESC
		  LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("library: list media: %w", err)
	}
	defer rows.Close()

	var out []*Media
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("library: scan media: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SortTime is the timestamp a record is placed under in the timeline.
func (m *Media) SortTime() time.Time {
	if m.TakenAt != nil {
		return *m.TakenAt
	}
	return m.CreatedAt
}

// Count returns how many items are in the library.
func Count(ctx context.Context, v Vault) (int, error) {
	var n int
	if err := v.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM media WHERE user_id = ? AND deleted_at IS NULL`,
		v.UserID).Scan(&n); err != nil {
		return 0, fmt.Errorf("library: count media: %w", err)
	}
	return n, nil
}

// CountList counts what List would return across all pages, ignoring the
// cursor and the limit.
func CountList(ctx context.Context, v Vault, opt ListOptions) (int, error) {
	where, args := opt.filter(v)
	var n int
	if err := v.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM media WHERE `+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("library: count media: %w", err)
	}
	return n, nil
}

// --- SQL null helpers -------------------------------------------------------
//
// The schema stores "unknown" as NULL rather than 0, because 0 is a legitimate
// value for several of these columns -- ISO 0 does not exist, but a focal
// length or a GPS coordinate of 0 does.

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullFloat(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullFloatPtr(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

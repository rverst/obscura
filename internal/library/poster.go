package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/rverst/obscura/internal/media"
)

// ErrNotVideo reports a poster request for something that is not a video.
var ErrNotVideo = errors.New("library: not a video")

// ErrNoVideoTools reports that ffmpeg is not installed, so no still can be cut.
var ErrNoVideoTools = errors.New("library: video tools not available")

// ErrBadOffset reports a poster offset outside the clip.
var ErrBadOffset = errors.New("library: offset outside the video")

// SetPoster replaces a video's stills with the frame at the given offset.
//
// The frame one second in is a guess, and for a clip that opens on a pan or a
// pocket it is a bad one. Both derivatives are cut from the same frame, so the
// tile and the lightbox never disagree about which moment stands for the clip.
//
// New blobs are written before the row changes and the old ones removed after,
// the same order Purge keeps: a crash in between leaves an unreferenced file,
// never a row pointing at nothing.
func (l *Library) SetPoster(ctx context.Context, v Vault, mediaID int64, at time.Duration) (*Media, error) {
	m, err := Get(ctx, v, mediaID)
	if err != nil {
		return nil, err
	}
	if m.Kind != KindVideo {
		return nil, ErrNotVideo
	}
	if l.video == nil {
		return nil, ErrNoVideoTools
	}
	// Only checked against a known duration: a clip ffprobe could not measure
	// may still decode, and ffmpeg reports a seek past the end on its own.
	if at < 0 || (m.DurationMs > 0 && at > time.Duration(m.DurationMs)*time.Millisecond) {
		return nil, ErrBadOffset
	}

	// The same bound as ingesting: this is a decode, and an impatient user
	// clicking through frames must not run more of them than an import would.
	select {
	case l.sem <- struct{}{}:
		defer func() { <-l.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	src, err := l.blobs.Open(v.DEK, m.BlobOrig)
	if err != nil {
		return nil, fmt.Errorf("library: reopen video: %w", err)
	}
	defer src.Close()

	var created []string
	cleanup := func() {
		for _, id := range created {
			_ = l.blobs.Remove(id)
		}
	}
	ids := map[int]string{}
	for _, size := range []int{media.ThumbSize, media.PreviewSize} {
		id, err := l.derivePoster(v, func(w io.Writer) error {
			return l.video.PosterAt(ctx, src, src.Size, w, size, at)
		})
		if err != nil {
			cleanup()
			if errors.Is(err, media.ErrNotAVideo) {
				return nil, fmt.Errorf("%w: %v", ErrBadOffset, err)
			}
			return nil, err
		}
		created = append(created, id)
		ids[size] = id
	}

	res, err := v.DB.ExecContext(ctx, `
		UPDATE media SET blob_thumb = ?, blob_preview = ?
		 WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
		ids[media.ThumbSize], ids[media.PreviewSize], mediaID, v.UserID)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("library: store poster: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Trashed while the frame was being cut.
		cleanup()
		return nil, ErrNotFound
	}

	for _, old := range []string{m.BlobThumb, m.BlobPreview} {
		if old != "" {
			if err := l.blobs.Remove(old); err != nil {
				// The row already points at the new stills, so nothing is
				// inconsistent; the old file is merely wasted space.
				return nil, fmt.Errorf("library: poster replaced, but an old still remains: %w", err)
			}
		}
	}
	m.BlobThumb, m.BlobPreview = ids[media.ThumbSize], ids[media.PreviewSize]
	return m, nil
}

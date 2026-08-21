// Package library turns an uploaded file into a media record: derivatives,
// metadata, and the database row that ties them together.
//
// It sits above blob (encrypted storage) and media (image processing), and
// below the HTTP layer. Everything it needs from a session -- the database
// handle, the DEK, the user -- arrives as a Vault, so this package does not
// depend on authentication.
package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"strings"
	"time"

	"github.com/rverst/obscura/internal/blob"
	"github.com/rverst/obscura/internal/media"
)

// Kinds of media.
const (
	KindPhoto = "photo"
	KindVideo = "video"
)

// ErrDuplicate reports that identical content is already in the library.
var ErrDuplicate = errors.New("library: already present")

// ErrNotFound reports that no media record matches.
var ErrNotFound = errors.New("library: not found")

// Vault is the per-request context: an unlocked database and the key that
// opens the blobs belonging to it.
type Vault struct {
	DB     *sql.DB
	DEK    []byte
	UserID int64
}

// Library owns the process-wide resources for ingesting media.
type Library struct {
	blobs  *blob.Store
	thumbs *media.Thumbnailer
	// video is nil when ffmpeg is not installed. Videos are then stored and
	// served as normal, they simply get no poster frame -- playback needs
	// nothing server-side beyond range requests.
	video *media.Video

	// sem bounds concurrent processing. libvips needs roughly 30MB per image;
	// unbounded parallelism reliably kills a 2GB Pi during a bulk import, and
	// the machine has four cores at best.
	sem chan struct{}
}

// DefaultConcurrency is how many files are processed at once.
const DefaultConcurrency = 2

// New wires a library together. video may be nil.
func New(blobs *blob.Store, thumbs *media.Thumbnailer, video *media.Video, concurrency int) *Library {
	if concurrency < 1 {
		concurrency = DefaultConcurrency
	}
	return &Library{
		blobs:  blobs,
		thumbs: thumbs,
		video:  video,
		sem:    make(chan struct{}, concurrency),
	}
}

// Media is a stored photo or video.
type Media struct {
	ID       int64
	Kind     string
	Filename string
	MIME     string
	Bytes    int64
	SHA256   []byte

	Width       int
	Height      int
	Orientation int
	DurationMs  int

	TakenAt     *time.Time
	CameraMake  string
	CameraModel string
	Lens        string
	ISO         int
	FNumber     float64
	Exposure    string
	FocalLen    float64
	GPSLat      *float64
	GPSLon      *float64

	BlobOrig    string
	BlobPreview string
	BlobThumb   string

	CreatedAt time.Time
}

// Ingestion describes a freshly stored original awaiting processing.
type Ingestion struct {
	BlobID   string
	Filename string
	Size     int64
	SHA256   []byte
}

// Ingest derives thumbnails and metadata for an already-stored blob and records
// it in the database.
//
// The original is expected to be in the blob store already: the upload streams
// straight into it so plaintext never lands on disk. On failure every blob this
// created, including the caller's original, is removed -- a half-ingested file
// with no database row would otherwise occupy space that nothing ever reclaims.
func (l *Library) Ingest(ctx context.Context, v Vault, in Ingestion) (*Media, error) {
	// Bound concurrency before touching anything expensive.
	select {
	case l.sem <- struct{}{}:
		defer func() { <-l.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if existing, err := FindBySHA256(ctx, v, in.SHA256); err == nil {
		// The client should have asked before uploading, but two uploads can
		// race and a client can simply not bother.
		_ = l.blobs.Remove(in.BlobID)
		return existing, ErrDuplicate
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	// The unique index on (user_id, sha256) covers trashed rows, so uploading
	// a photo that is sitting in the trash would otherwise fail on a constraint
	// violation -- which from the outside looks like a broken upload rather
	// than the photo already being there. Bring it back instead.
	if id, err := findTrashedBySHA256(ctx, v, in.SHA256); err == nil {
		_ = l.blobs.Remove(in.BlobID)
		if err := Restore(ctx, v, id); err != nil {
			return nil, err
		}
		restored, err := Get(ctx, v, id)
		if err != nil {
			return nil, err
		}
		return restored, ErrDuplicate
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	m := &Media{
		Kind:      kindOf(in.Filename),
		Filename:  filepath.Base(in.Filename),
		MIME:      mimeOf(in.Filename),
		Bytes:     in.Size,
		SHA256:    in.SHA256,
		BlobOrig:  in.BlobID,
		CreatedAt: time.Now(),
	}

	// Everything created below is rolled back unless we reach the end.
	created := []string{in.BlobID}
	committed := false
	defer func() {
		if !committed {
			for _, id := range created {
				_ = l.blobs.Remove(id)
			}
		}
	}()

	switch m.Kind {
	case KindPhoto:
		if err := l.processPhoto(ctx, v, m, &created); err != nil {
			return nil, err
		}
	case KindVideo:
		if err := l.processVideo(ctx, v, m, &created); err != nil {
			return nil, err
		}
	}

	if err := insertMedia(ctx, v, m); err != nil {
		return nil, err
	}
	committed = true
	return m, nil
}

// processPhoto fills in metadata and builds the derivatives.
func (l *Library) processPhoto(ctx context.Context, v Vault, m *Media, created *[]string) error {
	src, err := l.blobs.Open(v.DEK, m.BlobOrig)
	if err != nil {
		return fmt.Errorf("library: reopen original: %w", err)
	}
	defer src.Close()

	meta, err := media.ExtractMetadata(src)
	if err != nil {
		return fmt.Errorf("library: read metadata: %w", err)
	}
	m.Orientation = meta.Orientation
	m.TakenAt = meta.TakenAt
	m.CameraMake = meta.CameraMake
	m.CameraModel = meta.CameraModel
	m.Lens = meta.Lens
	m.ISO = meta.ISO
	m.FNumber = meta.FNumber
	m.Exposure = meta.Exposure
	m.FocalLen = meta.FocalLen
	m.GPSLat, m.GPSLon = meta.GPSLat, meta.GPSLon

	m.Width, m.Height = meta.Width, meta.Height
	if m.Width == 0 || m.Height == 0 {
		// EXIF did not carry them; read the image header instead.
		if w, h, err := media.Dimensions(src); err == nil {
			m.Width, m.Height = w, h
		}
	}
	// Derivatives come out already rotated, so the stored dimensions must
	// describe the displayed image -- otherwise the grid lays portrait photos
	// out in landscape boxes and every row jumps once the image loads.
	if media.SwapsDimensions(m.Orientation) {
		m.Width, m.Height = m.Height, m.Width
	}

	for _, d := range []struct {
		size int
		dst  *string
	}{
		{media.ThumbSize, &m.BlobThumb},
		{media.PreviewSize, &m.BlobPreview},
	} {
		id, err := l.derive(ctx, v, src, d.size)
		if err != nil {
			return err
		}
		*d.dst = id
		*created = append(*created, id)
	}
	return nil
}

// derive generates one scaled JPEG and stores it as its own blob.
func (l *Library) derive(ctx context.Context, v Vault, src io.ReadSeeker, size int) (string, error) {
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("library: rewind original: %w", err)
	}
	w, err := l.blobs.Create(v.DEK)
	if err != nil {
		return "", err
	}
	defer w.Abort()

	// Straight from the decrypting reader into vipsthumbnail and back into an
	// encrypting writer: the scaled image exists only in pipes and memory.
	if err := l.thumbs.Thumbnail(ctx, src, w, size); err != nil {
		return "", fmt.Errorf("library: %dpx derivative: %w", size, err)
	}
	if _, _, err := w.Commit(); err != nil {
		return "", err
	}
	return w.ID(), nil
}

// kindOf classifies by extension.
//
// Content sniffing would be better, but the extension is what the client sends
// and what every camera and phone sets correctly. A misclassified file is not a
// safety problem here: it only means the wrong derivatives are attempted.
func kindOf(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".mp4", ".mov", ".m4v", ".avi", ".mkv", ".webm", ".3gp", ".mts":
		return KindVideo
	default:
		return KindPhoto
	}
}

func mimeOf(filename string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); t != "" {
		return t
	}
	return "application/octet-stream"
}

// processVideo probes a video and extracts a poster frame.
//
// A failure here is not fatal. Nothing is transcoded and the original is served
// as uploaded, so a clip whose first frames ffmpeg cannot decode still uploads,
// still plays, and simply has no still image -- which is much better than
// refusing the file.
func (l *Library) processVideo(ctx context.Context, v Vault, m *Media, created *[]string) error {
	if l.video == nil {
		return nil // ffmpeg not installed; see media.NewVideo
	}

	src, err := l.blobs.Open(v.DEK, m.BlobOrig)
	if err != nil {
		return fmt.Errorf("library: reopen video: %w", err)
	}
	defer src.Close()

	if info, err := l.video.Probe(ctx, src, src.Size); err == nil {
		m.DurationMs = info.DurationMs
		m.Width, m.Height = info.Width, info.Height
	}

	for _, d := range []struct {
		size int
		dst  *string
	}{
		{media.ThumbSize, &m.BlobThumb},
		{media.PreviewSize, &m.BlobPreview},
	} {
		id, err := l.derivePoster(ctx, v, src, d.size)
		if err != nil {
			// Give up on stills but keep the video.
			return nil
		}
		*d.dst = id
		*created = append(*created, id)
	}
	return nil
}

// derivePoster stores one still from the video as its own blob.
func (l *Library) derivePoster(ctx context.Context, v Vault, src *blob.Reader, size int) (string, error) {
	w, err := l.blobs.Create(v.DEK)
	if err != nil {
		return "", err
	}
	defer w.Abort()

	if err := l.video.Poster(ctx, src, src.Size, w, size); err != nil {
		return "", err
	}
	if _, _, err := w.Commit(); err != nil {
		return "", err
	}
	return w.ID(), nil
}

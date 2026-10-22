package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrNoVideoTools reports that ffmpeg is not installed.
var ErrNoVideoTools = errors.New("media: ffmpeg not available")

// ErrNotAVideo reports that the input could not be read as video.
var ErrNotAVideo = errors.New("media: not a decodable video")

// posterTimeout bounds a single extraction. A corrupt file must not pin a
// worker, and on a Pi there are only a couple of them.
const posterTimeout = 2 * time.Minute

// Video extracts poster frames and metadata by running ffmpeg as a subprocess.
//
// Nothing is transcoded, ever: the Pi 5 has no hardware encoder at all, so
// re-encoding would run entirely in software and occupy the machine for minutes
// per file. Originals are served as uploaded and the browser decodes them.
type Video struct {
	ffmpeg  string
	ffprobe string
	timeout time.Duration
}

// NewVideo locates ffmpeg and ffprobe.
//
// Returns ErrNoVideoTools when they are missing rather than failing startup:
// obscura works without them, it simply cannot show a still for a video. That
// keeps a slim deployment possible, since ffmpeg is 78MB of mostly encoder
// libraries this program never invokes.
func NewVideo() (*Video, error) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, ErrNoVideoTools
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		return nil, ErrNoVideoTools
	}
	return &Video{ffmpeg: ffmpegPath, ffprobe: ffprobePath, timeout: posterTimeout}, nil
}

// Info is what a probe can tell us about a video.
type Info struct {
	DurationMs int
	Width      int
	Height     int
	// Rotation from the container's display matrix, in degrees. Phones record
	// upright video as landscape plus a rotation flag, so ignoring this would
	// store every portrait clip as landscape.
	Rotation int
}

// Probe reads duration and dimensions.
func (v *Video) Probe(ctx context.Context, src io.ReadSeeker, size int64) (Info, error) {
	url, stop, err := serveSeekable(src, size)
	if err != nil {
		return Info{}, err
	}
	defer stop()

	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, v.ffprobe,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height:stream_side_data=rotation:format=duration",
		"-of", "json",
		url,
	)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Info{}, fmt.Errorf("%w: %s", ErrNotAVideo, firstLine(strings.TrimSpace(stderr.String())))
	}

	var probe struct {
		Streams []struct {
			Width     int `json:"width"`
			Height    int `json:"height"`
			SideDatas []struct {
				Rotation int `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out.Bytes(), &probe); err != nil {
		return Info{}, fmt.Errorf("media: parse ffprobe output: %w", err)
	}
	if len(probe.Streams) == 0 {
		return Info{}, fmt.Errorf("%w: no video stream", ErrNotAVideo)
	}

	info := Info{Width: probe.Streams[0].Width, Height: probe.Streams[0].Height}
	if secs, err := strconv.ParseFloat(probe.Format.Duration, 64); err == nil && secs > 0 {
		info.DurationMs = int(secs * 1000)
	}
	for _, sd := range probe.Streams[0].SideDatas {
		if sd.Rotation != 0 {
			// ffprobe reports the counter-clockwise correction; normalise to a
			// positive quarter-turn count.
			info.Rotation = ((-sd.Rotation)%360 + 360) % 360
			break
		}
	}
	// A quarter turn means the stored dimensions are the wrong way round for
	// display, exactly as with EXIF orientation on a photo.
	if info.Rotation == 90 || info.Rotation == 270 {
		info.Width, info.Height = info.Height, info.Width
	}
	return info, nil
}

// Poster writes a JPEG still from the video to dst.
//
// The frame is taken a moment into the clip rather than at zero: the first
// frame of a phone recording is very often black or a blurred pan, which makes
// for a useless thumbnail. If seeking that far fails -- a clip shorter than the
// offset -- it falls back to the first frame.
func (v *Video) Poster(ctx context.Context, src io.ReadSeeker, size int64, dst io.Writer, maxEdge int) error {
	if maxEdge <= 0 {
		return fmt.Errorf("media: invalid size %d", maxEdge)
	}
	for _, seek := range []string{"1", "0"} {
		err := v.extract(ctx, src, size, dst, maxEdge, seek)
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrNotAVideo) && seek != "0" {
			continue
		}
		return err
	}
	return ErrNotAVideo
}

// PosterAt writes a JPEG still of the frame at the given offset to dst.
//
// This is the still someone chose, so unlike Poster there is no fallback: a
// different frame than the one asked for would be a silent substitution. An
// offset past the end produces no frame at all, which is reported as
// ErrNotAVideo rather than as an empty image.
func (v *Video) PosterAt(ctx context.Context, src io.ReadSeeker, size int64, dst io.Writer, maxEdge int, at time.Duration) error {
	if maxEdge <= 0 {
		return fmt.Errorf("media: invalid size %d", maxEdge)
	}
	if at < 0 {
		return fmt.Errorf("media: invalid offset %s", at)
	}
	cw := &countingWriter{w: dst}
	// Milliseconds are as fine as a player reports a position anyway.
	if err := v.extract(ctx, src, size, cw, maxEdge, fmt.Sprintf("%.3f", at.Seconds())); err != nil {
		return err
	}
	if cw.n == 0 {
		return fmt.Errorf("%w: no frame at %s", ErrNotAVideo, at)
	}
	return nil
}

// countingWriter notices whether ffmpeg wrote anything. Seeking past the end
// is not an error to ffmpeg: it exits cleanly having encoded nothing.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func (v *Video) extract(ctx context.Context, src io.ReadSeeker, size int64, dst io.Writer, maxEdge int, seek string) error {
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("media: rewind video: %w", err)
	}
	url, stop, err := serveSeekable(src, size)
	if err != nil {
		return err
	}
	defer stop()

	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, v.ffmpeg,
		"-v", "error",
		// Before -i, so the seek happens on the input and only the frames near
		// the target are decoded.
		"-ss", seek,
		"-i", url,
		"-frames:v", "1",
		// Fit inside the box without upscaling, preserving aspect ratio.
		"-vf", fmt.Sprintf("scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease", maxEdge, maxEdge),
		"-f", "image2",
		"-vcodec", "mjpeg",
		"-q:v", "4",
		"pipe:1",
	)
	var stderr bytes.Buffer
	cmd.Stdout = dst
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("media: ffmpeg timed out after %s", v.timeout)
		}
		msg := firstLine(strings.TrimSpace(stderr.String()))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%w: %s", ErrNotAVideo, msg)
	}
	return nil
}

// serveSeekable exposes a reader to a subprocess over HTTP on localhost.
//
// This exists because ffmpeg needs to seek. An MP4 written without faststart --
// which is ffmpeg's own default and common from cameras -- keeps its index at
// the end of the file, so reading from a pipe fails outright: measured, not
// assumed. The blob on disk is encrypted, so ffmpeg cannot open it directly
// either.
//
// The obvious alternative, decrypting to a temporary file, would copy the whole
// video: fatal for a multi-gigabyte clip on a Pi. Over HTTP with range support
// ffmpeg fetches only what it needs -- 2.8MB of a 111MB clip in three requests,
// measured -- and memory stays bounded regardless of file size.
//
// The listener is on the loopback interface only, lives for the duration of one
// extraction, and its path is 32 random bytes, so another local process would
// have to guess 256 bits to reach it.
func serveSeekable(src io.ReadSeeker, size int64) (url string, stop func(), err error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return "", nil, fmt.Errorf("media: generate token: %w", err)
	}
	path := "/" + hex.EncodeToString(token)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("media: listen on loopback: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		// A fresh section reader per request: ffmpeg issues several overlapping
		// range requests and they must not share a seek position.
		http.ServeContent(w, r, "video", time.Time{}, io.NewSectionReader(readerAt(src), 0, size))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()

	return fmt.Sprintf("http://%s%s", ln.Addr().String(), path), func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}, nil
}

// readerAt adapts a ReadSeeker to ReaderAt.
//
// Serialised, because the underlying reader has one seek position and ffmpeg's
// requests overlap in time. Contention is irrelevant here: this serves exactly
// one subprocess.
type seekReaderAt struct {
	src io.ReadSeeker
	mu  chan struct{}
}

func readerAt(src io.ReadSeeker) io.ReaderAt {
	if ra, ok := src.(io.ReaderAt); ok {
		return ra
	}
	return &seekReaderAt{src: src, mu: make(chan struct{}, 1)}
}

func (s *seekReaderAt) ReadAt(p []byte, off int64) (int, error) {
	s.mu <- struct{}{}
	defer func() { <-s.mu }()

	if _, err := s.src.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	return io.ReadFull(s.src, p)
}

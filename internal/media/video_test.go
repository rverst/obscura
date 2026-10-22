package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newVideo(t *testing.T) *Video {
	t.Helper()
	v, err := NewVideo()
	if errors.Is(err, ErrNoVideoTools) {
		t.Skip("ffmpeg not installed -- run these through `make test` (Docker)")
	}
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// makeVideo renders a short clip with ffmpeg itself.
//
// faststart controls where the index lands. Without it the index sits at the
// end of the file, which is ffmpeg's own default and common from cameras -- and
// the case that makes a pipe useless, so both variants have to be covered.
func makeVideo(t *testing.T, seconds string, w, h int, faststart bool) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "v.mp4")

	args := []string{
		"-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=" + itoa(w) + "x" + itoa(h) + ":duration=" + seconds + ":rate=15",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
	}
	if faststart {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, path)

	cmd := exec.Command("ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot render a test clip: %v: %s", err, stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestPosterFromVideo(t *testing.T) {
	v := newVideo(t)
	data := makeVideo(t, "3", 640, 480, true)

	var out bytes.Buffer
	if err := v.Poster(context.Background(), bytes.NewReader(data), int64(len(data)), &out, ThumbSize); err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("poster is not a decodable JPEG: %v", err)
	}
	if cfg.Width > ThumbSize || cfg.Height > ThumbSize {
		t.Fatalf("poster is %dx%d, exceeds the %dpx box", cfg.Width, cfg.Height, ThumbSize)
	}
	// Aspect ratio must survive, as with photo thumbnails.
	if ratio := float64(cfg.Width) / float64(cfg.Height); ratio < 1.28 || ratio > 1.39 {
		t.Fatalf("aspect ratio %.3f, want ~1.333 (%dx%d)", ratio, cfg.Width, cfg.Height)
	}
}

// The case that decided the whole design. An MP4 whose index sits at the end
// cannot be read from a pipe at all; it works here because ffmpeg is given a
// seekable source over HTTP.
func TestPosterFromVideoWithIndexAtTheEnd(t *testing.T) {
	v := newVideo(t)
	data := makeVideo(t, "3", 640, 480, false) // no faststart

	var out bytes.Buffer
	if err := v.Poster(context.Background(), bytes.NewReader(data), int64(len(data)), &out, ThumbSize); err != nil {
		t.Fatalf("index-at-end video failed, which is the case a pipe cannot handle: %v", err)
	}
	if _, err := jpeg.DecodeConfig(bytes.NewReader(out.Bytes())); err != nil {
		t.Fatalf("poster is not a decodable JPEG: %v", err)
	}
}

// A clip shorter than the seek offset must still produce a still rather than
// nothing, by falling back to the first frame.
func TestPosterFromVeryShortClip(t *testing.T) {
	v := newVideo(t)
	data := makeVideo(t, "0.4", 320, 240, true)

	var out bytes.Buffer
	if err := v.Poster(context.Background(), bytes.NewReader(data), int64(len(data)), &out, ThumbSize); err != nil {
		t.Fatalf("short clip produced no poster: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("poster is empty")
	}
}

func TestProbe(t *testing.T) {
	v := newVideo(t)
	data := makeVideo(t, "3", 640, 480, true)

	info, err := v.Probe(context.Background(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 640 || info.Height != 480 {
		t.Fatalf("dimensions %dx%d, want 640x480", info.Width, info.Height)
	}
	// Three seconds, with generous slack for container rounding.
	if info.DurationMs < 2500 || info.DurationMs > 3500 {
		t.Fatalf("DurationMs = %d, want roughly 3000", info.DurationMs)
	}
}

func TestProbeRejectsNonVideo(t *testing.T) {
	v := newVideo(t)
	data := []byte("this is definitely not a video")
	if _, err := v.Probe(context.Background(), bytes.NewReader(data), int64(len(data))); !errors.Is(err, ErrNotAVideo) {
		t.Fatalf("want ErrNotAVideo, got: %v", err)
	}
}

func TestPosterRejectsNonVideo(t *testing.T) {
	v := newVideo(t)
	data := []byte("still not a video")
	var out bytes.Buffer
	if err := v.Poster(context.Background(), bytes.NewReader(data), int64(len(data)), &out, ThumbSize); !errors.Is(err, ErrNotAVideo) {
		t.Fatalf("want ErrNotAVideo, got: %v", err)
	}
}

func TestPosterHonoursCancellation(t *testing.T) {
	v := newVideo(t)
	data := makeVideo(t, "2", 320, 240, true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := v.Poster(ctx, bytes.NewReader(data), int64(len(data)), &out, ThumbSize); err == nil {
		t.Fatal("a cancelled context still produced a poster")
	}
}

// The helper server exists to hand a subprocess a seekable view of decrypted
// content without ever writing it to disk.
func TestServeSeekableIsLoopbackAndUnguessable(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 1024)
	url, stop, err := serveSeekable(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("url = %q, must be bound to loopback only", url)
	}
	// 32 random bytes as hex: another local process cannot find it by guessing.
	path := url[strings.LastIndex(url, "/")+1:]
	if len(path) != 64 {
		t.Fatalf("path token is %d characters, want 64 hex characters", len(path))
	}
}

func TestServeSeekableServesRanges(t *testing.T) {
	data := make([]byte, 4096)
	for i := range data {
		data[i] = byte(i)
	}
	url, stop, err := serveSeekable(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	req, _ := newRangeRequest(url, 1000, 1099)
	resp, err := httpDo(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 206 {
		t.Fatalf("status %d, want 206 -- without ranges ffmpeg cannot seek", resp.StatusCode)
	}
	got := make([]byte, 100)
	if _, err := readFull(resp.Body, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data[1000:1100]) {
		t.Fatal("the served range is not the requested one")
	}
}

// Small helpers so the tests above read as intent rather than plumbing.
func newRangeRequest(url string, from, to int) (*http.Request, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to))
	return req, nil
}

func httpDo(req *http.Request) (*http.Response, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	return client.Do(req)
}

func readFull(r io.Reader, p []byte) (int, error) { return io.ReadFull(r, p) }

func TestServeSeekableStopsListening(t *testing.T) {
	data := []byte("hello")
	url, stop, err := serveSeekable(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	stop()

	// Nothing may keep listening once an extraction is done.
	req, _ := newRangeRequest(url, 0, 1)
	if resp, err := httpDo(req); err == nil {
		resp.Body.Close()
		t.Fatal("the helper server is still reachable after stop()")
	}
}

// A chosen frame has to be that frame, not the automatic one: two offsets in a
// moving test pattern must give two different stills.
func TestPosterAtTakesTheRequestedFrame(t *testing.T) {
	v := newVideo(t)
	data := makeVideo(t, "3", 320, 240, true)

	still := func(at time.Duration) []byte {
		var out bytes.Buffer
		if err := v.PosterAt(context.Background(), bytes.NewReader(data), int64(len(data)), &out, ThumbSize, at); err != nil {
			t.Fatalf("PosterAt(%s): %v", at, err)
		}
		if _, err := jpeg.DecodeConfig(bytes.NewReader(out.Bytes())); err != nil {
			t.Fatalf("PosterAt(%s) is not a decodable JPEG: %v", at, err)
		}
		return out.Bytes()
	}
	if bytes.Equal(still(200*time.Millisecond), still(2500*time.Millisecond)) {
		t.Fatal("two different offsets produced the same still")
	}
}

// Past the end ffmpeg exits cleanly having written nothing. That must surface
// as an error, not as an empty blob that every tile then fails to decode.
func TestPosterAtPastTheEndFails(t *testing.T) {
	v := newVideo(t)
	data := makeVideo(t, "1", 320, 240, true)

	var out bytes.Buffer
	err := v.PosterAt(context.Background(), bytes.NewReader(data), int64(len(data)), &out, ThumbSize, 10*time.Second)
	if !errors.Is(err, ErrNotAVideo) {
		t.Fatalf("err = %v, want ErrNotAVideo", err)
	}
}

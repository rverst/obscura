package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func newThumbnailer(t *testing.T) *Thumbnailer {
	t.Helper()
	if _, err := exec.LookPath("vipsthumbnail"); err != nil {
		t.Skip("vipsthumbnail not installed -- run these through `make test` (Docker)")
	}
	th, err := NewThumbnailer()
	if err != nil {
		t.Fatal(err)
	}
	return th
}

func decodeSize(t *testing.T, b []byte) (int, int) {
	t.Helper()
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("output is not a decodable JPEG: %v", err)
	}
	return cfg.Width, cfg.Height
}

func TestThumbnailFitsInsideBox(t *testing.T) {
	th := newThumbnailer(t)
	src := jpegNoExif(t, 2000, 1000) // 2:1 landscape

	var out bytes.Buffer
	if err := th.Thumbnail(context.Background(), bytes.NewReader(src), &out, ThumbSize); err != nil {
		t.Fatal(err)
	}
	w, h := decodeSize(t, out.Bytes())
	if w > ThumbSize || h > ThumbSize {
		t.Fatalf("%dx%d exceeds the %dpx box", w, h, ThumbSize)
	}
	// Aspect ratio must survive: cropping would silently cut people out of
	// their own photos.
	if got, want := float64(w)/float64(h), 2.0; got < want*0.98 || got > want*1.02 {
		t.Fatalf("aspect ratio %.3f, want ~%.1f (%dx%d)", got, want, w, h)
	}
	if w != ThumbSize {
		t.Fatalf("long edge is %d, want %d", w, ThumbSize)
	}
}

func TestThumbnailPortrait(t *testing.T) {
	th := newThumbnailer(t)
	var out bytes.Buffer
	if err := th.Thumbnail(context.Background(),
		bytes.NewReader(jpegNoExif(t, 800, 1600)), &out, ThumbSize); err != nil {
		t.Fatal(err)
	}
	w, h := decodeSize(t, out.Bytes())
	if h != ThumbSize {
		t.Fatalf("long edge (height) is %d, want %d", h, ThumbSize)
	}
	if w > h {
		t.Fatalf("a portrait image came back landscape: %dx%d", w, h)
	}
}

func TestPreviewSizeIsLarger(t *testing.T) {
	th := newThumbnailer(t)
	src := jpegNoExif(t, 4000, 3000)

	var thumb, preview bytes.Buffer
	if err := th.Thumbnail(context.Background(), bytes.NewReader(src), &thumb, ThumbSize); err != nil {
		t.Fatal(err)
	}
	if err := th.Thumbnail(context.Background(), bytes.NewReader(src), &preview, PreviewSize); err != nil {
		t.Fatal(err)
	}
	tw, _ := decodeSize(t, thumb.Bytes())
	pw, _ := decodeSize(t, preview.Bytes())
	if tw != ThumbSize || pw != PreviewSize {
		t.Fatalf("thumb %dpx / preview %dpx, want %d / %d", tw, pw, ThumbSize, PreviewSize)
	}
	if preview.Len() <= thumb.Len() {
		t.Fatalf("preview (%d B) is not larger than thumb (%d B)", preview.Len(), thumb.Len())
	}
}

// Derivatives must not carry EXIF onward: a thumbnail is served to a browser,
// and GPS coordinates travelling with it would defeat the point of keeping
// metadata in an encrypted database.
func TestDerivativesAreStripped(t *testing.T) {
	th := newThumbnailer(t)
	src := jpegWithExif(t, 1200, 900, exifFixture{
		make_: "SONY", model: "ILCE-7M4", orientation: 1, iso: 400,
		dateTime: "2019:07:14 18:23:05", pixelX: 1200, pixelY: 900,
	})

	var out bytes.Buffer
	if err := th.Thumbnail(context.Background(), bytes.NewReader(src), &out, ThumbSize); err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"SONY", "ILCE-7M4", "2019:07:14"} {
		if bytes.Contains(out.Bytes(), []byte(needle)) {
			t.Fatalf("derivative still carries %q", needle)
		}
	}
}

func TestThumbnailRejectsGarbage(t *testing.T) {
	th := newThumbnailer(t)
	var out bytes.Buffer
	err := th.Thumbnail(context.Background(),
		strings.NewReader("this is definitely not an image"), &out, ThumbSize)
	if err == nil {
		t.Fatal("garbage input was accepted")
	}
	// Callers need to tell a bad upload from a broken server to pick a status.
	if !errors.Is(err, ErrNotAnImage) {
		t.Fatalf("want ErrNotAnImage, got: %v", err)
	}
}

func TestThumbnailRejectsEmptyInput(t *testing.T) {
	th := newThumbnailer(t)
	var out bytes.Buffer
	if err := th.Thumbnail(context.Background(), bytes.NewReader(nil), &out, ThumbSize); err == nil {
		t.Fatal("empty input was accepted")
	}
}

func TestThumbnailHonoursContextCancellation(t *testing.T) {
	th := newThumbnailer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already dead before the process starts

	var out bytes.Buffer
	if err := th.Thumbnail(ctx, bytes.NewReader(jpegNoExif(t, 800, 600)), &out, ThumbSize); err == nil {
		t.Fatal("a cancelled context still produced a thumbnail")
	}
}

func TestThumbnailRejectsInvalidSize(t *testing.T) {
	th := newThumbnailer(t)
	var out bytes.Buffer
	for _, size := range []int{0, -1} {
		if err := th.Thumbnail(context.Background(),
			bytes.NewReader(jpegNoExif(t, 100, 100)), &out, size); err == nil {
			t.Fatalf("size %d was accepted", size)
		}
	}
}

// --- EXIF -------------------------------------------------------------------

func TestExtractMetadata(t *testing.T) {
	src := jpegWithExif(t, 1200, 900, exifFixture{
		make_: "SONY", model: "ILCE-7M4", orientation: 6, iso: 400,
		dateTime: "2019:07:14 18:23:05", pixelX: 1200, pixelY: 900,
	})

	m, err := ExtractMetadata(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	// The fixture writes "SONY", the parser reports "Sony": imagemeta maps
	// manufacturer strings onto a canonical list. That is wanted -- the same
	// vendor writes SONY, Sony and SONY CORPORATION depending on the body, and
	// search would otherwise treat them as three brands.
	if m.CameraMake != "Sony" {
		t.Errorf("CameraMake = %q, want the canonical Sony", m.CameraMake)
	}
	if m.CameraModel != "ILCE-7M4" {
		t.Errorf("CameraModel = %q, want ILCE-7M4", m.CameraModel)
	}
	if m.Orientation != 6 {
		t.Errorf("Orientation = %d, want 6", m.Orientation)
	}
	if m.ISO != 400 {
		t.Errorf("ISO = %d, want 400", m.ISO)
	}
	if m.Width != 1200 || m.Height != 900 {
		t.Errorf("dimensions %dx%d, want 1200x900", m.Width, m.Height)
	}
	if m.TakenAt == nil {
		t.Fatal("TakenAt is nil although DateTimeOriginal was present")
	}
	want := time.Date(2019, 7, 14, 18, 23, 5, 0, m.TakenAt.Location())
	if !m.TakenAt.Equal(want) {
		t.Errorf("TakenAt = %s, want %s", m.TakenAt, want)
	}
}

// Photos without EXIF are entirely normal -- screenshots, exports, anything
// that has been through a stripping tool. That must not fail an upload.
func TestExtractMetadataWithoutExif(t *testing.T) {
	m, err := ExtractMetadata(bytes.NewReader(jpegNoExif(t, 640, 480)))
	if err != nil {
		t.Fatalf("a file without EXIF was treated as an error: %v", err)
	}
	if m.TakenAt != nil {
		t.Errorf("TakenAt = %v, want nil", m.TakenAt)
	}
	if m.CameraMake != "" || m.CameraModel != "" {
		t.Errorf("camera fields populated from nothing: %q / %q", m.CameraMake, m.CameraModel)
	}
}

func TestExtractMetadataFromGarbage(t *testing.T) {
	// Must not panic and must not fail the upload.
	if _, err := ExtractMetadata(strings.NewReader("not an image at all")); err != nil {
		t.Fatalf("garbage produced an error instead of empty metadata: %v", err)
	}
}

// 0,0 is a real location in the Gulf of Guinea, so "no coordinates" has to be
// distinguishable from the equator.
func TestMissingGPSIsNil(t *testing.T) {
	m, err := ExtractMetadata(bytes.NewReader(jpegWithExif(t, 100, 100, exifFixture{
		make_: "Canon", model: "EOS R6", orientation: 1, iso: 100,
		dateTime: "2020:01:01 00:00:00", pixelX: 100, pixelY: 100,
	})))
	if err != nil {
		t.Fatal(err)
	}
	if m.GPSLat != nil || m.GPSLon != nil {
		t.Fatalf("GPS reported as %v/%v although the fixture has none", m.GPSLat, m.GPSLon)
	}
}

func TestSwapsDimensions(t *testing.T) {
	for _, o := range []int{1, 2, 3, 4} {
		if SwapsDimensions(o) {
			t.Errorf("orientation %d should not swap width and height", o)
		}
	}
	for _, o := range []int{5, 6, 7, 8} {
		if !SwapsDimensions(o) {
			t.Errorf("orientation %d should swap width and height", o)
		}
	}
}

func TestOrientationFallsBackToUpright(t *testing.T) {
	m, err := ExtractMetadata(bytes.NewReader(jpegWithExif(t, 100, 100, exifFixture{
		make_: "X", model: "Y", orientation: 99, // nonsense
		iso: 100, dateTime: "2020:01:01 00:00:00", pixelX: 100, pixelY: 100,
	})))
	if err != nil {
		t.Fatal(err)
	}
	if m.Orientation != 1 {
		t.Fatalf("Orientation = %d, want the upright fallback 1", m.Orientation)
	}
}

var _ = image.Rect // keep the image import honest if tests are trimmed

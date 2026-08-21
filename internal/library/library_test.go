package library

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/rverst/obscura/internal/blob"
	"github.com/rverst/obscura/internal/crypto"
	"github.com/rverst/obscura/internal/media"
	"github.com/rverst/obscura/internal/store"
)

type fixture struct {
	lib   *Library
	vault Vault
	blobs *blob.Store
	dir   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("vipsthumbnail"); err != nil {
		t.Skip("vipsthumbnail not installed -- run these through `make test` (Docker)")
	}
	dir := t.TempDir()

	dek, err := crypto.NewDEK()
	if err != nil {
		t.Fatal(err)
	}
	dbKey, err := crypto.DBKey(dek)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenEncrypted(filepath.Join(dir, "photos.db"), dbKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, store.SetPhotos); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO users(id, username, created_at) VALUES(1,'ralf',unixepoch())`); err != nil {
		t.Fatal(err)
	}

	blobs, err := blob.NewStore(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	th, err := media.NewThumbnailer()
	if err != nil {
		t.Fatal(err)
	}
	video, _ := media.NewVideo() // nil when ffmpeg is absent
	return &fixture{
		lib:   New(blobs, th, video, 2),
		vault: Vault{DB: db, DEK: dek, UserID: 1},
		blobs: blobs,
		dir:   dir,
	}
}

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 0x40, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// upload mimics what the HTTP layer does: stream into a blob, then ingest.
func (f *fixture) upload(t *testing.T, name string, content []byte) (*Media, error) {
	t.Helper()
	id, size, sum, err := f.blobs.Put(f.vault.DEK, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	return f.lib.Ingest(context.Background(), f.vault, Ingestion{
		BlobID: id, Filename: name, Size: size, SHA256: sum,
	})
}

func TestIngestPhoto(t *testing.T) {
	f := newFixture(t)
	m, err := f.upload(t, "DSC01234.jpg", testJPEG(t, 2000, 1500))
	if err != nil {
		t.Fatal(err)
	}

	if m.ID == 0 {
		t.Fatal("no id assigned")
	}
	if m.Kind != KindPhoto {
		t.Fatalf("Kind = %q, want photo", m.Kind)
	}
	if m.Width != 2000 || m.Height != 1500 {
		t.Fatalf("dimensions %dx%d, want 2000x1500", m.Width, m.Height)
	}
	if m.MIME != "image/jpeg" {
		t.Fatalf("MIME = %q", m.MIME)
	}
	if m.BlobThumb == "" || m.BlobPreview == "" {
		t.Fatal("derivatives were not created")
	}

	// All three blobs must be readable and distinct.
	seen := map[string]bool{}
	for _, id := range []string{m.BlobOrig, m.BlobPreview, m.BlobThumb} {
		if seen[id] {
			t.Fatalf("blob id %s reused across derivatives", id)
		}
		seen[id] = true
		r, err := f.blobs.Open(f.vault.DEK, id)
		if err != nil {
			t.Fatalf("blob %s unreadable: %v", id, err)
		}
		r.Close()
	}
}

func TestIngestPersistsToDatabase(t *testing.T) {
	f := newFixture(t)
	m, err := f.upload(t, "holiday.jpg", testJPEG(t, 800, 600))
	if err != nil {
		t.Fatal(err)
	}

	got, err := Get(context.Background(), f.vault, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Filename != "holiday.jpg" {
		t.Fatalf("Filename = %q", got.Filename)
	}
	if got.Width != 800 || got.Height != 600 {
		t.Fatalf("dimensions %dx%d survived the round trip wrong", got.Width, got.Height)
	}
	if !bytes.Equal(got.SHA256, m.SHA256) {
		t.Fatal("hash differs after reload")
	}
}

// Uploading the same bytes twice must not produce a second copy: the whole
// point of hashing the plaintext is that duplicates are cheap to detect.
func TestDuplicateIsRejected(t *testing.T) {
	f := newFixture(t)
	content := testJPEG(t, 640, 480)

	first, err := f.upload(t, "a.jpg", content)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.upload(t, "a-copy.jpg", content)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got: %v", err)
	}
	if second == nil || second.ID != first.ID {
		t.Fatal("duplicate did not report the existing record")
	}

	n, err := Count(context.Background(), f.vault)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d records after a duplicate upload, want 1", n)
	}
}

// A rejected duplicate must not leave its blob behind, or every retry would
// leak the full size of the file.
func TestDuplicateRemovesItsBlob(t *testing.T) {
	f := newFixture(t)
	content := testJPEG(t, 400, 300)
	if _, err := f.upload(t, "a.jpg", content); err != nil {
		t.Fatal(err)
	}

	id, size, sum, err := f.blobs.Put(f.vault.DEK, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.lib.Ingest(context.Background(), f.vault, Ingestion{
		BlobID: id, Filename: "a-copy.jpg", Size: size, SHA256: sum,
	}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got: %v", err)
	}
	if _, err := f.blobs.Open(f.vault.DEK, id); !errors.Is(err, blob.ErrNotFound) {
		t.Fatal("the duplicate's blob was left on disk")
	}
}

// A file that cannot be processed must leave nothing behind -- no orphaned
// blobs occupying space that nothing will ever reclaim.
func TestFailedIngestCleansUp(t *testing.T) {
	f := newFixture(t)
	garbage := []byte("this is not an image, not even slightly")

	id, size, sum, err := f.blobs.Put(f.vault.DEK, bytes.NewReader(garbage))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.lib.Ingest(context.Background(), f.vault, Ingestion{
		BlobID: id, Filename: "broken.jpg", Size: size, SHA256: sum,
	}); err == nil {
		t.Fatal("garbage was ingested successfully")
	}

	if _, err := f.blobs.Open(f.vault.DEK, id); !errors.Is(err, blob.ErrNotFound) {
		t.Fatal("the original blob survived a failed ingest")
	}
	n, _ := Count(context.Background(), f.vault)
	if n != 0 {
		t.Fatalf("%d records after a failed ingest", n)
	}
}

func TestListIsNewestFirst(t *testing.T) {
	f := newFixture(t)
	// Distinct content so dedup does not collapse them.
	for i, dim := range []int{100, 200, 300} {
		if _, err := f.upload(t, "p.jpg", testJPEG(t, dim, dim)); err != nil {
			t.Fatalf("upload %d: %v", i, err)
		}
	}

	items, err := List(context.Background(), f.vault, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("%d items, want 3", len(items))
	}
	for i := 1; i < len(items); i++ {
		prev, cur := items[i-1].SortTime(), items[i].SortTime()
		if cur.After(prev) {
			t.Fatalf("item %d (%s) is newer than item %d (%s)", i, cur, i-1, prev)
		}
	}
}

func TestListPagesWithoutRepeating(t *testing.T) {
	f := newFixture(t)
	for _, dim := range []int{100, 150, 200, 250, 300} {
		if _, err := f.upload(t, "p.jpg", testJPEG(t, dim, dim)); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	seen := map[int64]bool{}
	var cursor *time.Time
	var cursorID int64

	for page := 0; page < 5; page++ {
		items, err := List(ctx, f.vault, ListOptions{Limit: 2, Before: cursor, BeforeID: cursorID})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 0 {
			break
		}
		for _, m := range items {
			if seen[m.ID] {
				t.Fatalf("item %d returned on two pages", m.ID)
			}
			seen[m.ID] = true
		}
		last := items[len(items)-1]
		st := last.SortTime()
		cursor, cursorID = &st, last.ID
	}
	if len(seen) != 5 {
		t.Fatalf("paging yielded %d of 5 items", len(seen))
	}
}

func TestGetMissing(t *testing.T) {
	f := newFixture(t)
	if _, err := Get(context.Background(), f.vault, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got: %v", err)
	}
}

// Another user's media must not be reachable, even with the right id. v1 is
// single-user, but the queries are already scoped and a regression here would
// be invisible until multi-user lands.
func TestOtherUsersMediaIsInvisible(t *testing.T) {
	f := newFixture(t)
	m, err := f.upload(t, "mine.jpg", testJPEG(t, 320, 240))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.vault.DB.Exec(
		`INSERT INTO users(id, username, created_at) VALUES(2,'eve',unixepoch())`); err != nil {
		t.Fatal(err)
	}
	eve := Vault{DB: f.vault.DB, DEK: f.vault.DEK, UserID: 2}

	if _, err := Get(context.Background(), eve, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another user could read media %d: %v", m.ID, err)
	}
	if _, err := FindBySHA256(context.Background(), eve, m.SHA256); !errors.Is(err, ErrNotFound) {
		t.Fatal("another user could find media by hash")
	}
	items, err := List(context.Background(), eve, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("another user sees %d items", len(items))
	}
}

func TestKindOf(t *testing.T) {
	cases := map[string]string{
		"a.jpg": KindPhoto, "a.JPG": KindPhoto, "a.png": KindPhoto,
		"a.heic": KindPhoto, "a.arw": KindPhoto, "noext": KindPhoto,
		"a.mp4": KindVideo, "a.MOV": KindVideo, "a.mkv": KindVideo,
	}
	for name, want := range cases {
		if got := kindOf(name); got != want {
			t.Errorf("kindOf(%q) = %q, want %q", name, got, want)
		}
	}
}

// --- video ------------------------------------------------------------------

// makeClip renders a short video with ffmpeg, or skips.
func makeClip(t *testing.T, seconds string, w, h int, faststart bool) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed -- run these through `make test` (Docker)")
	}
	path := filepath.Join(t.TempDir(), "clip.mp4")
	args := []string{
		"-v", "error", "-y", "-f", "lavfi",
		"-i", fmt.Sprintf("testsrc2=size=%dx%d:duration=%s:rate=15", w, h, seconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
	}
	if faststart {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, path)
	if err := exec.Command("ffmpeg", args...).Run(); err != nil {
		t.Skipf("cannot render a test clip: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestIngestVideoProducesPosterAndDuration(t *testing.T) {
	f := newFixture(t)
	if f.lib.video == nil {
		t.Skip("ffmpeg not installed")
	}
	m, err := f.upload(t, "holiday.mp4", makeClip(t, "3", 640, 480, true))
	if err != nil {
		t.Fatal(err)
	}

	if m.Kind != KindVideo {
		t.Fatalf("Kind = %q, want video", m.Kind)
	}
	if m.BlobThumb == "" || m.BlobPreview == "" {
		t.Fatal("no poster frames were produced")
	}
	if m.Width != 640 || m.Height != 480 {
		t.Fatalf("dimensions %dx%d, want 640x480", m.Width, m.Height)
	}
	if m.DurationMs < 2500 || m.DurationMs > 3500 {
		t.Fatalf("DurationMs = %d, want roughly 3000", m.DurationMs)
	}

	// The poster has to be a real, readable image.
	r, err := f.blobs.Open(f.vault.DEK, m.BlobThumb)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := jpeg.DecodeConfig(r); err != nil {
		t.Fatalf("the poster is not a decodable JPEG: %v", err)
	}
}

// An MP4 with its index at the end is ffmpeg's own default and common from
// cameras. It is also the case a pipe cannot handle at all.
func TestIngestVideoWithIndexAtTheEnd(t *testing.T) {
	f := newFixture(t)
	if f.lib.video == nil {
		t.Skip("ffmpeg not installed")
	}
	m, err := f.upload(t, "camera.mp4", makeClip(t, "3", 640, 480, false))
	if err != nil {
		t.Fatal(err)
	}
	if m.BlobThumb == "" {
		t.Fatal("no poster for an index-at-end video")
	}
}

// A clip that cannot be decoded must still be stored. The original is served as
// uploaded, so refusing the file would lose it for the sake of a thumbnail.
func TestUndecodableVideoIsStoredWithoutPoster(t *testing.T) {
	f := newFixture(t)
	m, err := f.upload(t, "broken.mp4", []byte("not a video at all, but named like one"))
	if err != nil {
		t.Fatalf("an undecodable video was rejected instead of stored: %v", err)
	}
	if m.Kind != KindVideo {
		t.Fatalf("Kind = %q", m.Kind)
	}
	if m.BlobOrig == "" {
		t.Fatal("the original was not kept")
	}
	if m.BlobThumb != "" {
		t.Fatal("a poster was reported for a file that cannot be decoded")
	}
	// And it must be listed, not silently absent.
	items, err := List(context.Background(), f.vault, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("%d items in the library, want 1", len(items))
	}
}

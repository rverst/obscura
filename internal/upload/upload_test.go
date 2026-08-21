package upload

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/rverst/obscura/internal/blob"
	"github.com/rverst/obscura/internal/crypto"
)

func newManager(t *testing.T, maxSize int64) (*Manager, *blob.Store, []byte) {
	t.Helper()
	blobs, err := blob.NewStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	dek, err := crypto.NewDEK()
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(blobs, maxSize, time.Hour)
	t.Cleanup(m.CloseAll)
	return m, blobs, dek
}

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*13 + i/97)
	}
	return b
}

func TestChunkedUpload(t *testing.T) {
	m, blobs, dek := newManager(t, 0)
	want := payload(250 << 10)

	s, err := m.Create(dek, 1, "holiday.jpg", int64(len(want)))
	if err != nil {
		t.Fatal(err)
	}

	const chunk = 64 << 10
	for off := 0; off < len(want); off += chunk {
		end := min(off+chunk, len(want))
		if _, err := m.Append(s.ID, 1, int64(off), bytes.NewReader(want[off:end])); err != nil {
			t.Fatalf("chunk at %d: %v", off, err)
		}
	}
	if got := s.Received(); got != int64(len(want)) {
		t.Fatalf("Received() = %d, want %d", got, len(want))
	}

	res, err := m.Finish(s.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Size != int64(len(want)) {
		t.Fatalf("Size = %d, want %d", res.Size, len(want))
	}
	if h := sha256.Sum256(want); !bytes.Equal(res.SHA256, h[:]) {
		t.Fatal("hash does not match the uploaded bytes")
	}
	if res.Filename != "holiday.jpg" {
		t.Fatalf("Filename = %q", res.Filename)
	}

	r, err := blobs.Open(dek, res.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("stored content differs from what was uploaded")
	}
}

// A client that loses the response to a chunk does not know whether it arrived,
// so it resends from its last acknowledged offset. The overlap must be dropped
// rather than appended a second time.
func TestResumeAfterUnacknowledgedChunk(t *testing.T) {
	m, blobs, dek := newManager(t, 0)
	want := payload(100 << 10)

	s, _ := m.Create(dek, 1, "x.jpg", 0)
	if _, err := m.Append(s.ID, 1, 0, bytes.NewReader(want[:60<<10])); err != nil {
		t.Fatal(err)
	}
	// Client believes only 40KB landed and resends from there.
	if _, err := m.Append(s.ID, 1, 40<<10, bytes.NewReader(want[40<<10:])); err != nil {
		t.Fatal(err)
	}

	res, err := m.Finish(s.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Size != int64(len(want)) {
		t.Fatalf("Size = %d, want %d -- the overlap was counted twice", res.Size, len(want))
	}
	r, _ := blobs.Open(dek, res.BlobID)
	defer r.Close()
	got, _ := io.ReadAll(r)
	if !bytes.Equal(got, want) {
		t.Fatal("resumed upload produced different content")
	}
}

// A chunk that is entirely a retransmit must be accepted and change nothing.
func TestFullyRedundantChunk(t *testing.T) {
	m, _, dek := newManager(t, 0)
	data := payload(20 << 10)

	s, _ := m.Create(dek, 1, "x.jpg", 0)
	if _, err := m.Append(s.ID, 1, 0, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Append(s.ID, 1, 0, bytes.NewReader(data[:10<<10])); err != nil {
		t.Fatalf("a fully redundant chunk was rejected: %v", err)
	}
	if got := s.Received(); got != int64(len(data)) {
		t.Fatalf("Received() = %d, want %d", got, len(data))
	}
}

// A gap would write a file with a hole that decrypts to garbage, so it has to
// be refused rather than papered over.
func TestGapIsRefused(t *testing.T) {
	m, _, dek := newManager(t, 0)
	s, _ := m.Create(dek, 1, "x.jpg", 0)

	if _, err := m.Append(s.ID, 1, 0, bytes.NewReader(payload(1000))); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Append(s.ID, 1, 5000, bytes.NewReader(payload(1000))); !errors.Is(err, ErrOffsetMismatch) {
		t.Fatalf("want ErrOffsetMismatch, got: %v", err)
	}
}

func TestStatusReportsOffsetForResume(t *testing.T) {
	m, _, dek := newManager(t, 0)
	s, _ := m.Create(dek, 1, "x.jpg", 0)
	if _, err := m.Append(s.ID, 1, 0, bytes.NewReader(payload(7777))); err != nil {
		t.Fatal(err)
	}

	got, err := m.Status(s.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Received() != 7777 {
		t.Fatalf("Status reports %d bytes, want 7777", got.Received())
	}
	if got.Filename != "x.jpg" {
		t.Fatalf("Filename = %q", got.Filename)
	}
}

// Another user must not be able to touch, read or hijack an upload, and the
// error must not confirm that the id exists.
func TestOtherUserCannotTouchUpload(t *testing.T) {
	m, _, dek := newManager(t, 0)
	s, _ := m.Create(dek, 1, "x.jpg", 0)

	for name, err := range map[string]error{
		"Status": func() error { _, e := m.Status(s.ID, 2); return e }(),
		"Append": func() error { _, e := m.Append(s.ID, 2, 0, bytes.NewReader(payload(10))); return e }(),
		"Finish": func() error { _, e := m.Finish(s.ID, 2); return e }(),
		"Abort":  m.Abort(s.ID, 2),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s as another user: want ErrNotFound, got %v", name, err)
		}
	}
	// The rightful owner's upload must be unharmed.
	if _, err := m.Status(s.ID, 1); err != nil {
		t.Fatalf("owner lost access after another user probed: %v", err)
	}
}

func TestAbortDiscardsBlob(t *testing.T) {
	m, blobs, dek := newManager(t, 0)
	s, _ := m.Create(dek, 1, "x.jpg", 0)
	if _, err := m.Append(s.ID, 1, 0, bytes.NewReader(payload(50<<10))); err != nil {
		t.Fatal(err)
	}
	if err := m.Abort(s.ID, 1); err != nil {
		t.Fatal(err)
	}
	if m.InFlight() != 0 {
		t.Fatalf("%d uploads still in flight after abort", m.InFlight())
	}
	if _, err := m.Status(s.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatal("aborted upload is still addressable")
	}
	// Nothing must remain in the blob store.
	if _, err := blobs.Open(dek, "deadbeefdeadbeefdeadbeefdeadbeef"); !errors.Is(err, blob.ErrNotFound) {
		t.Fatal("unexpected blob present")
	}
}

func TestFinishTwiceFails(t *testing.T) {
	m, _, dek := newManager(t, 0)
	s, _ := m.Create(dek, 1, "x.jpg", 0)
	m.Append(s.ID, 1, 0, bytes.NewReader(payload(100)))

	if _, err := m.Finish(s.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Finish(s.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("finishing twice: want ErrNotFound, got %v", err)
	}
	if _, err := m.Append(s.ID, 1, 100, bytes.NewReader(payload(10))); !errors.Is(err, ErrNotFound) {
		t.Fatal("appending to a finished upload was accepted")
	}
}

func TestSizeLimitOnDeclaredSize(t *testing.T) {
	m, _, dek := newManager(t, 1<<20)
	if _, err := m.Create(dek, 1, "huge.mp4", 5<<20); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got: %v", err)
	}
}

// A client can lie about the declared size, so the limit has to hold against
// what actually arrives.
func TestSizeLimitOnActualBytes(t *testing.T) {
	m, _, dek := newManager(t, 100<<10)
	s, err := m.Create(dek, 1, "sneaky.mp4", 1) // claims to be tiny
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Append(s.ID, 1, 0, bytes.NewReader(payload(200<<10)))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got: %v", err)
	}
}

func TestCreateRejectsEmptyFilename(t *testing.T) {
	m, _, dek := newManager(t, 0)
	if _, err := m.Create(dek, 1, "", 0); err == nil {
		t.Fatal("empty filename was accepted")
	}
}

// An abandoned upload holds its .part file; on a Pi with a 32GB card a handful
// of those is enough to fill the disk.
func TestSweepDiscardsIdleUploads(t *testing.T) {
	blobs, err := blob.NewStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	dek, _ := crypto.NewDEK()
	m := NewManager(blobs, 0, time.Millisecond)
	defer m.CloseAll()

	s, _ := m.Create(dek, 1, "abandoned.jpg", 0)
	m.Append(s.ID, 1, 0, bytes.NewReader(payload(10<<10)))

	time.Sleep(5 * time.Millisecond)
	if n := m.Sweep(); n != 1 {
		t.Fatalf("Sweep discarded %d uploads, want 1", n)
	}
	if m.InFlight() != 0 {
		t.Fatalf("%d uploads still in flight", m.InFlight())
	}
	if _, err := m.Status(s.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatal("swept upload is still addressable")
	}
}

func TestSweepKeepsActiveUploads(t *testing.T) {
	m, _, dek := newManager(t, 0)
	s, _ := m.Create(dek, 1, "active.jpg", 0)
	if n := m.Sweep(); n != 0 {
		t.Fatalf("Sweep discarded %d active uploads", n)
	}
	if _, err := m.Status(s.ID, 1); err != nil {
		t.Fatalf("active upload was swept: %v", err)
	}
}

func TestCloseAllDiscardsEverything(t *testing.T) {
	m, _, dek := newManager(t, 0)
	for i := 0; i < 3; i++ {
		s, _ := m.Create(dek, 1, "x.jpg", 0)
		m.Append(s.ID, 1, 0, bytes.NewReader(payload(1024)))
	}
	if m.InFlight() != 3 {
		t.Fatalf("InFlight() = %d, want 3", m.InFlight())
	}
	m.CloseAll()
	if m.InFlight() != 0 {
		t.Fatalf("InFlight() = %d after CloseAll", m.InFlight())
	}
}

func TestUnknownUploadID(t *testing.T) {
	m, _, _ := newManager(t, 0)
	if _, err := m.Status("nope", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got: %v", err)
	}
}

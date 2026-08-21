package blob

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rverst/obscura/internal/crypto"
)

func newStore(t *testing.T) (*Store, []byte, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	dek, err := crypto.NewDEK()
	if err != nil {
		t.Fatal(err)
	}
	return s, dek, dir
}

// deterministic content, so any byte range can be regenerated for comparison
// without holding the whole thing in memory.
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

func TestPutAndOpenRoundtrip(t *testing.T) {
	s, dek, _ := newStore(t)
	want := pattern(300 << 10) // spans several 64 KB DARE packages

	id, size, sum, err := s.Put(dek, bytes.NewReader(want))
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(want)) {
		t.Fatalf("size %d, want %d", size, len(want))
	}
	if h := sha256.Sum256(want); !bytes.Equal(sum, h[:]) {
		t.Fatal("returned hash does not match the plaintext")
	}

	r, err := s.Open(dek, id)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Size != int64(len(want)) {
		t.Fatalf("Reader.Size %d, want %d", r.Size, len(want))
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("decrypted content differs")
	}
}

// The reason DARE was chosen over a single stream cipher: seeking must not
// require decrypting everything before the target offset.
func TestRandomAccess(t *testing.T) {
	s, dek, _ := newStore(t)
	want := pattern(2 << 20)

	id, _, _, err := s.Put(dek, bytes.NewReader(want))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Open(dek, id)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	rnd := rand.New(rand.NewSource(7))
	for i := 0; i < 50; i++ {
		length := 1 + rnd.Intn(64<<10)
		off := rnd.Intn(len(want) - length)

		buf := make([]byte, length)
		if _, err := r.ReadAt(buf, int64(off)); err != nil {
			t.Fatalf("ReadAt(%d, %d): %v", off, length, err)
		}
		if !bytes.Equal(buf, want[off:off+length]) {
			t.Fatalf("range [%d,%d) decrypted incorrectly", off, off+length)
		}
	}
}

func TestOnDiskContentIsEncrypted(t *testing.T) {
	s, dek, dir := newStore(t)
	needle := []byte("BEACH-HOLIDAY-CRETE-2019-SECRET-MARKER")
	plain := append(append(pattern(4096), needle...), pattern(4096)...)

	id, _, _, err := s.Put(dek, bytes.NewReader(plain))
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if bytes.Contains(raw, needle) {
			found = true
		}
		// The id must not appear in the content either.
		if bytes.Contains(raw, []byte(id)) {
			t.Errorf("the blob id appears inside %s", p)
		}
		return nil
	})
	if found {
		t.Fatal("plaintext marker found on disk")
	}
}

func TestWrongKeyFails(t *testing.T) {
	s, dek, _ := newStore(t)
	id, _, _, err := s.Put(dek, bytes.NewReader(pattern(128<<10)))
	if err != nil {
		t.Fatal(err)
	}

	other, _ := crypto.NewDEK()
	r, err := s.Open(other, id)
	if err != nil {
		return // rejected at open time, fine
	}
	defer r.Close()
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("SECURITY PROBLEM: a blob decrypted with the wrong key")
	}
}

// Every blob has its own HKDF-derived key, so a blob must not be readable under
// another blob's identity even with the right DEK.
func TestBlobsAreNotInterchangeable(t *testing.T) {
	s, dek, _ := newStore(t)
	idA, _, _, err := s.Put(dek, bytes.NewReader(pattern(70<<10)))
	if err != nil {
		t.Fatal(err)
	}
	idB, _, _, err := s.Put(dek, bytes.NewReader(pattern(70<<10)))
	if err != nil {
		t.Fatal(err)
	}

	pa, _ := s.path(idA)
	pb, _ := s.path(idB)
	rawA, _ := os.ReadFile(pa)
	rawB, _ := os.ReadFile(pb)
	if bytes.Equal(rawA, rawB) {
		t.Fatal("identical plaintext produced identical ciphertext -- the key is not per blob")
	}

	// Swap the file contents: reading idA must now fail, not silently serve B.
	if err := os.WriteFile(pa, rawB, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := s.Open(dek, idA)
	if err != nil {
		return
	}
	defer r.Close()
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("a blob swapped in under another id was accepted")
	}
}

func TestTamperingDetected(t *testing.T) {
	s, dek, _ := newStore(t)
	id, _, _, err := s.Put(dek, bytes.NewReader(pattern(200<<10)))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.path(id)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte inside a package payload, past the 16-byte header.
	raw[len(raw)/2] ^= 0x01
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := s.Open(dek, id)
	if err != nil {
		return
	}
	defer r.Close()
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("SECURITY PROBLEM: a tampered blob was served without error")
	}
}

func TestOpenMissing(t *testing.T) {
	s, dek, _ := newStore(t)
	if _, err := s.Open(dek, "00112233445566778899aabbccddeeff"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got: %v", err)
	}
}

func TestMalformedID(t *testing.T) {
	s, dek, _ := newStore(t)
	// A traversal attempt must not escape the blob directory.
	for _, id := range []string{"", "ab", "../../etc/passwd", "ZZZZ", "ab/cd"} {
		if _, err := s.Open(dek, id); err == nil {
			t.Fatalf("malformed id %q was accepted", id)
		}
	}
}

func TestRemoveIsRepeatable(t *testing.T) {
	s, dek, _ := newStore(t)
	id, _, _, err := s.Put(dek, bytes.NewReader(pattern(1024)))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Remove(id); err != nil {
			t.Fatalf("Remove call %d: %v", i+1, err)
		}
	}
	if _, err := s.Open(dek, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("blob still present after removal: %v", err)
	}
}

// A failed write must not leave anything that a later read could mistake for a
// complete blob.
func TestFailedPutLeavesNoBlob(t *testing.T) {
	s, dek, dir := newStore(t)
	_, _, _, err := s.Put(dek, io.MultiReader(
		bytes.NewReader(pattern(128<<10)),
		&failingReader{},
	))
	if err == nil {
		t.Fatal("Put succeeded despite a failing reader")
	}

	var leftovers []string
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			leftovers = append(leftovers, strings.TrimPrefix(p, dir))
		}
		return nil
	})
	if len(leftovers) != 0 {
		t.Fatalf("files left behind after a failed Put: %v", leftovers)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk went away") }

func TestEmptyBlob(t *testing.T) {
	s, dek, _ := newStore(t)
	id, size, _, err := s.Put(dek, bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	if size != 0 {
		t.Fatalf("size %d, want 0", size)
	}
	r, err := s.Open(dek, id)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("read %d bytes from an empty blob", len(got))
	}
}

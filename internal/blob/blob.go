// Package blob stores encrypted file content on disk.
//
// Every blob is encrypted with its own key, derived from the DEK via HKDF, and
// written in the DARE format (github.com/minio/sio): a sequence of 64 KB
// packages, each individually encrypted and authenticated with AES-256-GCM.
//
// The package structure is not incidental. It is what makes HTTP range requests
// possible: to serve second 47 of a video, only the packages covering that byte
// range are decrypted. A single-stream cipher would force decrypting the file
// from the start for every seek, which on a Raspberry Pi is the difference
// between a video that scrubs and one that does not.
package blob

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/minio/sio"
	"github.com/rverst/obscura/internal/crypto"
)

// ErrNotFound reports that no blob exists for an ID.
var ErrNotFound = errors.New("blob: not found")

// Store owns the directory holding encrypted blobs.
type Store struct {
	dir string
}

// NewStore prepares the blob directory.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("blob: create directory: %w", err)
	}
	return &Store{dir: dir}, nil
}

// NewID generates an identifier for a new blob.
//
// Random rather than derived from the content: a content-derived name would
// leak equality between blobs to anyone who can list the directory, which is
// exactly the kind of metadata this design is trying not to expose.
func NewID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("blob: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// path fans blobs out over two levels of subdirectory.
//
// A single flat directory with tens of thousands of entries makes readdir and
// several filesystems slow; 256 x 256 buckets keeps directories small.
func (s *Store) path(id string) (string, error) {
	if len(id) < 4 || !isHex(id) {
		return "", fmt.Errorf("blob: malformed id %q", id)
	}
	return filepath.Join(s.dir, id[0:2], id[2:4], id), nil
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return len(s)%2 == 0
}

// Put encrypts src into a new blob and returns its id, the plaintext size and
// the SHA-256 of the plaintext.
//
// The hash is computed on the way through rather than in a second pass so the
// input is read exactly once -- it may be a several-gigabyte upload.
func (s *Store) Put(dek []byte, src io.Reader) (id string, size int64, sum []byte, err error) {
	id, err = NewID()
	if err != nil {
		return "", 0, nil, err
	}
	dst, err := s.path(id)
	if err != nil {
		return "", 0, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", 0, nil, fmt.Errorf("blob: create bucket: %w", err)
	}

	key, err := crypto.FileKey(dek, id)
	if err != nil {
		return "", 0, nil, err
	}
	defer crypto.Zero(key)

	// Write to a temporary name and rename on success, so a crash mid-upload
	// cannot leave a truncated blob that looks complete.
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", 0, nil, fmt.Errorf("blob: create %s: %w", tmp, err)
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()

	hasher := sha256.New()
	counter := &countingReader{r: io.TeeReader(src, hasher)}

	if _, err = sio.Encrypt(f, counter, sio.Config{Key: key}); err != nil {
		return "", 0, nil, fmt.Errorf("blob: encrypt: %w", err)
	}
	if err = f.Sync(); err != nil {
		return "", 0, nil, fmt.Errorf("blob: sync: %w", err)
	}
	if err = f.Close(); err != nil {
		return "", 0, nil, fmt.Errorf("blob: close: %w", err)
	}
	if err = os.Rename(tmp, dst); err != nil {
		return "", 0, nil, fmt.Errorf("blob: publish: %w", err)
	}
	return id, counter.n, hasher.Sum(nil), nil
}

// Open returns a reader over the decrypted blob together with its plaintext
// size. The caller must close it.
//
// The returned ReadSeeker is what http.ServeContent needs to answer range
// requests; seeking costs one package decryption, not a re-read of the file.
func (s *Store) Open(dek []byte, id string) (*Reader, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("blob: open %s: %w", id, err)
	}

	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("blob: stat %s: %w", id, err)
	}
	plainSize, err := sio.DecryptedSize(uint64(st.Size()))
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("blob: %s has an invalid size: %w", id, err)
	}

	// Not zeroed on return: DecryptReaderAt keeps the key for every subsequent
	// read, so it has to stay alive for the Reader's lifetime. Close wipes it.
	key, err := crypto.FileKey(dek, id)
	if err != nil {
		f.Close()
		return nil, err
	}

	ra, err := sio.DecryptReaderAt(f, sio.Config{Key: key})
	if err != nil {
		f.Close()
		crypto.Zero(key)
		return nil, fmt.Errorf("blob: decrypt %s: %w", id, err)
	}
	return &Reader{
		SectionReader: io.NewSectionReader(ra, 0, int64(plainSize)),
		f:             f,
		key:           key,
		Size:          int64(plainSize),
	}, nil
}

// Reader is a seekable view of a decrypted blob.
type Reader struct {
	*io.SectionReader
	f    *os.File
	key  []byte
	Size int64
}

// Close releases the file and wipes this blob's key.
func (r *Reader) Close() error {
	crypto.Zero(r.key)
	return r.f.Close()
}

// Remove deletes a blob. Removing something that is already gone is not an
// error -- deletion needs to be safe to retry.
func (s *Store) Remove(id string) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blob: remove %s: %w", id, err)
	}
	return nil
}

// countingReader records how many plaintext bytes passed through.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

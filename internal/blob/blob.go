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
	"hash"
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

// Create starts a new blob and returns a writer that encrypts as it goes.
//
// Content is encrypted from the very first byte, which is what lets a chunked
// upload span several HTTP requests without ever parking plaintext on disk.
// The alternative -- buffering the upload somewhere and encrypting at the end --
// would leave a readable copy of every photo lying around for the duration of
// its upload.
func (s *Store) Create(dek []byte) (*Writer, error) {
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	dst, err := s.path(id)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return nil, fmt.Errorf("blob: create bucket: %w", err)
	}

	key, err := crypto.FileKey(dek, id)
	if err != nil {
		return nil, err
	}

	// Written under a .part name and renamed on commit, so a crash mid-upload
	// cannot leave a truncated blob that later reads as complete.
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		crypto.Zero(key)
		return nil, fmt.Errorf("blob: create %s: %w", tmp, err)
	}
	enc, err := sio.EncryptWriter(f, sio.Config{Key: key})
	if err != nil {
		f.Close()
		os.Remove(tmp)
		crypto.Zero(key)
		return nil, fmt.Errorf("blob: encrypt: %w", err)
	}
	return &Writer{
		id: id, tmp: tmp, dst: dst,
		f: f, enc: enc, key: key,
		hasher: sha256.New(),
	}, nil
}

// Writer accumulates encrypted content for one blob.
//
// Not safe for concurrent use: one upload is one sequential stream.
type Writer struct {
	id     string
	tmp    string
	dst    string
	f      *os.File
	enc    io.WriteCloser
	key    []byte
	hasher hash.Hash
	n      int64
	done   bool
}

// ID is the identifier the blob will have once committed.
func (w *Writer) ID() string { return w.id }

// Size is the number of plaintext bytes written so far.
func (w *Writer) Size() int64 { return w.n }

func (w *Writer) Write(p []byte) (int, error) {
	if w.done {
		return 0, errors.New("blob: writer already closed")
	}
	n, err := w.enc.Write(p)
	if n > 0 {
		// Hash the plaintext on the way past. A second pass would mean
		// decrypting the whole blob again just to learn its hash, and these
		// can be several gigabytes.
		w.hasher.Write(p[:n])
		w.n += int64(n)
	}
	if err != nil {
		return n, fmt.Errorf("blob: write: %w", err)
	}
	return n, nil
}

// Commit finalises the blob and returns its plaintext size and SHA-256.
func (w *Writer) Commit() (size int64, sum []byte, err error) {
	if w.done {
		return 0, nil, errors.New("blob: writer already closed")
	}
	w.done = true
	defer crypto.Zero(w.key)

	// EncryptWriter.Close flushes the final package and closes the file with it.
	if err := w.enc.Close(); err != nil {
		os.Remove(w.tmp)
		return 0, nil, fmt.Errorf("blob: finalise: %w", err)
	}
	if err := os.Rename(w.tmp, w.dst); err != nil {
		os.Remove(w.tmp)
		return 0, nil, fmt.Errorf("blob: publish: %w", err)
	}
	return w.n, w.hasher.Sum(nil), nil
}

// Abort discards a partial blob. Safe to call after Commit, where it does
// nothing -- callers defer it to cover the error paths.
func (w *Writer) Abort() error {
	if w.done {
		return nil
	}
	w.done = true
	crypto.Zero(w.key)
	w.enc.Close()
	w.f.Close()
	if err := os.Remove(w.tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blob: discard %s: %w", w.tmp, err)
	}
	return nil
}

// Put stores src as a single blob. A convenience wrapper over Create for
// callers that already have the whole thing.
func (s *Store) Put(dek []byte, src io.Reader) (id string, size int64, sum []byte, err error) {
	w, err := s.Create(dek)
	if err != nil {
		return "", 0, nil, err
	}
	defer w.Abort()

	if _, err := io.Copy(w, src); err != nil {
		return "", 0, nil, err
	}
	size, sum, err = w.Commit()
	if err != nil {
		return "", 0, nil, err
	}
	return w.ID(), size, sum, nil
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

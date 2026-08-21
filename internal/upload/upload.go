// Package upload handles resumable chunked uploads.
//
// Resumability is not a luxury here: a 4 GB video pushed over wifi to a
// Raspberry Pi that dies at 90% is the normal case, not the edge case. The
// protocol is deliberately small -- create, ask the offset, append, finish --
// rather than pulling in a full tus implementation for four endpoints.
//
// Content is encrypted from the first byte via blob.Writer, so an upload in
// flight never exists as plaintext on disk. The consequence is that sessions
// cannot survive a restart: the encrypting stream lives in memory, and so does
// the key. That matches the rest of the design, where a restart locks
// everything until someone logs in anyway.
package upload

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/rverst/obscura/internal/blob"
)

var (
	// ErrNotFound reports an unknown or already finished upload.
	ErrNotFound = errors.New("upload: no such upload")
	// ErrOffsetMismatch reports a gap between what the server has and where
	// the client wants to continue.
	ErrOffsetMismatch = errors.New("upload: offset does not match")
	// ErrTooLarge reports that an upload exceeded the configured limit.
	ErrTooLarge = errors.New("upload: too large")
)

// Session is one upload in progress.
type Session struct {
	ID       string
	UserID   int64
	Filename string
	// Declared is the size the client announced, or 0 if it did not. Advisory
	// only -- the authoritative size is what actually arrives.
	Declared int64

	mu       sync.Mutex
	received int64
	w        *blob.Writer
	lastSeen time.Time
	done     bool
}

// Received is how many bytes the server holds. This is what a resuming client
// asks for.
func (s *Session) Received() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.received
}

// Manager owns in-flight uploads.
type Manager struct {
	blobs   *blob.Store
	maxSize int64
	ttl     time.Duration

	mu       sync.Mutex
	sessions map[string]*Session
}

// NewManager creates an upload manager. maxSize caps a single file; ttl is how
// long an untouched upload survives before it is swept.
func NewManager(blobs *blob.Store, maxSize int64, ttl time.Duration) *Manager {
	return &Manager{
		blobs:    blobs,
		maxSize:  maxSize,
		ttl:      ttl,
		sessions: make(map[string]*Session),
	}
}

// Create opens a new upload and the encrypted blob behind it.
func (m *Manager) Create(dek []byte, userID int64, filename string, declared int64) (*Session, error) {
	if filename == "" {
		return nil, errors.New("upload: empty filename")
	}
	if m.maxSize > 0 && declared > m.maxSize {
		return nil, fmt.Errorf("%w: declared %d bytes, limit is %d", ErrTooLarge, declared, m.maxSize)
	}

	w, err := m.blobs.Create(dek)
	if err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		w.Abort()
		return nil, err
	}

	s := &Session{
		ID:       id,
		UserID:   userID,
		Filename: filename,
		Declared: declared,
		w:        w,
		lastSeen: time.Now(),
	}
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	return s, nil
}

// Append writes a chunk at offset.
//
// An offset behind what the server holds means the client is resending after an
// unacknowledged chunk; the overlap is discarded rather than rejected, because
// the client cannot know how much of its last request arrived. An offset ahead
// of it is a genuine gap and is refused -- silently accepting it would write a
// file with a hole in the middle that decrypts to garbage.
func (m *Manager) Append(id string, userID int64, offset int64, r io.Reader) (*Session, error) {
	s, err := m.lookup(id, userID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return nil, ErrNotFound
	}
	s.lastSeen = time.Now()

	switch {
	case offset > s.received:
		return nil, fmt.Errorf("%w: server has %d, client sent %d", ErrOffsetMismatch, s.received, offset)
	case offset < s.received:
		if _, err := io.CopyN(io.Discard, r, s.received-offset); err != nil {
			if errors.Is(err, io.EOF) {
				// The whole chunk was a retransmit of bytes already held.
				return s, nil
			}
			return nil, fmt.Errorf("upload: skip retransmitted bytes: %w", err)
		}
	}

	var src io.Reader = r
	if m.maxSize > 0 {
		// One more byte than allowed, so exceeding the limit is detectable
		// rather than silently truncated.
		src = io.LimitReader(r, m.maxSize-s.received+1)
	}

	n, err := io.Copy(s.w, src)
	s.received += n
	if err != nil {
		return nil, fmt.Errorf("upload: store chunk: %w", err)
	}
	if m.maxSize > 0 && s.received > m.maxSize {
		return nil, fmt.Errorf("%w: exceeded %d bytes", ErrTooLarge, m.maxSize)
	}
	return s, nil
}

// Result describes a completed upload, ready to be ingested.
type Result struct {
	BlobID   string
	Filename string
	Size     int64
	SHA256   []byte
}

// Finish commits the blob and retires the session.
func (m *Manager) Finish(id string, userID int64) (Result, error) {
	s, err := m.lookup(id, userID)
	if err != nil {
		return Result{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return Result{}, ErrNotFound
	}
	s.done = true

	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()

	size, sum, err := s.w.Commit()
	if err != nil {
		return Result{}, err
	}
	return Result{BlobID: s.w.ID(), Filename: s.Filename, Size: size, SHA256: sum}, nil
}

// Abort discards an upload and its partial blob.
func (m *Manager) Abort(id string, userID int64) error {
	s, err := m.lookup(id, userID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()

	if s.done {
		return nil
	}
	s.done = true
	return s.w.Abort()
}

// Status returns an in-flight upload, for a client that wants to resume.
func (m *Manager) Status(id string, userID int64) (*Session, error) {
	return m.lookup(id, userID)
}

// Sweep discards uploads that have gone quiet.
//
// Without it an interrupted upload holds its .part file forever, and on a Pi
// with a 32GB card a handful of abandoned videos is enough to fill the disk.
func (m *Manager) Sweep() int {
	m.mu.Lock()
	var stale []*Session
	for id, s := range m.sessions {
		s.mu.Lock()
		idle := time.Since(s.lastSeen)
		s.mu.Unlock()
		if idle > m.ttl {
			stale = append(stale, s)
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()

	for _, s := range stale {
		s.mu.Lock()
		if !s.done {
			s.done = true
			s.w.Abort()
		}
		s.mu.Unlock()
	}
	return len(stale)
}

// CloseAll discards every in-flight upload, on shutdown or logout.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	all := make([]*Session, 0, len(m.sessions))
	for id, s := range m.sessions {
		all = append(all, s)
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	for _, s := range all {
		s.mu.Lock()
		if !s.done {
			s.done = true
			s.w.Abort()
		}
		s.mu.Unlock()
	}
}

// InFlight reports how many uploads are open.
func (m *Manager) InFlight() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

func (m *Manager) lookup(id string, userID int64) (*Session, error) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	// A session belonging to someone else is reported as missing rather than
	// forbidden: "wrong owner" would confirm the id exists.
	if !ok || s.UserID != userID {
		return nil, ErrNotFound
	}
	return s, nil
}

func newID() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("upload: generate id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/rverst/obscura/internal/crypto"
	"github.com/rverst/obscura/internal/store"
)

// Session is a logged-in session.
type Session struct {
	ID       string
	UserID   int64
	Username string

	// DB is the unlocked metadata database. It belongs to the vault, not to the
	// session -- several sessions of the same user share it.
	DB *sql.DB

	// DEK is needed for blob encryption. Also borrowed: after logout the slice
	// is zeroed and must not be used any more.
	DEK []byte

	CreatedAt time.Time
	LastSeen  time.Time
}

// vault holds a user's unlocked resources.
//
// There is exactly one vault per user no matter how many sessions are open:
// two concurrently opened connections to the same encrypted SQLite file would
// not just be wasteful, they would block each other on writes.
type vault struct {
	userID   int64
	username string
	dek      []byte
	db       *sql.DB
	refs     int // number of open sessions
}

// Manager owns sessions and their vaults.
//
// Everything lives in memory only. Restarting the server therefore necessarily
// ends every session and the DEK disappears -- that is intended and the core of
// the security model: with no active session the key exists nowhere.
type Manager struct {
	dataDir string
	ttl     time.Duration

	mu       sync.Mutex
	sessions map[string]*Session
	vaults   map[int64]*vault
}

// NewManager creates a session manager. ttl is the idle time after which a
// session expires.
func NewManager(dataDir string, ttl time.Duration) *Manager {
	return &Manager{
		dataDir:  dataDir,
		ttl:      ttl,
		sessions: make(map[string]*Session),
		vaults:   make(map[int64]*vault),
	}
}

// Unlock opens the user's vault (if not already open) and creates a session.
// Ownership of the passed DEK transfers to the manager.
func (m *Manager) Unlock(userID int64, username string, dek []byte) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	v, ok := m.vaults[userID]
	if ok {
		// Vault is already open, so the DEK we just derived is not needed. (It
		// is identical to the stored one; every slot unlocks the same key.)
		crypto.Zero(dek)
	} else {
		dbKey, err := crypto.DBKey(dek)
		if err != nil {
			crypto.Zero(dek)
			return nil, err
		}
		defer crypto.Zero(dbKey)

		db, err := store.OpenEncrypted(m.photosPath(), dbKey)
		if err != nil {
			crypto.Zero(dek)
			return nil, err
		}
		if err := store.Migrate(db, store.SetPhotos); err != nil {
			db.Close()
			crypto.Zero(dek)
			return nil, err
		}
		if err := ensureUser(db, userID, username); err != nil {
			db.Close()
			crypto.Zero(dek)
			return nil, err
		}
		v = &vault{userID: userID, username: username, dek: dek, db: db}
		m.vaults[userID] = v
	}

	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sess := &Session{
		ID:        id,
		UserID:    userID,
		Username:  username,
		DB:        v.db,
		DEK:       v.dek,
		CreatedAt: now,
		LastSeen:  now,
	}
	v.refs++
	m.sessions[id] = sess
	return sess, nil
}

// Get returns the session for an ID and refreshes its timestamp. Expired
// sessions are discarded along the way.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sess, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	if time.Since(sess.LastSeen) > m.ttl {
		m.dropLocked(id)
		return nil, false
	}
	sess.LastSeen = time.Now()
	return sess, true
}

// Logout ends a session. If it was the user's last one, the vault is closed and
// the DEK zeroed.
func (m *Manager) Logout(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropLocked(id)
}

// Sweep removes expired sessions. Called periodically so a forgotten browser
// tab does not hold the vault open indefinitely.
func (m *Manager) Sweep() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	var dropped int
	for id, sess := range m.sessions {
		if time.Since(sess.LastSeen) > m.ttl {
			m.dropLocked(id)
			dropped++
		}
	}
	return dropped
}

// CloseAll ends every session -- used on shutdown.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.sessions {
		m.dropLocked(id)
	}
}

// Locked reports whether no vault is currently open.
func (m *Manager) Locked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.vaults) == 0
}

// dropLocked expects m.mu to be held.
func (m *Manager) dropLocked(id string) {
	sess, ok := m.sessions[id]
	if !ok {
		return
	}
	delete(m.sessions, id)

	v, ok := m.vaults[sess.UserID]
	if !ok {
		return
	}
	v.refs--
	if v.refs > 0 {
		return
	}
	// Last session: close the vault.
	v.db.Close()
	crypto.Zero(v.dek)
	delete(m.vaults, sess.UserID)
}

func (m *Manager) photosPath() string { return m.dataDir + "/photos.db" }

func newSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate session ID: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ensureUser mirrors the user from auth.db into the encrypted database.
//
// Both databases have a users table: auth.db needs it for the key slots,
// photos.db for the foreign keys on media and albums. The IDs are deliberately
// kept identical.
func ensureUser(db *sql.DB, userID int64, username string) error {
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO users(id, username, created_at) VALUES(?, ?, unixepoch())
		ON CONFLICT(id) DO UPDATE SET username = excluded.username`,
		userID, username)
	if err != nil {
		return fmt.Errorf("auth: create user in photos.db: %w", err)
	}
	return nil
}

package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/rverst/obscura/internal/crypto"
)

// handleSize is the length of the WebAuthn user handle. The specification
// allows up to 64 bytes and recommends using them; 32 is plenty of collision
// resistance and keeps the credential small on space-constrained keys.
const handleSize = 32

// webauthnUser adapts a user row to the interface go-webauthn expects.
//
// Unexported on purpose: it exists only for the duration of a ceremony and
// carries a decoded copy of the credentials, which is not something the rest of
// the program should hold onto.
type webauthnUser struct {
	id     int64
	handle []byte
	name   string
	creds  []webauthn.Credential
}

func (u *webauthnUser) WebAuthnID() []byte                         { return u.handle }
func (u *webauthnUser) WebAuthnName() string                       { return u.name }
func (u *webauthnUser) WebAuthnDisplayName() string                { return u.name }
func (u *webauthnUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// user loads a user and their registered credentials.
//
// username may be empty, which resolves to the only account there is. That is
// what lets the login screen offer a bare "Use a passkey" button: a passkey
// already identifies its owner, so asking who they are first is a step that
// buys nothing. It fails as soon as a second account exists.
func (s *Store) user(ctx context.Context, username string) (*webauthnUser, error) {
	var (
		u      webauthnUser
		handle []byte
	)
	var row *sql.Row
	if username == "" {
		row = s.db.QueryRowContext(ctx,
			`SELECT id, username, webauthn_handle FROM users
			  WHERE (SELECT count(*) FROM users) = 1`)
	} else {
		row = s.db.QueryRowContext(ctx,
			`SELECT id, username, webauthn_handle FROM users WHERE username = ?`, username)
	}
	switch err := row.Scan(&u.id, &u.name, &handle); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, ErrUnlockFailed
	case err != nil:
		return nil, fmt.Errorf("auth: read user: %w", err)
	}
	u.handle = handle

	creds, err := s.credentials(ctx, u.id)
	if err != nil {
		return nil, err
	}
	u.creds = creds
	return &u, nil
}

// ensureHandle assigns a WebAuthn user handle if the account does not have one.
//
// Deferred until the first passkey rather than created at setup, because a
// library that never registers one should not carry the column at all.
func (s *Store) ensureHandle(ctx context.Context, u *webauthnUser) error {
	if len(u.handle) == handleSize {
		return nil
	}
	h := make([]byte, handleSize)
	if _, err := rand.Read(h); err != nil {
		return fmt.Errorf("auth: generate user handle: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET webauthn_handle = ? WHERE id = ?`, h, u.id); err != nil {
		return fmt.Errorf("auth: store user handle: %w", err)
	}
	u.handle = h
	return nil
}

// userByHandle resolves the handle an authenticator returns in an assertion.
func (s *Store) userByHandle(ctx context.Context, handle []byte) (*webauthnUser, error) {
	var u webauthnUser
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username FROM users WHERE webauthn_handle = ?`, handle).Scan(&u.id, &u.name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnlockFailed
	}
	if err != nil {
		return nil, fmt.Errorf("auth: read user by handle: %w", err)
	}
	u.handle = handle
	creds, err := s.credentials(ctx, u.id)
	if err != nil {
		return nil, err
	}
	u.creds = creds
	return &u, nil
}

func (s *Store) credentials(ctx context.Context, userID int64) ([]webauthn.Credential, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT record FROM webauthn_credentials WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("auth: read credentials: %w", err)
	}
	defer rows.Close()

	var out []webauthn.Credential
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, fmt.Errorf("auth: decode credential record: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PRFSalts returns the PRF salt of every passkey slot, keyed by credential ID.
//
// A salt per credential, not per user: the login ceremony hands the browser an
// evalByCredential map, so whichever authenticator the user reaches for
// evaluates the salt that belongs to it.
func (s *Store) PRFSalts(ctx context.Context, userID int64) (map[string][]byte, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT credential_id, prf_salt FROM key_slots
		  WHERE user_id = ? AND kind = ? AND credential_id IS NOT NULL`, userID, KindPasskey)
	if err != nil {
		return nil, fmt.Errorf("auth: read PRF salts: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]byte)
	for rows.Next() {
		var id, salt []byte
		if err := rows.Scan(&id, &salt); err != nil {
			return nil, err
		}
		out[string(id)] = salt
	}
	return out, rows.Err()
}

// AddPasskey stores a credential and the key slot it unlocks, in one
// transaction.
//
// The two belong together: a credential without a slot signs in but cannot
// decrypt anything, and a slot without a credential can never be reached. Both
// halves or neither.
func (s *Store) AddPasskey(ctx context.Context, userID int64, dek, unlockKey []byte, cred *webauthn.Credential, prfSalt []byte, label string) error {
	if len(unlockKey) != crypto.DEKSize {
		return fmt.Errorf("auth: unlock key is %d bytes, want %d", len(unlockKey), crypto.DEKSize)
	}
	if len(cred.ID) == 0 {
		return errors.New("auth: empty credential ID")
	}
	record, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("auth: encode credential record: %w", err)
	}
	// No Argon2id: the PRF result is already uniform high-entropy key material,
	// and stretching it would only make the passkey slower than the password.
	sealed, err := crypto.Wrap(unlockKey, dek)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("auth: begin transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO webauthn_credentials(credential_id, user_id, record, created_at)
		VALUES(?, ?, ?, ?)`, cred.ID, userID, string(record), now); err != nil {
		return fmt.Errorf("auth: store credential: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO key_slots(user_id, kind, label, wrapped_dek, credential_id, prf_salt, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		userID, KindPasskey, label, sealed, cred.ID, prfSalt, now); err != nil {
		return fmt.Errorf("auth: create passkey slot: %w", err)
	}
	return tx.Commit()
}

// updateCredential writes the record back after a successful assertion, which
// is what advances the signature counter used to spot cloned authenticators.
func (s *Store) updateCredential(ctx context.Context, userID int64, cred *webauthn.Credential) error {
	record, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("auth: encode credential record: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE webauthn_credentials SET record = ? WHERE credential_id = ? AND user_id = ?`,
		string(record), cred.ID, userID); err != nil {
		return fmt.Errorf("auth: update credential: %w", err)
	}
	return nil
}

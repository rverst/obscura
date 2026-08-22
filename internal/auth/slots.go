// Package auth manages unlock methods ("key slots") and access to the Data
// Encryption Key.
//
// A slot is one way to obtain the DEK. All slots of a user unlock the same DEK
// -- the model is borrowed from LUKS key slots. The practical benefit: changing
// the password rewrites one row instead of re-encrypting the whole library.
package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rverst/obscura/internal/crypto"
)

// Slot kinds.
const (
	KindPassword = "password"
	KindRecovery = "recovery"
	KindPasskey  = "passkey"
)

var (
	// ErrNotInitialized reports that setup has not run yet.
	ErrNotInitialized = errors.New("auth: not initialized")
	// ErrAlreadyInitialized reports a second setup attempt.
	ErrAlreadyInitialized = errors.New("auth: already initialized")
	// ErrUnlockFailed reports a failed unlock attempt.
	//
	// Deliberately a single error for "unknown user", "wrong password" and
	// "no such slot": anything else would disclose which accounts exist.
	ErrUnlockFailed = errors.New("auth: unlock failed")
)

// Store reads and writes the key slots in the unencrypted auth.db.
type Store struct {
	db *sql.DB
}

// NewStore binds a Store to an already migrated auth.db.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Slot describes an unlock method. The wrapped DEK is deliberately not part of
// this struct -- it is also used for display purposes.
type Slot struct {
	ID         int64
	UserID     int64
	Kind       string
	Label      string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// IsInitialized reports whether a user has been set up.
func (s *Store) IsInitialized(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return false, fmt.Errorf("auth: count users: %w", err)
	}
	return n > 0, nil
}

// Setup creates the first user: generates a fresh DEK and creates two slots --
// one for the password, one for the recovery code.
//
// The recovery code is returned exactly once and stored nowhere; only the DEK
// wrapped with it lives in the database. If it is lost together with the
// password, the library is gone for good -- the unavoidable flip side of the
// server not knowing the key.
func (s *Store) Setup(ctx context.Context, username, password string) (dek []byte, recoveryCode string, err error) {
	if username == "" {
		return nil, "", errors.New("auth: empty username")
	}
	if len(password) < 8 {
		return nil, "", errors.New("auth: password must be at least 8 characters")
	}

	init, err := s.IsInitialized(ctx)
	if err != nil {
		return nil, "", err
	}
	if init {
		return nil, "", ErrAlreadyInitialized
	}

	dek, err = crypto.NewDEK()
	if err != nil {
		return nil, "", err
	}
	recoveryCode, err = crypto.NewRecoveryCode()
	if err != nil {
		return nil, "", err
	}

	pwSlot, err := buildSlot([]byte(password), dek)
	if err != nil {
		return nil, "", err
	}
	recSlot, err := buildSlot([]byte(crypto.NormalizeRecoveryCode(recoveryCode)), dek)
	if err != nil {
		return nil, "", err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", fmt.Errorf("auth: begin transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO users(username, created_at) VALUES(?, ?)`, username, now)
	if err != nil {
		return nil, "", fmt.Errorf("auth: create user: %w", err)
	}
	userID, err := res.LastInsertId()
	if err != nil {
		return nil, "", err
	}

	for kind, sl := range map[string]wrapped{KindPassword: pwSlot, KindRecovery: recSlot} {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO key_slots(user_id, kind, label, kdf_params, wrapped_dek, created_at)
			VALUES(?, ?, '', ?, ?, ?)`,
			userID, kind, sl.params, sl.wrapped, now); err != nil {
			return nil, "", fmt.Errorf("auth: create %s slot: %w", kind, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, "", fmt.Errorf("auth: commit: %w", err)
	}
	return dek, recoveryCode, nil
}

// UnlockWithPassword unlocks the DEK through the password slot.
func (s *Store) UnlockWithPassword(ctx context.Context, username, password string) (int64, []byte, error) {
	return s.unlock(ctx, username, KindPassword, []byte(password))
}

// UnlockWithRecoveryCode unlocks the DEK through the recovery slot. Casing and
// separators in the input do not matter.
func (s *Store) UnlockWithRecoveryCode(ctx context.Context, username, code string) (int64, []byte, error) {
	return s.unlock(ctx, username, KindRecovery, []byte(crypto.NormalizeRecoveryCode(code)))
}

func (s *Store) unlock(ctx context.Context, username, kind string, secret []byte) (int64, []byte, error) {
	var (
		slotID  int64
		userID  int64
		rawKDF  []byte
		wrapped []byte
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT ks.id, ks.user_id, ks.kdf_params, ks.wrapped_dek
		  FROM key_slots ks
		  JOIN users u ON u.id = ks.user_id
		 WHERE u.username = ? AND ks.kind = ?`, username, kind).
		Scan(&slotID, &userID, &rawKDF, &wrapped)

	if errors.Is(err, sql.ErrNoRows) {
		// Still run a derivation so an unknown username is not detectable from
		// the shorter response time.
		burnTime(secret)
		return 0, nil, ErrUnlockFailed
	}
	if err != nil {
		return 0, nil, fmt.Errorf("auth: read slot: %w", err)
	}

	var params crypto.KDFParams
	if err := json.Unmarshal(rawKDF, &params); err != nil {
		return 0, nil, fmt.Errorf("auth: KDF parameters of slot %d: %w", slotID, err)
	}

	kek := params.Derive(secret)
	defer crypto.Zero(kek)

	dek, err := crypto.Unwrap(kek, wrapped)
	if err != nil {
		return 0, nil, ErrUnlockFailed
	}

	s.touch(ctx, slotID)
	return userID, dek, nil
}

// UnlockWithKey unlocks through a slot whose KEK is already available -- the
// passkey case, where the key comes from the WebAuthn PRF extension and no
// Argon2id derivation is needed.
func (s *Store) UnlockWithKey(ctx context.Context, credentialID, unlockKey []byte) (int64, []byte, error) {
	var (
		slotID  int64
		userID  int64
		wrapped []byte
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, wrapped_dek FROM key_slots
		 WHERE kind = ? AND credential_id = ?`, KindPasskey, credentialID).
		Scan(&slotID, &userID, &wrapped)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, ErrUnlockFailed
	}
	if err != nil {
		return 0, nil, fmt.Errorf("auth: read passkey slot: %w", err)
	}

	dek, err := crypto.Unwrap(unlockKey, wrapped)
	if err != nil {
		return 0, nil, ErrUnlockFailed
	}
	s.touch(ctx, slotID)
	return userID, dek, nil
}

// ChangePassword swaps the password without touching the DEK.
//
// This is where the slot model pays off: one row is rewritten. Every blob and
// the whole database stay untouched.
func (s *Store) ChangePassword(ctx context.Context, username, oldPassword, newPassword string) error {
	if len(newPassword) < 8 {
		return errors.New("auth: password must be at least 8 characters")
	}
	_, dek, err := s.UnlockWithPassword(ctx, username, oldPassword)
	if err != nil {
		return err
	}
	defer crypto.Zero(dek)

	sl, err := buildSlot([]byte(newPassword), dek)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE key_slots SET kdf_params = ?, wrapped_dek = ?
		 WHERE kind = ? AND user_id = (SELECT id FROM users WHERE username = ?)`,
		sl.params, sl.wrapped, KindPassword, username)
	if err != nil {
		return fmt.Errorf("auth: update password slot: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("auth: updated %d slots, want 1", n)
	}
	return nil
}

// AddPasskeySlot creates a slot unlocked by a WebAuthn PRF key. unlockKey is
// the material the browser derived from the PRF result via HKDF -- the raw PRF
// output never leaves the device.
func (s *Store) AddPasskeySlot(ctx context.Context, userID int64, dek, unlockKey, credentialID, prfSalt []byte, label string) error {
	if len(unlockKey) != crypto.DEKSize {
		return fmt.Errorf("auth: unlock key is %d bytes, want %d", len(unlockKey), crypto.DEKSize)
	}
	if len(credentialID) == 0 {
		return errors.New("auth: empty credential ID")
	}
	// No Argon2id: the PRF result is already high-entropy key material.
	wrapped, err := crypto.Wrap(unlockKey, dek)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO key_slots(user_id, kind, label, wrapped_dek, credential_id, prf_salt, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		userID, KindPasskey, label, wrapped, credentialID, prfSalt, time.Now().Unix()); err != nil {
		return fmt.Errorf("auth: create passkey slot: %w", err)
	}
	return nil
}

// ListSlots returns all unlock methods of a user.
func (s *Store) ListSlots(ctx context.Context, userID int64) ([]Slot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, kind, label, created_at, last_used_at
		  FROM key_slots WHERE user_id = ? ORDER BY kind, created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("auth: read slots: %w", err)
	}
	defer rows.Close()

	var out []Slot
	for rows.Next() {
		var sl Slot
		var created int64
		var used sql.NullInt64
		if err := rows.Scan(&sl.ID, &sl.UserID, &sl.Kind, &sl.Label, &created, &used); err != nil {
			return nil, err
		}
		sl.CreatedAt = time.Unix(created, 0)
		if used.Valid {
			t := time.Unix(used.Int64, 0)
			sl.LastUsedAt = &t
		}
		out = append(out, sl)
	}
	return out, rows.Err()
}

// RemoveSlot deletes an unlock method.
//
// The password and recovery slots are protected: if they could be deleted, a
// user could lock themselves out permanently with a single click.
func (s *Store) RemoveSlot(ctx context.Context, userID, slotID int64) error {
	var (
		kind   string
		credID []byte
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT kind, credential_id FROM key_slots WHERE id = ? AND user_id = ?`,
		slotID, userID).Scan(&kind, &credID)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("auth: slot not found")
	}
	if err != nil {
		return fmt.Errorf("auth: read slot: %w", err)
	}
	if kind != KindPasskey {
		return fmt.Errorf("auth: slots of kind %q cannot be removed", kind)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("auth: begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM key_slots WHERE id = ? AND user_id = ?`, slotID, userID); err != nil {
		return fmt.Errorf("auth: delete slot: %w", err)
	}
	// The credential goes with it. Leaving it behind would keep offering the
	// passkey at the login screen, where it would authenticate successfully and
	// then fail to unlock -- the most confusing possible outcome.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM webauthn_credentials WHERE credential_id = ? AND user_id = ?`,
		credID, userID); err != nil {
		return fmt.Errorf("auth: delete credential: %w", err)
	}
	return tx.Commit()
}

// --- internal ---------------------------------------------------------------

type wrapped struct {
	// KDFParams as JSON. Deliberately a string and not []byte: kdf_params is a
	// TEXT column in a STRICT table, and a []byte would bind as a BLOB and be
	// rejected.
	params  string
	wrapped []byte
}

// buildSlot derives a KEK from a secret and seals the DEK with it.
func buildSlot(secret, dek []byte) (wrapped, error) {
	params, err := crypto.NewKDFParams()
	if err != nil {
		return wrapped{}, err
	}
	kek := params.Derive(secret)
	defer crypto.Zero(kek)

	w, err := crypto.Wrap(kek, dek)
	if err != nil {
		return wrapped{}, err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return wrapped{}, fmt.Errorf("auth: marshal KDF parameters: %w", err)
	}
	return wrapped{params: string(raw), wrapped: w}, nil
}

// burnTime spends the same CPU time on an unknown user as a real unlock would,
// so response timing does not disclose which accounts exist.
func burnTime(secret []byte) {
	if p, err := crypto.NewKDFParams(); err == nil {
		crypto.Zero(p.Derive(secret))
	}
}

func (s *Store) touch(ctx context.Context, slotID int64) {
	// Purely informational ("last used"); a failure here must not make the
	// unlock fail.
	_, _ = s.db.ExecContext(ctx,
		`UPDATE key_slots SET last_used_at = ? WHERE id = ?`, time.Now().Unix(), slotID)
}

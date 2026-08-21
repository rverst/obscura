package auth

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rverst/obscura/internal/store"
)

func newStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db, err := store.OpenPlain(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db, store.SetAuth); err != nil {
		t.Fatal(err)
	}
	return NewStore(db), db
}

func setup(t *testing.T, s *Store) (dek []byte, recovery string) {
	t.Helper()
	dek, recovery, err := s.Setup(context.Background(), "ralf", "a-good-password")
	if err != nil {
		t.Fatal(err)
	}
	return dek, recovery
}

func TestSetupAndUnlock(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	if init, err := s.IsInitialized(ctx); err != nil || init {
		t.Fatalf("a fresh database reports as initialized (init=%v, err=%v)", init, err)
	}

	dek, recovery := setup(t, s)
	if len(dek) != 32 {
		t.Fatalf("DEK is %d bytes", len(dek))
	}

	if init, err := s.IsInitialized(ctx); err != nil || !init {
		t.Fatalf("not recognized as initialized after setup (init=%v, err=%v)", init, err)
	}

	// Password and recovery code must yield the same DEK -- otherwise data
	// encrypted under one would be unreadable through the other.
	_, viaPw, err := s.UnlockWithPassword(ctx, "ralf", "a-good-password")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(viaPw, dek) {
		t.Fatal("the password yields a different DEK than setup returned")
	}

	_, viaRec, err := s.UnlockWithRecoveryCode(ctx, "ralf", recovery)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(viaRec, dek) {
		t.Fatal("the recovery code yields a different DEK")
	}
}

func TestSecondSetupRejected(t *testing.T) {
	s, _ := newStore(t)
	setup(t, s)
	if _, _, err := s.Setup(context.Background(), "second", "another-password"); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("want ErrAlreadyInitialized, got: %v", err)
	}
}

func TestSetupRejectsShortPassword(t *testing.T) {
	s, _ := newStore(t)
	if _, _, err := s.Setup(context.Background(), "ralf", "short"); err == nil {
		t.Fatal("a too-short password was accepted")
	}
}

func TestWrongPassword(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	setup(t, s)

	if _, _, err := s.UnlockWithPassword(ctx, "ralf", "wrong-wrong"); !errors.Is(err, ErrUnlockFailed) {
		t.Fatalf("want ErrUnlockFailed, got: %v", err)
	}
}

// An unknown username must produce the same error as a wrong password --
// otherwise it is possible to find out which accounts exist.
func TestUnknownUserIsIndistinguishable(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	setup(t, s)

	_, _, errUnknown := s.UnlockWithPassword(ctx, "nosuchuser", "a-good-password")
	_, _, errWrong := s.UnlockWithPassword(ctx, "ralf", "wrong-password")

	if !errors.Is(errUnknown, ErrUnlockFailed) || !errors.Is(errWrong, ErrUnlockFailed) {
		t.Fatalf("different errors: unknown=%v wrong=%v", errUnknown, errWrong)
	}
	if errUnknown.Error() != errWrong.Error() {
		t.Fatalf("error texts reveal the difference: %q vs %q", errUnknown, errWrong)
	}
}

func TestRecoveryCodeToleratesSpelling(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	dek, recovery := setup(t, s)

	// This is how someone copies the code off a piece of paper.
	inputs := []string{
		strings.ToLower(recovery),
		strings.ReplaceAll(recovery, "-", ""),
		strings.ReplaceAll(recovery, "-", " "),
		"  " + recovery + "  ",
	}
	for _, in := range inputs {
		_, got, err := s.UnlockWithRecoveryCode(ctx, "ralf", in)
		if err != nil {
			t.Fatalf("input %q was rejected: %v", in, err)
		}
		if !bytes.Equal(got, dek) {
			t.Fatalf("input %q yielded the wrong DEK", in)
		}
	}
}

// The whole reason for the slot model: after a password change the DEK must be
// unchanged, otherwise every photo would need re-encrypting. And the recovery
// code must keep working, because it lives in its own untouched slot.
func TestPasswordChange(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	dek, recovery := setup(t, s)

	if err := s.ChangePassword(ctx, "ralf", "a-good-password", "a-new-password"); err != nil {
		t.Fatal(err)
	}

	_, updated, err := s.UnlockWithPassword(ctx, "ralf", "a-new-password")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(updated, dek) {
		t.Fatal("the DEK changed during the password change -- every blob would be unreadable")
	}

	if _, _, err := s.UnlockWithPassword(ctx, "ralf", "a-good-password"); !errors.Is(err, ErrUnlockFailed) {
		t.Fatal("the old password still works")
	}

	_, viaRec, err := s.UnlockWithRecoveryCode(ctx, "ralf", recovery)
	if err != nil {
		t.Fatalf("recovery code broken after the password change: %v", err)
	}
	if !bytes.Equal(viaRec, dek) {
		t.Fatal("the recovery code yields a different DEK after the password change")
	}
}

func TestPasswordChangeWithWrongOldPassword(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	setup(t, s)

	if err := s.ChangePassword(ctx, "ralf", "wrong", "new-password"); !errors.Is(err, ErrUnlockFailed) {
		t.Fatalf("want ErrUnlockFailed, got: %v", err)
	}
	// The old password must still be valid.
	if _, _, err := s.UnlockWithPassword(ctx, "ralf", "a-good-password"); err != nil {
		t.Fatalf("old password broken after a failed change: %v", err)
	}
}

func TestPasskeySlot(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	dek, _ := setup(t, s)

	userID, _, err := s.UnlockWithPassword(ctx, "ralf", "a-good-password")
	if err != nil {
		t.Fatal(err)
	}

	// Stands in for HKDF(WebAuthn PRF result).
	unlockKey := bytes.Repeat([]byte{0xAB}, 32)
	credID := []byte("credential-id-1")
	prfSalt := bytes.Repeat([]byte{0x11}, 32)

	if err := s.AddPasskeySlot(ctx, userID, dek, unlockKey, credID, prfSalt, "iPhone"); err != nil {
		t.Fatal(err)
	}

	_, viaPasskey, err := s.UnlockWithKey(ctx, credID, unlockKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(viaPasskey, dek) {
		t.Fatal("the passkey yields a different DEK")
	}

	// A wrong unlock key must not get through.
	if _, _, err := s.UnlockWithKey(ctx, credID, bytes.Repeat([]byte{0xCD}, 32)); !errors.Is(err, ErrUnlockFailed) {
		t.Fatalf("wrong unlock key accepted: %v", err)
	}
	// Neither must an unknown credential ID.
	if _, _, err := s.UnlockWithKey(ctx, []byte("nosuchcred"), unlockKey); !errors.Is(err, ErrUnlockFailed) {
		t.Fatalf("unknown credential ID accepted: %v", err)
	}
}

func TestListAndRemoveSlots(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	dek, _ := setup(t, s)

	userID, _, err := s.UnlockWithPassword(ctx, "ralf", "a-good-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddPasskeySlot(ctx, userID, dek,
		bytes.Repeat([]byte{0xAB}, 32), []byte("cred-1"), nil, "iPhone"); err != nil {
		t.Fatal(err)
	}

	slots, err := s.ListSlots(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 3 {
		t.Fatalf("%d slots, want 3 (password, recovery, passkey)", len(slots))
	}

	byKind := map[string]int64{}
	for _, sl := range slots {
		byKind[sl.Kind] = sl.ID
	}

	// The password and recovery slots are protected, otherwise a single click
	// could lock the user out permanently.
	for _, kind := range []string{KindPassword, KindRecovery} {
		if err := s.RemoveSlot(ctx, userID, byKind[kind]); err == nil {
			t.Fatalf("slot %q could be removed", kind)
		}
	}
	if err := s.RemoveSlot(ctx, userID, byKind[KindPasskey]); err != nil {
		t.Fatalf("the passkey slot could not be removed: %v", err)
	}

	slots, _ = s.ListSlots(ctx, userID)
	if len(slots) != 2 {
		t.Fatalf("%d slots after removal, want 2", len(slots))
	}
}

// auth.db is unencrypted -- so nothing that means anything without the password
// may end up in it.
func TestAuthDBHoldsNoPlaintextKey(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	dek, recovery := setup(t, s)

	rows, err := db.QueryContext(ctx, `SELECT wrapped_dek, kdf_params FROM key_slots`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var w, p []byte
		if err := rows.Scan(&w, &p); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(w, dek) {
			t.Fatal("the DEK is stored in plaintext in wrapped_dek")
		}
		if bytes.Contains(p, []byte(strings.ReplaceAll(recovery, "-", ""))) {
			t.Fatal("the recovery code is stored in the KDF parameters")
		}
	}
}

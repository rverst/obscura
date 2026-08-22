package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

func testCredential(t *testing.T, id string) *webauthn.Credential {
	t.Helper()
	pk := make([]byte, 64)
	if _, err := rand.Read(pk); err != nil {
		t.Fatal(err)
	}
	return &webauthn.Credential{
		ID:              []byte(id),
		PublicKey:       pk,
		AttestationType: "none",
	}
}

func randomKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

// The property the whole slot model rests on: a passkey unwraps the very same
// DEK the password does. If these differed, photos written before enrolling the
// passkey would be unreadable through it.
func TestPasskeyUnlocksTheSameDEK(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	dek, _ := setup(t, s)

	unlockKey := randomKey(t)
	cred := testCredential(t, "cred-a")
	if err := s.AddPasskey(ctx, 1, dek, unlockKey, cred, []byte("salt-a"), "Phone"); err != nil {
		t.Fatal(err)
	}

	userID, got, err := s.UnlockWithKey(ctx, cred.ID, unlockKey)
	if err != nil {
		t.Fatal(err)
	}
	if userID != 1 {
		t.Fatalf("userID = %d, want 1", userID)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("the passkey unwrapped a different key than the password")
	}
}

func TestPasskeyRejectsTheWrongKey(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	dek, _ := setup(t, s)

	cred := testCredential(t, "cred-a")
	if err := s.AddPasskey(ctx, 1, dek, randomKey(t), cred, []byte("salt-a"), "Phone"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UnlockWithKey(ctx, cred.ID, randomKey(t)); !errors.Is(err, ErrUnlockFailed) {
		t.Fatalf("want ErrUnlockFailed, got %v", err)
	}
	// An unknown credential must look exactly the same from outside.
	if _, _, err := s.UnlockWithKey(ctx, []byte("nope"), randomKey(t)); !errors.Is(err, ErrUnlockFailed) {
		t.Fatalf("want ErrUnlockFailed, got %v", err)
	}
}

// A slot and its credential are one thing in two tables. Removing the slot must
// take the credential with it -- otherwise the login screen keeps offering a
// passkey that authenticates and then cannot decrypt.
func TestRemovingAPasskeyTakesTheCredential(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	dek, _ := setup(t, s)

	unlockKey := randomKey(t)
	cred := testCredential(t, "cred-a")
	if err := s.AddPasskey(ctx, 1, dek, unlockKey, cred, []byte("salt-a"), "Phone"); err != nil {
		t.Fatal(err)
	}

	var slotID int64
	if err := db.QueryRow(
		`SELECT id FROM key_slots WHERE kind = 'passkey'`).Scan(&slotID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveSlot(ctx, 1, slotID); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM webauthn_credentials`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d credentials survived the slot", n)
	}
	if _, _, err := s.UnlockWithKey(ctx, cred.ID, unlockKey); !errors.Is(err, ErrUnlockFailed) {
		t.Fatalf("the removed passkey still unlocks: %v", err)
	}
	// The password must be untouched by all of this.
	if _, got, err := s.UnlockWithPassword(ctx, "ralf", "a-good-password"); err != nil {
		t.Fatal(err)
	} else if !bytes.Equal(got, dek) {
		t.Fatal("removing a passkey changed the DEK")
	}
}

func TestPasskeySurvivesAPasswordChange(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	dek, _ := setup(t, s)

	unlockKey := randomKey(t)
	cred := testCredential(t, "cred-a")
	if err := s.AddPasskey(ctx, 1, dek, unlockKey, cred, []byte("salt-a"), "Phone"); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, "ralf", "a-good-password", "an-even-better-one"); err != nil {
		t.Fatal(err)
	}
	// Only the password slot is rewritten, so the passkey keeps working.
	if _, got, err := s.UnlockWithKey(ctx, cred.ID, unlockKey); err != nil {
		t.Fatal(err)
	} else if !bytes.Equal(got, dek) {
		t.Fatal("the passkey no longer yields the DEK")
	}
}

func TestPRFSaltsAreKeyedByCredential(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	dek, _ := setup(t, s)

	for _, tc := range []struct{ id, salt string }{
		{"cred-a", "salt-a"},
		{"cred-b", "salt-b"},
	} {
		if err := s.AddPasskey(ctx, 1, dek, randomKey(t), testCredential(t, tc.id), []byte(tc.salt), tc.id); err != nil {
			t.Fatal(err)
		}
	}

	salts, err := s.PRFSalts(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(salts) != 2 {
		t.Fatalf("%d salts, want 2", len(salts))
	}
	if string(salts["cred-a"]) != "salt-a" || string(salts["cred-b"]) != "salt-b" {
		t.Fatalf("salts do not follow their credentials: %v", salts)
	}
}

func TestUserHandleIsRandomAndStable(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	setup(t, s)

	u, err := s.user(ctx, "ralf")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.handle) != 0 {
		t.Fatal("a fresh account should not carry a handle yet")
	}
	if err := s.ensureHandle(ctx, u); err != nil {
		t.Fatal(err)
	}
	first := append([]byte(nil), u.handle...)
	if len(first) != handleSize {
		t.Fatalf("handle is %d bytes, want %d", len(first), handleSize)
	}
	// It must not be the row id in disguise -- that would tell anyone holding
	// the authenticator which account on the server this is.
	if first[0] == 0 && first[len(first)-1] == 1 {
		t.Fatal("the handle looks like an encoded row id")
	}

	again, err := s.user(ctx, "ralf")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ensureHandle(ctx, again); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, again.handle) {
		t.Fatal("the handle changed on the second call")
	}

	byHandle, err := s.userByHandle(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if byHandle.id != 1 || byHandle.name != "ralf" {
		t.Fatalf("handle resolved to %d/%q", byHandle.id, byHandle.name)
	}
	if _, err := s.userByHandle(ctx, []byte("not a handle")); !errors.Is(err, ErrUnlockFailed) {
		t.Fatalf("want ErrUnlockFailed, got %v", err)
	}
}

// An empty username resolves to the only account, which is what lets the login
// screen show a bare "Use a passkey" button. It has to stop doing that the
// moment a second account exists.
func TestEmptyUsernameResolvesOnlyWhileThereIsOneAccount(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	setup(t, s)

	u, err := s.user(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if u.name != "ralf" {
		t.Fatalf("resolved to %q", u.name)
	}

	if _, err := db.Exec(
		`INSERT INTO users(username, created_at) VALUES('eve', unixepoch())`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.user(ctx, ""); !errors.Is(err, ErrUnlockFailed) {
		t.Fatalf("want ErrUnlockFailed with two accounts, got %v", err)
	}
	if _, err := s.user(ctx, "ralf"); err != nil {
		t.Fatalf("naming the account must still work: %v", err)
	}
}

func TestCredentialRecordSurvivesTheRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	dek, _ := setup(t, s)

	cred := testCredential(t, "cred-a")
	// The flags are the reason the record is stored whole: go-webauthn checks
	// them for consistency on every later login.
	cred.Flags = webauthn.CredentialFlags{
		UserPresent: true, UserVerified: true, BackupEligible: true, BackupState: true,
	}
	if err := s.AddPasskey(ctx, 1, dek, randomKey(t), cred, []byte("salt"), "Phone"); err != nil {
		t.Fatal(err)
	}

	got, err := s.credentials(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d credentials, want 1", len(got))
	}
	if !bytes.Equal(got[0].PublicKey, cred.PublicKey) {
		t.Fatal("the public key did not survive")
	}
	if got[0].Flags != cred.Flags {
		t.Fatalf("flags = %+v, want %+v", got[0].Flags, cred.Flags)
	}
}

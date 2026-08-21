package crypto

import (
	"bytes"
	"strings"
	"testing"
)

func mustKDF(t *testing.T) KDFParams {
	t.Helper()
	// Deliberately weak parameters for tests -- otherwise every run takes
	// seconds instead of milliseconds.
	p, err := NewKDFParams()
	if err != nil {
		t.Fatal(err)
	}
	p.Memory = 8 * 1024
	p.Time = 1
	return p
}

func TestWrapUnwrapRoundtrip(t *testing.T) {
	dek, err := NewDEK()
	if err != nil {
		t.Fatal(err)
	}
	p := mustKDF(t)
	kek := p.Derive([]byte("correct-horse-battery-staple"))

	wrapped, err := Wrap(kek, dek)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wrapped, dek) {
		t.Fatal("the DEK appears in plaintext inside the wrapped blob")
	}

	got, err := Unwrap(kek, wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("unwrapped DEK differs")
	}
}

func TestUnwrapWrongPassword(t *testing.T) {
	dek, _ := NewDEK()
	p := mustKDF(t)
	wrapped, err := Wrap(p.Derive([]byte("right")), dek)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Unwrap(p.Derive([]byte("wrong")), wrapped); err != ErrWrongKey {
		t.Fatalf("want ErrWrongKey, got: %v", err)
	}
}

func TestUnwrapTampered(t *testing.T) {
	dek, _ := NewDEK()
	p := mustKDF(t)
	kek := p.Derive([]byte("pw"))
	wrapped, _ := Wrap(kek, dek)

	// Flip every single byte -- no variant may slip through.
	for i := range wrapped {
		tampered := bytes.Clone(wrapped)
		tampered[i] ^= 0x01
		if _, err := Unwrap(kek, tampered); err != ErrWrongKey {
			t.Fatalf("byte %d tampered, but Unwrap returned %v", i, err)
		}
	}
}

func TestWrapIsNotDeterministic(t *testing.T) {
	dek, _ := NewDEK()
	kek := mustKDF(t).Derive([]byte("pw"))
	a, _ := Wrap(kek, dek)
	b, _ := Wrap(kek, dek)
	if bytes.Equal(a, b) {
		t.Fatal("wrapping twice produced identical bytes -- the nonce is being reused")
	}
}

// This is the central property of the slot model: a password change only
// rewraps, the DEK stays the same. Were that not so, every password change
// would require re-encrypting the entire library.
func TestPasswordChangeKeepsDEK(t *testing.T) {
	dek, _ := NewDEK()

	oldP := mustKDF(t)
	oldSlot, _ := Wrap(oldP.Derive([]byte("old-password")), dek)

	// Change: unlock with the old password, rewrap with the new one.
	unlocked, err := Unwrap(oldP.Derive([]byte("old-password")), oldSlot)
	if err != nil {
		t.Fatal(err)
	}
	newP := mustKDF(t)
	newSlot, _ := Wrap(newP.Derive([]byte("new-password")), unlocked)

	again, err := Unwrap(newP.Derive([]byte("new-password")), newSlot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, dek) {
		t.Fatal("the DEK changed during the password change -- every blob would be unreadable")
	}
	if _, err := Unwrap(oldP.Derive([]byte("old-password")), newSlot); err != ErrWrongKey {
		t.Fatal("the new slot still opens with the old password")
	}
}

func TestFileKeyDiffersPerBlob(t *testing.T) {
	dek, _ := NewDEK()

	a, err := FileKey(dek, "blob-aaa")
	if err != nil {
		t.Fatal(err)
	}
	b, err := FileKey(dek, "blob-bbb")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two blobs share the same key")
	}
	if bytes.Equal(a, dek) {
		t.Fatal("the file key is the DEK itself")
	}

	// Derivation must be reproducible, otherwise blobs would be unreadable
	// after a restart.
	again, _ := FileKey(dek, "blob-aaa")
	if !bytes.Equal(a, again) {
		t.Fatal("FileKey is not deterministic")
	}

	dbKey, err := DBKey(dek)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(dbKey, a) || bytes.Equal(dbKey, dek) {
		t.Fatal("the DB key collides with a blob key or the DEK")
	}
}

func TestRecoveryCodeFormat(t *testing.T) {
	code, err := NewRecoveryCode()
	if err != nil {
		t.Fatal(err)
	}
	if want := "XXXX-XXXX-XXXX-XXXX-XXXX-XXXX"; len(code) != len(want) {
		t.Fatalf("length %d (%q), want %d", len(code), code, len(want))
	}
	if n := len(NormalizeRecoveryCode(code)); n != recoveryChars {
		t.Fatalf("normalized to %d characters, want %d", n, recoveryChars)
	}
	for _, r := range strings.ReplaceAll(code, "-", "") {
		if !strings.ContainsRune(recoveryAlphabet, r) {
			t.Fatalf("character %q is not in the alphabet", r)
		}
	}

	// Two codes must not be equal.
	other, _ := NewRecoveryCode()
	if code == other {
		t.Fatal("two recovery codes were identical")
	}
}

func TestNormalizeRecoveryCodeToleratesTypos(t *testing.T) {
	// Exactly the confusions that happen when copying from a piece of paper.
	cases := map[string]string{
		"abcd-efgh":   "ABCDEFGH",
		"ABCD EFGH":   "ABCDEFGH",
		"0BCD":        "0BCD",
		"OBCD":        "0BCD", // O -> 0
		"1BCD":        "1BCD",
		"IBCD":        "1BCD", // I -> 1
		"LBCD":        "1BCD", // L -> 1
		"UBCD":        "VBCD", // U -> V
		"  abcd  ":    "ABCD",
		"ab-cd-ef-gh": "ABCDEFGH",
	}
	for in, want := range cases {
		if got := NormalizeRecoveryCode(in); got != want {
			t.Errorf("NormalizeRecoveryCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRecoveryCodeUnlocksSlot(t *testing.T) {
	dek, _ := NewDEK()
	code, _ := NewRecoveryCode()

	p := mustKDF(t)
	slot, err := Wrap(p.Derive([]byte(NormalizeRecoveryCode(code))), dek)
	if err != nil {
		t.Fatal(err)
	}

	// The user types it in lower case with spaces instead of dashes.
	input := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	got, err := Unwrap(p.Derive([]byte(NormalizeRecoveryCode(input))), slot)
	if err != nil {
		t.Fatalf("recovery code in a different spelling was rejected: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("wrong DEK")
	}
}

func TestZero(t *testing.T) {
	b := []byte{1, 2, 3, 4}
	Zero(b)
	for i, v := range b {
		if v != 0 {
			t.Fatalf("byte %d is %d, want 0", i, v)
		}
	}
}

// Package crypto holds obscura's key management.
//
// The model follows the LUKS principle: there is exactly one random Data
// Encryption Key (DEK) that encrypts both the database and the blobs. The DEK
// itself is never stored in the clear -- only wrapped, once per unlock method
// (password, recovery code, passkey).
//
// The point of that indirection: changing the password rewraps a single slot.
// The DEK stays the same, so not a single photo needs re-encrypting.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

// DEKSize is the length of the Data Encryption Key in bytes (AES-256).
const DEKSize = 32

// ErrWrongKey reports that a slot could not be unlocked with the given secret --
// wrong password, wrong recovery code, or a tampered slot. The three cases are
// deliberately not distinguished.
var ErrWrongKey = errors.New("crypto: wrong key")

// NewDEK generates a fresh Data Encryption Key.
func NewDEK() ([]byte, error) {
	dek := make([]byte, DEKSize)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("crypto: generate DEK: %w", err)
	}
	return dek, nil
}

// Zero overwrites a key in memory.
//
// This is hygiene, not a guarantee: Go may leave copies behind when slices are
// copied or moved by the GC, and it does not help at all against an attacker
// who can read the running process. It merely shortens the window.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// --- Argon2id ---------------------------------------------------------------

// KDFParams describes one Argon2id derivation. Parameters are stored per slot
// so their cost can be raised later without invalidating existing slots.
type KDFParams struct {
	Salt    []byte `json:"salt"`
	Time    uint32 `json:"t"`
	Memory  uint32 `json:"m"` // in KiB
	Threads uint8  `json:"p"`
}

// Defaults, measured in the M0 spike: ~70 ms on Apple Silicon, extrapolating to
// ~350 ms on a Raspberry Pi 4.
//
// Deliberately not higher: 256 MiB would push a 2 GB Pi into swapping during
// login -- and swapped-out memory lands unencrypted on exactly the card we are
// trying to protect.
const (
	defaultTime    = 3
	defaultMemory  = 64 * 1024 // 64 MiB
	defaultThreads = 2
	saltSize       = 16
)

// NewKDFParams returns the default parameters with a fresh salt.
func NewKDFParams() (KDFParams, error) {
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return KDFParams{}, fmt.Errorf("crypto: generate salt: %w", err)
	}
	return KDFParams{
		Salt:    salt,
		Time:    defaultTime,
		Memory:  defaultMemory,
		Threads: defaultThreads,
	}, nil
}

// Derive turns a secret (password, recovery code) into the Key Encryption Key
// that wraps and unwraps a slot.
func (p KDFParams) Derive(secret []byte) []byte {
	return argon2.IDKey(secret, p.Salt, p.Time, p.Memory, p.Threads, DEKSize)
}

// --- Key wrapping -----------------------------------------------------------

// Wrap seals the DEK with a KEK (AES-256-GCM).
//
// GCM rather than AES-KW because the auth tag doubles as the "was the key
// correct?" check -- no separate password verifier is needed, which would
// itself be another thing to attack.
func Wrap(kek, dek []byte) ([]byte, error) {
	aead, err := newAEAD(kek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("crypto: generate nonce: %w", err)
	}
	// The nonce is prepended to the ciphertext.
	return aead.Seal(nonce, nonce, dek, nil), nil
}

// Unwrap opens a wrapped DEK. A wrong KEK yields ErrWrongKey.
func Unwrap(kek, wrapped []byte) ([]byte, error) {
	aead, err := newAEAD(kek)
	if err != nil {
		return nil, err
	}
	ns := aead.NonceSize()
	if len(wrapped) < ns+aead.Overhead() {
		return nil, ErrWrongKey
	}
	dek, err := aead.Open(nil, wrapped[:ns], wrapped[ns:], nil)
	if err != nil {
		return nil, ErrWrongKey
	}
	if len(dek) != DEKSize {
		return nil, ErrWrongKey
	}
	return dek, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != DEKSize {
		return nil, fmt.Errorf("crypto: key is %d bytes, want %d", len(key), DEKSize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: aes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: gcm: %w", err)
	}
	return aead, nil
}

// --- Derived keys -----------------------------------------------------------

// FileKey derives a per-blob key from the DEK.
//
// Every blob gets its own key so the DEK is never used directly as a cipher
// key, and so nonce reuse across two files is impossible by construction.
func FileKey(dek []byte, blobID string) ([]byte, error) {
	return subkey(dek, "obscura/blob/v1:"+blobID)
}

// DBKey derives the key for the encrypted metadata database.
func DBKey(dek []byte) ([]byte, error) {
	return subkey(dek, "obscura/db/v1")
}

func subkey(dek []byte, info string) ([]byte, error) {
	if len(dek) != DEKSize {
		return nil, fmt.Errorf("crypto: DEK is %d bytes, want %d", len(dek), DEKSize)
	}
	key, err := hkdf.Expand(sha256.New, dek, info, DEKSize)
	if err != nil {
		return nil, fmt.Errorf("crypto: hkdf: %w", err)
	}
	return key, nil
}

// --- Recovery code ----------------------------------------------------------

// Crockford base32: no I, L, O or U, so a code written down by hand does not
// fail on ambiguous characters.
const recoveryAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const (
	recoveryChars = 24 // 24 * 5 bits = 120 bits of entropy
	recoveryGroup = 4
)

// NewRecoveryCode generates a recovery code of the form
// XXXX-XXXX-XXXX-XXXX-XXXX-XXXX.
//
// The code is the only way back if the password is lost: without it the library
// is gone for good. It is shown exactly once during setup and never stored --
// only the DEK wrapped with it.
func NewRecoveryCode() (string, error) {
	var sb strings.Builder
	buf := make([]byte, recoveryChars)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("crypto: generate recovery code: %w", err)
	}
	for i, b := range buf {
		if i > 0 && i%recoveryGroup == 0 {
			sb.WriteByte('-')
		}
		// Modulo bias is negligible here: 256 % 32 == 0.
		sb.WriteByte(recoveryAlphabet[int(b)%len(recoveryAlphabet)])
	}
	return sb.String(), nil
}

// NormalizeRecoveryCode makes user input comparable: upper case, separators
// stripped, and the usual transcription mistakes corrected (O->0, I/L->1).
func NormalizeRecoveryCode(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch r {
		case '-', ' ', '\t':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		case 'U':
			r = 'V'
		}
		if strings.ContainsRune(recoveryAlphabet, r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

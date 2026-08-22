// Package authtest provides a virtual WebAuthn authenticator.
//
// Passkeys are the one part of obscura that cannot be exercised by calling a
// function: the interesting half runs in a browser, on a device. Without
// something standing in for that device, the sign-in path would ship with only
// its edges tested -- and the edges are not where a signature check goes wrong.
//
// This is test support only and is never imported by the server.
package authtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// Authenticator flags, as laid out in the authenticator data.
const (
	flagUserPresent  = 0x01
	flagUserVerified = 0x04
	flagAttestedData = 0x40
)

// Authenticator is a stand-in for a security key or platform authenticator.
type Authenticator struct {
	t   *testing.T
	key *ecdsa.PrivateKey

	// CredentialID identifies this credential to the relying party.
	CredentialID []byte

	// prfSeed stands in for the per-credential secret a real authenticator
	// keeps in its secure element. The PRF extension is exactly an HMAC over
	// the salt with a secret only the device holds, which is why the output is
	// stable across sign-ins while a signature is not.
	prfSeed []byte

	// SignCount advances on every assertion. A counter that goes backwards is
	// how a cloned authenticator gives itself away.
	SignCount uint32

	// SupportsPRF can be turned off to play a device that authenticates but
	// cannot produce key material.
	SupportsPRF bool
}

// New creates an authenticator with a fresh credential.
func New(t *testing.T) *Authenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	seed := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	return &Authenticator{t: t, key: key, CredentialID: id, prfSeed: seed, SupportsPRF: true}
}

// PRF returns the extension output for a salt: stable for this credential,
// unpredictable without it.
func (a *Authenticator) PRF(salt []byte) []byte {
	a.t.Helper()
	if !a.SupportsPRF {
		a.t.Fatal("authenticator does not support PRF")
	}
	mac := hmac.New(sha256.New, a.prfSeed)
	mac.Write([]byte("WebAuthn PRF"))
	mac.Write(salt)
	return mac.Sum(nil)
}

// UnlockKey is what the browser sends the server: HKDF over the PRF output.
//
// The raw PRF result stays on the device; only this derived key leaves it. The
// info string must match the one the frontend uses, or a passkey enrolled by
// one and used by the other would silently fail to unwrap.
func (a *Authenticator) UnlockKey(salt []byte, info string) []byte {
	a.t.Helper()
	key, err := hkdf.Expand(sha256.New, a.PRF(salt), info, 32)
	if err != nil {
		a.t.Fatal(err)
	}
	return key
}

// Create answers a registration challenge with an attestation response, in the
// JSON shape the browser posts back.
func (a *Authenticator) Create(rpID, origin string, challenge []byte) []byte {
	a.t.Helper()
	clientData := a.clientData("webauthn.create", origin, challenge)

	authData := a.authenticatorData(rpID, flagUserPresent|flagUserVerified|flagAttestedData)
	authData = append(authData, make([]byte, 16)...) // AAGUID: all zeroes, "no attestation"
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(a.CredentialID)))
	authData = append(authData, a.CredentialID...)
	authData = append(authData, a.coseKey()...)

	object, err := cbor.Marshal(map[string]any{
		"fmt":      "none",
		"attStmt":  map[string]any{},
		"authData": authData,
	})
	if err != nil {
		a.t.Fatal(err)
	}

	return a.marshal(map[string]any{
		"response": map[string]any{
			"clientDataJSON":    b64(clientData),
			"attestationObject": b64(object),
			"transports":        []string{"internal"},
		},
	})
}

// Get answers a sign-in challenge with an assertion.
func (a *Authenticator) Get(rpID, origin string, challenge, userHandle []byte) []byte {
	a.t.Helper()
	clientData := a.clientData("webauthn.get", origin, challenge)

	a.SignCount++
	authData := a.authenticatorData(rpID, flagUserPresent|flagUserVerified)

	// The signature covers the authenticator data and a hash of the client
	// data, which is what binds the assertion to this origin and challenge.
	digest := sha256.Sum256(append(append([]byte{}, authData...), hash(clientData)...))
	signature, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		a.t.Fatal(err)
	}

	return a.marshal(map[string]any{
		"response": map[string]any{
			"clientDataJSON":    b64(clientData),
			"authenticatorData": b64(authData),
			"signature":         b64(signature),
			"userHandle":        b64(userHandle),
		},
	})
}

// --- internals --------------------------------------------------------------

func (a *Authenticator) marshal(extra map[string]any) []byte {
	a.t.Helper()
	out := map[string]any{
		"id":                      b64(a.CredentialID),
		"rawId":                   b64(a.CredentialID),
		"type":                    "public-key",
		"authenticatorAttachment": "platform",
		"clientExtensionResults":  map[string]any{},
	}
	for k, v := range extra {
		out[k] = v
	}
	raw, err := json.Marshal(out)
	if err != nil {
		a.t.Fatal(err)
	}
	return raw
}

func (a *Authenticator) clientData(typ, origin string, challenge []byte) []byte {
	a.t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type":        typ,
		"challenge":   b64(challenge),
		"origin":      origin,
		"crossOrigin": false,
	})
	if err != nil {
		a.t.Fatal(err)
	}
	return raw
}

func (a *Authenticator) authenticatorData(rpID string, flags byte) []byte {
	out := hash([]byte(rpID))
	out = append(out, flags)
	return binary.BigEndian.AppendUint32(out, a.SignCount)
}

// coseKey encodes the public key as COSE_Key for ES256.
func (a *Authenticator) coseKey() []byte {
	a.t.Helper()
	raw, err := cbor.Marshal(map[int]any{
		1:  2,            // kty: EC2
		3:  -7,           // alg: ES256
		-1: 1,            // crv: P-256
		-2: pad(a.key.X), // x
		-3: pad(a.key.Y), // y
	})
	if err != nil {
		a.t.Fatal(err)
	}
	return raw
}

// pad left-pads a coordinate to the 32 bytes P-256 requires; a coordinate that
// happens to start with a zero byte would otherwise encode short.
func pad(n *big.Int) []byte {
	b := n.Bytes()
	if len(b) >= 32 {
		return b
	}
	return append(make([]byte, 32-len(b)), b...)
}

func hash(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Challenge decodes the base64url challenge out of a ceremony's options.
func Challenge(t *testing.T, raw string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatal(fmt.Errorf("authtest: decode challenge: %w", err))
	}
	return b
}

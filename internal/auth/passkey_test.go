package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
)

func newRP(t *testing.T) (*RelyingParty, *Store, []byte) {
	t.Helper()
	s, _ := newStore(t)
	dek, _ := setup(t, s)
	p, err := NewRelyingParty(s, "", "obscura test")
	if err != nil {
		t.Fatal(err)
	}
	return p, s, dek
}

const testOrigin = "https://photos.example.com"

// Browsers refuse WebAuthn outside a secure context. Catching that here turns a
// misconfigured deployment into a startup error instead of a failure inside the
// browser, where the message is far less obvious.
func TestOriginMustBeSecure(t *testing.T) {
	for _, tc := range []struct {
		origin string
		rpID   string
		ok     bool
	}{
		{"https://photos.example.com", "photos.example.com", true},
		{"https://photos.example.com:8443", "photos.example.com", true},
		{"http://localhost:8080", "localhost", true},
		{"http://127.0.0.1:8080", "127.0.0.1", true},
		{"http://photos.example.com", "", false},
		{"photos.example.com", "", false},
		{"", "", false},
	} {
		id, err := rpIDFor(tc.origin)
		if tc.ok && err != nil {
			t.Errorf("%q: %v", tc.origin, err)
			continue
		}
		if !tc.ok && err == nil {
			t.Errorf("%q was accepted", tc.origin)
			continue
		}
		if tc.ok && id != tc.rpID {
			t.Errorf("%q: RP ID = %q, want %q", tc.origin, id, tc.rpID)
		}
	}
	if _, err := NewRelyingParty(nil, "http://photos.example.com", ""); err == nil {
		t.Error("a plain-http origin was accepted at construction")
	}
}

func TestRegistrationChallenge(t *testing.T) {
	ctx := context.Background()
	p, _, _ := newRP(t)

	ch, err := p.BeginRegistration(ctx, testOrigin, 1, "ralf")
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.PRFSalt) != prfSaltSize {
		t.Fatalf("PRF salt is %d bytes, want %d", len(ch.PRFSalt), prfSaltSize)
	}
	if len(ch.Options.Response.Challenge) < 16 {
		t.Fatalf("challenge is only %d bytes", len(ch.Options.Response.Challenge))
	}
	if ch.Options.Response.RelyingParty.ID != "photos.example.com" {
		t.Fatalf("RP ID = %q", ch.Options.Response.RelyingParty.ID)
	}
	// User verification is required, not preferred: this passkey replaces the
	// password outright, so the device must confirm a human is there.
	if ch.Options.Response.AuthenticatorSelection.UserVerification != "required" {
		t.Fatalf("user verification = %q, want required",
			ch.Options.Response.AuthenticatorSelection.UserVerification)
	}
	// The handle is created on demand and must be in the options.
	handle, ok := ch.Options.Response.User.ID.(protocol.URLEncodedBase64)
	if !ok {
		t.Fatalf("user handle has type %T", ch.Options.Response.User.ID)
	}
	if len(handle) != handleSize {
		t.Fatalf("user handle is %d bytes", len(handle))
	}
}

// Two ceremonies started in a row must not share a salt, or one authenticator's
// PRF output would unwrap another's slot.
func TestEachRegistrationGetsItsOwnSalt(t *testing.T) {
	ctx := context.Background()
	p, _, _ := newRP(t)

	first, err := p.BeginRegistration(ctx, testOrigin, 1, "ralf")
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.BeginRegistration(ctx, testOrigin, 1, "ralf")
	if err != nil {
		t.Fatal(err)
	}
	if string(first.PRFSalt) == string(second.PRFSalt) {
		t.Fatal("two ceremonies share a PRF salt")
	}
	if first.ID == second.ID {
		t.Fatal("two ceremonies share an ID")
	}
}

// Enrolling the same authenticator twice would silently create a second slot
// for one device instead of an error the user can act on.
func TestRegistrationExcludesEnrolledAuthenticators(t *testing.T) {
	ctx := context.Background()
	p, s, dek := newRP(t)

	cred := testCredential(t, "cred-a")
	if err := s.AddPasskey(ctx, 1, dek, randomKey(t), cred, []byte("salt"), "Phone"); err != nil {
		t.Fatal(err)
	}
	ch, err := p.BeginRegistration(ctx, testOrigin, 1, "ralf")
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Options.Response.CredentialExcludeList) != 1 {
		t.Fatalf("exclude list has %d entries, want 1",
			len(ch.Options.Response.CredentialExcludeList))
	}
	if string(ch.Options.Response.CredentialExcludeList[0].CredentialID) != "cred-a" {
		t.Fatal("the enrolled credential is not excluded")
	}
}

func TestLoginChallengeCarriesASaltPerCredential(t *testing.T) {
	ctx := context.Background()
	p, s, dek := newRP(t)

	if _, err := p.BeginLogin(ctx, testOrigin, ""); !errors.Is(err, ErrNoPasskeys) {
		t.Fatalf("want ErrNoPasskeys, got %v", err)
	}

	for _, tc := range []struct{ id, salt string }{{"cred-a", "salt-a"}, {"cred-b", "salt-b"}} {
		if err := s.AddPasskey(ctx, 1, dek, randomKey(t), testCredential(t, tc.id), []byte(tc.salt), tc.id); err != nil {
			t.Fatal(err)
		}
	}

	// No username: a passkey already identifies its owner.
	ch, err := p.BeginLogin(ctx, testOrigin, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Options.Response.AllowedCredentials) != 2 {
		t.Fatalf("%d allowed credentials, want 2",
			len(ch.Options.Response.AllowedCredentials))
	}
	// The browser cannot know which authenticator the user will reach for, so
	// every candidate has to arrive with its own salt.
	want := map[string]string{
		base64.RawURLEncoding.EncodeToString([]byte("cred-a")): "salt-a",
		base64.RawURLEncoding.EncodeToString([]byte("cred-b")): "salt-b",
	}
	if len(ch.PRFSalts) != len(want) {
		t.Fatalf("%d salts, want %d", len(ch.PRFSalts), len(want))
	}
	for id, salt := range want {
		if string(ch.PRFSalts[id]) != salt {
			t.Fatalf("salt for %s = %q, want %q", id, ch.PRFSalts[id], salt)
		}
	}
}

// A challenge that can be replayed is not a challenge.
func TestCeremoniesAreSingleUse(t *testing.T) {
	ctx := context.Background()
	p, _, _ := newRP(t)

	ch, err := p.BeginRegistration(ctx, testOrigin, 1, "ralf")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.consume(ch.ID); !ok {
		t.Fatal("the ceremony could not be consumed once")
	}
	if _, ok := p.consume(ch.ID); ok {
		t.Fatal("the ceremony could be consumed twice")
	}

	// And finishing with a stale ID must not reach the parser at all.
	err = p.FinishRegistration(ctx, testOrigin, ch.ID, 1, randomKey(t), randomKey(t), "x",
		strings.NewReader("{}"))
	if !errors.Is(err, ErrCeremonyExpired) {
		t.Fatalf("want ErrCeremonyExpired, got %v", err)
	}
}

func TestCeremoniesExpire(t *testing.T) {
	ctx := context.Background()
	p, _, _ := newRP(t)

	ch, err := p.BeginRegistration(ctx, testOrigin, 1, "ralf")
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.ceremonies[ch.ID].expires = time.Now().Add(-time.Second)
	p.mu.Unlock()

	if _, ok := p.consume(ch.ID); ok {
		t.Fatal("an expired ceremony was accepted")
	}
}

// A registration ceremony must not be usable to sign in, nor a login ceremony
// to enroll a slot.
func TestCeremonyKindsDoNotCross(t *testing.T) {
	ctx := context.Background()
	p, s, dek := newRP(t)

	reg, err := p.BeginRegistration(ctx, testOrigin, 1, "ralf")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := p.FinishLogin(ctx, testOrigin, reg.ID, randomKey(t), strings.NewReader("{}")); !errors.Is(err, ErrCeremonyExpired) {
		t.Fatalf("a registration ceremony was accepted for login: %v", err)
	}

	if err := s.AddPasskey(ctx, 1, dek, randomKey(t), testCredential(t, "cred-a"), []byte("salt"), "Phone"); err != nil {
		t.Fatal(err)
	}
	login, err := p.BeginLogin(ctx, testOrigin, "")
	if err != nil {
		t.Fatal(err)
	}
	err = p.FinishRegistration(ctx, testOrigin, login.ID, 1, dek, randomKey(t), "x",
		strings.NewReader("{}"))
	if !errors.Is(err, ErrCeremonyExpired) {
		t.Fatalf("a login ceremony was accepted for registration: %v", err)
	}
}

// With an origin configured, whatever the request claims is ignored.
func TestAConfiguredOriginWins(t *testing.T) {
	s, _ := newStore(t)
	p, err := NewRelyingParty(s, "https://photos.example.com/", "obscura")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Pinned() {
		t.Fatal("Pinned() is false with an origin configured")
	}
	w, err := p.instanceFor("https://evil.example.net")
	if err != nil {
		t.Fatal(err)
	}
	if w.Config.RPID != "photos.example.com" {
		t.Fatalf("RP ID = %q, want the configured host", w.Config.RPID)
	}
	if len(w.Config.RPOrigins) != 1 || w.Config.RPOrigins[0] != "https://photos.example.com" {
		t.Fatalf("origins = %v", w.Config.RPOrigins)
	}
}

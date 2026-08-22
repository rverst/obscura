package auth

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/rverst/obscura/internal/authtest"
)

// enroll runs a full registration ceremony against a virtual authenticator and
// returns the salt the slot was created with.
func enroll(t *testing.T, p *RelyingParty, dek []byte, a *authtest.Authenticator, label string) []byte {
	t.Helper()
	ctx := context.Background()

	ch, err := p.BeginRegistration(ctx, testOrigin, 1, "ralf")
	if err != nil {
		t.Fatal(err)
	}
	response := a.Create("photos.example.com", testOrigin, ch.Options.Response.Challenge)
	unlockKey := a.UnlockKey(ch.PRFSalt, PRFInfo)

	if err := p.FinishRegistration(ctx, testOrigin, ch.ID, 1, dek, unlockKey, label,
		bytes.NewReader(response)); err != nil {
		t.Fatal(err)
	}
	return ch.PRFSalt
}

// The whole point of M7 in one test: enroll a passkey, then sign in with it and
// arrive at the same DEK the password produces.
func TestPasskeyRoundTrip(t *testing.T) {
	ctx := context.Background()
	p, s, dek := newRP(t)
	a := authtest.New(t)

	enroll(t, p, dek, a, "Phone")

	ch, err := p.BeginLogin(ctx, testOrigin, "")
	if err != nil {
		t.Fatal(err)
	}
	salt := ch.PRFSalts[b64url(a.CredentialID)]
	if len(salt) == 0 {
		t.Fatal("the login challenge carries no salt for the enrolled credential")
	}

	handle, err := s.user(ctx, "ralf")
	if err != nil {
		t.Fatal(err)
	}
	response := a.Get("photos.example.com", testOrigin, ch.Options.Response.Challenge, handle.handle)

	userID, username, got, err := p.FinishLogin(ctx, testOrigin, ch.ID,
		a.UnlockKey(salt, PRFInfo), bytes.NewReader(response))
	if err != nil {
		t.Fatal(err)
	}
	if userID != 1 || username != "ralf" {
		t.Fatalf("signed in as %d/%q", userID, username)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("the passkey yielded a different key than the password")
	}
}

// The salt is what makes the PRF output stable. A different salt must not open
// the slot, or the salt would be decoration.
func TestTheWrongSaltDoesNotUnlock(t *testing.T) {
	ctx := context.Background()
	p, s, dek := newRP(t)
	a := authtest.New(t)
	enroll(t, p, dek, a, "Phone")

	ch, err := p.BeginLogin(ctx, testOrigin, "")
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := s.user(ctx, "ralf")
	response := a.Get("photos.example.com", testOrigin, ch.Options.Response.Challenge, handle.handle)

	_, _, _, err = p.FinishLogin(ctx, testOrigin, ch.ID,
		a.UnlockKey([]byte("some other salt"), PRFInfo), bytes.NewReader(response))
	if err == nil {
		t.Fatal("a key derived from the wrong salt unlocked the slot")
	}
}

// A valid assertion from an authenticator that never enrolled must not sign in,
// however well-formed it is.
func TestAnUnknownAuthenticatorCannotSignIn(t *testing.T) {
	ctx := context.Background()
	p, s, dek := newRP(t)
	enrolled := authtest.New(t)
	enroll(t, p, dek, enrolled, "Phone")

	ch, err := p.BeginLogin(ctx, testOrigin, "")
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := s.user(ctx, "ralf")

	stranger := authtest.New(t)
	response := stranger.Get("photos.example.com", testOrigin, ch.Options.Response.Challenge, handle.handle)
	if _, _, _, err := p.FinishLogin(ctx, testOrigin, ch.ID,
		stranger.UnlockKey([]byte("salt"), PRFInfo), bytes.NewReader(response)); err == nil {
		t.Fatal("an unenrolled authenticator signed in")
	}
}

// An assertion produced for a different site must not be accepted here. This is
// the check that a request-derived origin weakens, so it is worth pinning down.
func TestAnAssertionForAnotherOriginIsRejected(t *testing.T) {
	ctx := context.Background()
	p, s, dek := newRP(t)
	a := authtest.New(t)
	enroll(t, p, dek, a, "Phone")

	ch, err := p.BeginLogin(ctx, testOrigin, "")
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := s.user(ctx, "ralf")
	salt := ch.PRFSalts[b64url(a.CredentialID)]

	// Same challenge, same credential, wrong origin in the client data.
	response := a.Get("photos.example.com", "https://evil.example.net",
		ch.Options.Response.Challenge, handle.handle)
	if _, _, _, err := p.FinishLogin(ctx, testOrigin, ch.ID,
		a.UnlockKey(salt, PRFInfo), bytes.NewReader(response)); err == nil {
		t.Fatal("an assertion made for another origin was accepted")
	}
}

// A replayed assertion has a stale signature counter, which is how a cloned
// authenticator gives itself away.
func TestSignCountAdvancesAcrossLogins(t *testing.T) {
	ctx := context.Background()
	p, s, dek := newRP(t)
	a := authtest.New(t)
	enroll(t, p, dek, a, "Phone")

	handle, _ := s.user(ctx, "ralf")
	for i := 0; i < 3; i++ {
		ch, err := p.BeginLogin(ctx, testOrigin, "")
		if err != nil {
			t.Fatal(err)
		}
		salt := ch.PRFSalts[b64url(a.CredentialID)]
		response := a.Get("photos.example.com", testOrigin, ch.Options.Response.Challenge, handle.handle)
		if _, _, _, err := p.FinishLogin(ctx, testOrigin, ch.ID,
			a.UnlockKey(salt, PRFInfo), bytes.NewReader(response)); err != nil {
			t.Fatalf("sign-in %d: %v", i+1, err)
		}
	}

	creds, err := s.credentials(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 {
		t.Fatalf("%d credentials, want 1", len(creds))
	}
	if creds[0].Authenticator.SignCount != a.SignCount {
		t.Fatalf("stored sign count = %d, authenticator is at %d",
			creds[0].Authenticator.SignCount, a.SignCount)
	}
}

// Two devices, two slots, one DEK -- and removing one leaves the other alone.
func TestTwoPasskeysUnlockIndependently(t *testing.T) {
	ctx := context.Background()
	p, s, dek := newRP(t)

	phone := authtest.New(t)
	saltPhone := enroll(t, p, dek, phone, "Phone")
	laptop := authtest.New(t)
	enroll(t, p, dek, laptop, "Laptop")

	slots, err := s.ListSlots(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var passkeys []Slot
	for _, sl := range slots {
		if sl.Kind == KindPasskey {
			passkeys = append(passkeys, sl)
		}
	}
	if len(passkeys) != 2 {
		t.Fatalf("%d passkey slots, want 2", len(passkeys))
	}

	// Drop the laptop.
	var laptopSlot int64
	for _, sl := range passkeys {
		if sl.Label == "Laptop" {
			laptopSlot = sl.ID
		}
	}
	if err := s.RemoveSlot(ctx, 1, laptopSlot); err != nil {
		t.Fatal(err)
	}

	ch, err := p.BeginLogin(ctx, testOrigin, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Options.Response.AllowedCredentials) != 1 {
		t.Fatalf("%d credentials still offered, want 1",
			len(ch.Options.Response.AllowedCredentials))
	}
	handle, _ := s.user(ctx, "ralf")
	response := phone.Get("photos.example.com", testOrigin, ch.Options.Response.Challenge, handle.handle)
	if _, _, got, err := p.FinishLogin(ctx, testOrigin, ch.ID,
		phone.UnlockKey(saltPhone, PRFInfo), bytes.NewReader(response)); err != nil {
		t.Fatal(err)
	} else if !bytes.Equal(got, dek) {
		t.Fatal("the surviving passkey no longer yields the DEK")
	}
}

// A device that cannot produce a PRF result must leave nothing behind. A
// credential that signs in but cannot decrypt is worse than no passkey at all.
func TestAbandonedEnrollmentStoresNothing(t *testing.T) {
	ctx := context.Background()
	p, s, dek := newRP(t)

	ch, err := p.BeginRegistration(ctx, testOrigin, 1, "ralf")
	if err != nil {
		t.Fatal(err)
	}
	// The client discovers there is no PRF output and gives up here.
	p.Discard(ch.ID)

	a := authtest.New(t)
	response := a.Create("photos.example.com", testOrigin, ch.Options.Response.Challenge)
	if err := p.FinishRegistration(ctx, testOrigin, ch.ID, 1, dek,
		a.UnlockKey([]byte("salt"), PRFInfo), "Phone", bytes.NewReader(response)); err == nil {
		t.Fatal("a discarded ceremony was still completed")
	}

	creds, err := s.credentials(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 0 {
		t.Fatalf("%d credentials stored after an abandoned enrollment", len(creds))
	}
	if _, err := p.BeginLogin(ctx, testOrigin, ""); err == nil {
		t.Fatal("the login screen would offer a passkey that was never enrolled")
	}
}

// Garbage in the credential field must not reach further than the parser.
func TestMalformedResponsesAreRejected(t *testing.T) {
	ctx := context.Background()
	p, _, dek := newRP(t)

	for _, body := range []string{"", "{}", `{"id":"","type":"public-key"}`, "not json"} {
		ch, err := p.BeginRegistration(ctx, testOrigin, 1, "ralf")
		if err != nil {
			t.Fatal(err)
		}
		if err := p.FinishRegistration(ctx, testOrigin, ch.ID, 1, dek, randomKey(t), "x",
			strings.NewReader(body)); err == nil {
			t.Errorf("%q was accepted as an attestation", body)
		}
	}
}

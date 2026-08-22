package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/rverst/obscura/internal/crypto"
)

// PRFInfo is the HKDF info string the browser uses to turn the PRF output into
// the unlock key. It is not a secret; it exists so the same PRF result cannot
// be reused as a key somewhere else, and it must match the frontend exactly --
// a mismatch produces a passkey that enrolls and then never unlocks.
const PRFInfo = "obscura-unlock-v1"

// prfSaltSize is the length of the salt handed to the PRF extension. The
// authenticator derives a stable secret from (credential, salt), so the salt
// need only be unique, not secret.
const prfSaltSize = 32

// ceremonyTTL bounds how long a started ceremony may be finished. Long enough
// to walk to another room for a security key, short enough that an abandoned
// challenge does not sit in memory all day.
const ceremonyTTL = 5 * time.Minute

// ErrNoPasskeys reports that the account has no passkey registered.
var ErrNoPasskeys = errors.New("auth: no passkey registered")

// ErrCeremonyExpired reports an unknown, expired or already used ceremony.
var ErrCeremonyExpired = errors.New("auth: challenge expired")

// RelyingParty runs the WebAuthn ceremonies behind the passkey slots.
//
// A passkey is not a second factor here -- it is a third way to unwrap the DEK,
// alongside the password and the recovery code. What makes that possible is the
// PRF extension: an ordinary passkey signature differs on every use and is
// useless as key material, while PRF returns the same 32 bytes for the same
// salt every time.
type RelyingParty struct {
	store       *Store
	displayName string

	// origin pins the relying party to one URL. Empty means it is taken from
	// the request; see instanceFor.
	origin string

	mu         sync.Mutex
	instances  map[string]*webauthn.WebAuthn
	ceremonies map[string]*ceremony
}

// NewRelyingParty creates the ceremony runner. origin may be empty.
func NewRelyingParty(store *Store, origin, displayName string) (*RelyingParty, error) {
	if origin != "" {
		if _, err := rpIDFor(origin); err != nil {
			return nil, err
		}
	}
	if displayName == "" {
		displayName = "obscura"
	}
	return &RelyingParty{
		store:       store,
		displayName: displayName,
		origin:      strings.TrimSuffix(origin, "/"),
		instances:   make(map[string]*webauthn.WebAuthn),
		ceremonies:  make(map[string]*ceremony),
	}, nil
}

// Pinned reports whether the relying party is fixed to a configured origin.
func (p *RelyingParty) Pinned() bool { return p.origin != "" }

type ceremony struct {
	login   bool
	userID  int64
	session webauthn.SessionData
	prfSalt []byte
	expires time.Time
}

// --- Registration -----------------------------------------------------------

// RegistrationChallenge is what the browser needs to create a credential.
type RegistrationChallenge struct {
	ID      string
	Options *protocol.CredentialCreation
	// PRFSalt is chosen here rather than by the browser. It is pinned for the
	// whole ceremony so the key the client derives is the key this slot will be
	// wrapped with -- a client that picked its own salt could register a slot
	// it is then unable to reproduce.
	PRFSalt []byte
}

// BeginRegistration starts enrolling a passkey for a signed-in user.
func (p *RelyingParty) BeginRegistration(ctx context.Context, origin string, userID int64, username string) (*RegistrationChallenge, error) {
	w, err := p.instanceFor(origin)
	if err != nil {
		return nil, err
	}
	u, err := p.store.user(ctx, username)
	if err != nil {
		return nil, err
	}
	if u.id != userID {
		return nil, ErrUnlockFailed
	}
	if err := p.store.ensureHandle(ctx, u); err != nil {
		return nil, err
	}

	// Offering an authenticator that is already enrolled would produce a second
	// slot for the same device rather than an error the user can act on.
	exclude := make([]protocol.CredentialDescriptor, 0, len(u.creds))
	for _, c := range u.creds {
		exclude = append(exclude, c.Descriptor())
	}

	options, session, err := w.BeginRegistration(u, webauthn.WithExclusions(exclude))
	if err != nil {
		return nil, fmt.Errorf("auth: begin passkey registration: %w", err)
	}

	salt := make([]byte, prfSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("auth: generate PRF salt: %w", err)
	}
	id, err := p.remember(&ceremony{userID: u.id, session: *session, prfSalt: salt})
	if err != nil {
		return nil, err
	}
	return &RegistrationChallenge{ID: id, Options: options, PRFSalt: salt}, nil
}

// FinishRegistration verifies the attestation and creates the key slot.
//
// unlockKey is what the browser derived from the PRF output via HKDF; the raw
// PRF secret never leaves the authenticator's host. Handing the server a key
// rather than deriving one from a signature is the whole trick -- and it is
// only acceptable because this design already lets the server see plaintext
// while a session is open.
func (p *RelyingParty) FinishRegistration(ctx context.Context, origin, ceremonyID string, userID int64, dek, unlockKey []byte, label string, response io.Reader) error {
	w, err := p.instanceFor(origin)
	if err != nil {
		return err
	}
	c, ok := p.consume(ceremonyID)
	if !ok || c.login || c.userID != userID {
		return ErrCeremonyExpired
	}

	parsed, err := protocol.ParseCredentialCreationResponseBody(response)
	if err != nil {
		return fmt.Errorf("auth: parse attestation: %w", err)
	}
	u, err := p.store.user(ctx, "")
	if err != nil {
		return err
	}
	if u.id != userID {
		return ErrUnlockFailed
	}
	cred, err := w.CreateCredential(u, c.session, parsed)
	if err != nil {
		return fmt.Errorf("auth: verify attestation: %w", err)
	}

	if label == "" {
		label = "Passkey"
	}
	return p.store.AddPasskey(ctx, userID, dek, unlockKey, cred, c.prfSalt, label)
}

// --- Login ------------------------------------------------------------------

// LoginChallenge is what the browser needs to produce an assertion.
type LoginChallenge struct {
	ID      string
	Options *protocol.CredentialAssertion
	// PRFSalts maps base64url credential IDs to their salt, which is exactly
	// the shape of the evalByCredential input. The browser cannot know in
	// advance which authenticator the user will reach for, so every candidate
	// gets its salt up front.
	PRFSalts map[string][]byte
}

// BeginLogin starts a passkey sign-in. An empty username resolves to the only
// account, which is what allows a bare "Use a passkey" button.
func (p *RelyingParty) BeginLogin(ctx context.Context, origin, username string) (*LoginChallenge, error) {
	w, err := p.instanceFor(origin)
	if err != nil {
		return nil, err
	}
	u, err := p.store.user(ctx, username)
	if err != nil {
		return nil, err
	}
	if len(u.creds) == 0 {
		return nil, ErrNoPasskeys
	}

	options, session, err := w.BeginLogin(u)
	if err != nil {
		return nil, fmt.Errorf("auth: begin passkey login: %w", err)
	}
	salts, err := p.store.PRFSalts(ctx, u.id)
	if err != nil {
		return nil, err
	}
	byCredential := make(map[string][]byte, len(salts))
	for id, salt := range salts {
		byCredential[base64.RawURLEncoding.EncodeToString([]byte(id))] = salt
	}

	id, err := p.remember(&ceremony{login: true, userID: u.id, session: *session})
	if err != nil {
		return nil, err
	}
	return &LoginChallenge{ID: id, Options: options, PRFSalts: byCredential}, nil
}

// FinishLogin verifies the assertion and unwraps the DEK.
//
// Order matters. The assertion is checked first, so a caller who somehow
// guessed an unlock key still has to hold the authenticator; and the credential
// ID used to pick the slot is taken from the verified assertion, never from the
// request body next to it.
func (p *RelyingParty) FinishLogin(ctx context.Context, origin, ceremonyID string, unlockKey []byte, response io.Reader) (userID int64, username string, dek []byte, err error) {
	w, err := p.instanceFor(origin)
	if err != nil {
		return 0, "", nil, err
	}
	c, ok := p.consume(ceremonyID)
	if !ok || !c.login {
		return 0, "", nil, ErrCeremonyExpired
	}

	parsed, err := protocol.ParseCredentialRequestResponseBody(response)
	if err != nil {
		return 0, "", nil, ErrUnlockFailed
	}
	u, err := p.store.userByHandle(ctx, c.session.UserID)
	if err != nil {
		return 0, "", nil, err
	}
	cred, err := w.ValidateLogin(u, c.session, parsed)
	if err != nil {
		return 0, "", nil, ErrUnlockFailed
	}
	// Write the record back: this is what advances the signature counter that
	// exposes a cloned authenticator.
	if err := p.store.updateCredential(ctx, u.id, cred); err != nil {
		return 0, "", nil, err
	}

	if len(unlockKey) != crypto.DEKSize {
		return 0, "", nil, ErrUnlockFailed
	}
	id, dek, err := p.store.UnlockWithKey(ctx, cred.ID, unlockKey)
	if err != nil {
		return 0, "", nil, err
	}
	return id, u.name, dek, nil
}

// --- Ceremony state ---------------------------------------------------------

func (p *RelyingParty) remember(c *ceremony) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: generate ceremony ID: %w", err)
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	c.expires = time.Now().Add(ceremonyTTL)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepLocked()
	p.ceremonies[id] = c
	return id, nil
}

// consume returns a ceremony and removes it. Single use: a challenge that could
// be replayed is not a challenge.
func (p *RelyingParty) consume(id string) (*ceremony, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.ceremonies[id]
	if !ok {
		return nil, false
	}
	delete(p.ceremonies, id)
	if time.Now().After(c.expires) {
		return nil, false
	}
	return c, true
}

func (p *RelyingParty) sweepLocked() {
	now := time.Now()
	for id, c := range p.ceremonies {
		if now.After(c.expires) {
			delete(p.ceremonies, id)
		}
	}
}

// --- Relying party identity -------------------------------------------------

// instanceFor returns the WebAuthn configuration for an origin.
//
// When no origin was configured, it comes from the request. That keeps a
// self-hosted install working under whatever hostname its owner points at it,
// but it does weaken the check: verifying the origin against a value taken from
// the same request proves nothing on its own. What still holds is the browser's
// own rule that the RP ID must be a suffix of the real origin, so a forged Host
// header breaks the ceremony rather than redirecting it. Pinning the origin in
// the configuration restores the full check and is the right choice in
// production.
func (p *RelyingParty) instanceFor(origin string) (*webauthn.WebAuthn, error) {
	if p.origin != "" {
		origin = p.origin
	}
	rpID, err := rpIDFor(origin)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if w, ok := p.instances[origin]; ok {
		return w, nil
	}
	w, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: p.displayName,
		RPOrigins:     []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			// Discoverable where the authenticator can manage it, so the user
			// is not asked for a username first.
			ResidentKey: protocol.ResidentKeyRequirementPreferred,
			// Required, not preferred: this passkey replaces the password
			// outright, so the device must confirm a human is present.
			UserVerification: protocol.VerificationRequired,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("auth: configure relying party: %w", err)
	}
	p.instances[origin] = w
	return w, nil
}

// rpIDFor reduces an origin to the RP ID, which is the bare host.
//
// Both rules here exist because a browser would otherwise reject the ceremony
// itself, with a message that points at nothing the user can act on.
func rpIDFor(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("auth: %q is not a usable origin (want scheme://host[:port])", origin)
	}
	host := u.Hostname()

	// An RP ID has to be a domain. Reaching a self-hosted server by its address
	// is the obvious thing to try, and http://127.0.0.1:8080 is a secure
	// context, so this rule is easy to trip over -- http://localhost:8080 is
	// the same server and does work.
	if net.ParseIP(host) != nil {
		return "", fmt.Errorf(
			"auth: passkeys need a hostname, not an address like %q (try localhost)", host)
	}
	if u.Scheme != "https" && host != "localhost" {
		return "", fmt.Errorf("auth: passkeys need https (or localhost), not %q", origin)
	}
	return host, nil
}

// Discard drops an abandoned ceremony rather than leaving it to expire -- what
// the client calls when the authenticator turns out not to support PRF and the
// credential is thrown away.
func (p *RelyingParty) Discard(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.ceremonies, id)
}

// OriginUsable reports whether passkeys can work for an origin, returning the
// RP ID that would be used. A configured origin overrides the argument.
func (p *RelyingParty) OriginUsable(origin string) (string, error) {
	if p.origin != "" {
		origin = p.origin
	}
	return rpIDFor(origin)
}

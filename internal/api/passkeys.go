package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/rverst/obscura/internal/auth"
)

// Passkey endpoints.
//
// Enrolling needs an open session, because the DEK has to be wrapped with the
// key the browser derives -- there is nothing to enroll while the library is
// locked. Signing in obviously does not.
//
// The unlock key travels to the server in the clear (inside TLS). That is
// consistent with the rest of this design, where the server handles plaintext
// for as long as a session is open; it is not an end-to-end scheme and does not
// pretend to be. The raw PRF output still never leaves the browser -- only the
// HKDF output does -- so the authenticator's secret is not disclosed.

func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if !s.passkeysConfigured(w, r) {
		return
	}
	sess := sessionFrom(r)
	ch, err := s.RP.BeginRegistration(r.Context(), s.origin(r), sess.UserID, sess.Username)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot start enrollment", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ceremony": ch.ID,
		"options":  ch.Options,
		// The browser needs the salt to build the prf extension input. It is
		// not secret -- the secret is what the authenticator makes of it.
		"prf_salt": b64(ch.PRFSalt),
	})
}

func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if !s.passkeysConfigured(w, r) {
		return
	}
	var req struct {
		Ceremony   string          `json:"ceremony"`
		Label      string          `json:"label"`
		UnlockKey  string          `json:"unlock_key"`
		Credential json.RawMessage `json:"credential"`
	}
	// Attestation objects carrying a certificate chain are comfortably larger
	// than the small JSON the other endpoints accept.
	if !readJSONLimit(w, r, &req, 256<<10) {
		return
	}
	key, ok := decodeKey(req.UnlockKey)
	if !ok {
		s.fail(w, r, http.StatusBadRequest, "malformed unlock key", nil)
		return
	}

	sess := sessionFrom(r)
	err := s.RP.FinishRegistration(r.Context(), s.origin(r), req.Ceremony,
		sess.UserID, sess.DEK, key, strings.TrimSpace(req.Label), bytes.NewReader(req.Credential))
	if errors.Is(err, auth.ErrCeremonyExpired) {
		s.fail(w, r, http.StatusBadRequest, "the challenge expired, please try again", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "the passkey could not be registered", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePasskeyRegisterAbort drops a ceremony the browser gave up on.
//
// The client calls this when the authenticator turns out not to produce a PRF
// result: the credential is discarded rather than stored, so there is never a
// passkey on record that can sign in but not decrypt.
func (s *Server) handlePasskeyRegisterAbort(w http.ResponseWriter, r *http.Request) {
	if !s.passkeysConfigured(w, r) {
		return
	}
	var req struct {
		Ceremony string `json:"ceremony"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.RP.Discard(req.Ceremony)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if !s.passkeysConfigured(w, r) {
		return
	}
	var req struct {
		// Optional: a passkey already identifies its owner, so the login screen
		// can offer a bare button as long as there is only one account.
		Username string `json:"username"`
	}
	if r.ContentLength > 0 && !readJSON(w, r, &req) {
		return
	}

	ch, err := s.RP.BeginLogin(r.Context(), s.origin(r), req.Username)
	if errors.Is(err, auth.ErrNoPasskeys) || errors.Is(err, auth.ErrUnlockFailed) {
		s.fail(w, r, http.StatusNotFound, "no passkey registered", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot start sign-in", err)
		return
	}

	salts := make(map[string]string, len(ch.PRFSalts))
	for id, salt := range ch.PRFSalts {
		salts[id] = b64(salt)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ceremony": ch.ID,
		"options":  ch.Options,
		// Keyed by credential id, which is the shape the evalByCredential
		// extension input wants.
		"prf_salts": salts,
	})
}

func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if !s.passkeysConfigured(w, r) {
		return
	}
	var req struct {
		Ceremony   string          `json:"ceremony"`
		UnlockKey  string          `json:"unlock_key"`
		Credential json.RawMessage `json:"credential"`
	}
	if !readJSONLimit(w, r, &req, 256<<10) {
		return
	}
	key, ok := decodeKey(req.UnlockKey)
	if !ok {
		// Same answer as a failed assertion: nothing here should say which of
		// the two halves went wrong.
		s.fail(w, r, http.StatusUnauthorized, "login failed", nil)
		return
	}

	userID, username, dek, err := s.RP.FinishLogin(r.Context(), s.origin(r),
		req.Ceremony, key, bytes.NewReader(req.Credential))
	if errors.Is(err, auth.ErrCeremonyExpired) {
		s.fail(w, r, http.StatusBadRequest, "the challenge expired, please try again", err)
		return
	}
	s.finishLogin(w, r, userID, username, dek, err)
}

// handleRemovePasskey deletes one passkey slot and its credential.
func (s *Server) handleRemovePasskey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid id", err)
		return
	}
	sess := sessionFrom(r)
	// RemoveSlot refuses anything that is not a passkey, so the password and
	// recovery slots cannot be deleted through this route.
	if err := s.Slots.RemoveSlot(r.Context(), sess.UserID, id); err != nil {
		s.fail(w, r, http.StatusBadRequest, "cannot remove this unlock method", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ----------------------------------------------------------------

// passkeysConfigured reports whether the relying party could be built.
//
// It cannot be when the server is reached over plain http on something other
// than localhost: browsers refuse WebAuthn outside a secure context, so the
// honest answer is that the feature is unavailable here rather than a ceremony
// that fails halfway.
func (s *Server) passkeysConfigured(w http.ResponseWriter, r *http.Request) bool {
	if s.RP == nil {
		s.fail(w, r, http.StatusNotImplemented, "passkeys are not enabled", nil)
		return false
	}
	if _, err := s.RP.OriginUsable(s.origin(r)); err != nil {
		s.fail(w, r, http.StatusBadRequest,
			"passkeys need an https address (or localhost)", err)
		return false
	}
	return true
}

// origin reconstructs the URL the browser used.
//
// Behind a reverse proxy the scheme is only visible in X-Forwarded-Proto, so it
// is honoured here. That header is trivially forged by anyone talking to the
// server directly -- which is why pinning the origin in the configuration is
// the right move in production, and why the relying party says so too.
func (s *Server) origin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		if i := strings.IndexByte(p, ','); i >= 0 {
			p = p[:i]
		}
		scheme = strings.TrimSpace(p)
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		if i := strings.IndexByte(h, ','); i >= 0 {
			h = h[:i]
		}
		host = strings.TrimSpace(h)
	}
	return scheme + "://" + host
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// decodeKey accepts base64url with or without padding; browsers disagree about
// emitting it and the difference carries no meaning.
func decodeKey(s string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil || len(raw) != 32 {
		return nil, false
	}
	return raw, true
}

package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rverst/obscura/internal/auth"
	"github.com/rverst/obscura/internal/authtest"
)

// The harness pins the relying party here, so the browser side of every
// ceremony has to speak as if it were served from this address.
const (
	testOrigin = "https://photos.example.com"
	testRPID   = "photos.example.com"
)

func challengeOf(t *testing.T, body map[string]any) []byte {
	t.Helper()
	options, ok := body["options"].(map[string]any)
	if !ok {
		t.Fatalf("no options in %v", body)
	}
	pk, ok := options["publicKey"].(map[string]any)
	if !ok {
		t.Fatalf("no publicKey in %v", options)
	}
	return authtest.Challenge(t, pk["challenge"].(string))
}

func decodeSalt(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// enrollPasskey runs a registration over HTTP, exactly as the browser does.
func (h *harness) enrollPasskey(t *testing.T, a *authtest.Authenticator, label string) []byte {
	t.Helper()
	code, body := h.do("POST", "/api/passkeys/register/begin", nil)
	if code != 200 {
		t.Fatalf("register/begin: %d %v", code, body)
	}
	salt := decodeSalt(t, body["prf_salt"].(string))
	response := a.Create(testRPID, testOrigin, challengeOf(t, body))

	code, body = h.do("POST", "/api/passkeys/register/finish", map[string]any{
		"ceremony":   body["ceremony"],
		"label":      label,
		"unlock_key": base64.RawURLEncoding.EncodeToString(a.UnlockKey(salt, auth.PRFInfo)),
		"credential": json.RawMessage(response),
	})
	if code != 204 {
		t.Fatalf("register/finish: %d %v", code, body)
	}
	return salt
}

// signInWithPasskey runs a sign-in over HTTP and returns the status code.
func (h *harness) signInWithPasskey(t *testing.T, a *authtest.Authenticator, salt []byte) (int, map[string]any) {
	t.Helper()
	code, body := h.do("POST", "/api/passkeys/login/begin", nil)
	if code != 200 {
		return code, body
	}
	options := body["options"].(map[string]any)["publicKey"].(map[string]any)
	allowed := options["allowCredentials"].([]any)
	if len(allowed) == 0 {
		t.Fatal("no credentials offered")
	}
	// The handle the authenticator would have stored at enrollment.
	handle := h.userHandle(t)
	response := a.Get(testRPID, testOrigin, challengeOf(t, body), handle)

	return h.do("POST", "/api/passkeys/login/finish", map[string]any{
		"ceremony":   body["ceremony"],
		"unlock_key": base64.RawURLEncoding.EncodeToString(a.UnlockKey(salt, auth.PRFInfo)),
		"credential": json.RawMessage(response),
	})
}

func (h *harness) userHandle(t *testing.T) []byte {
	t.Helper()
	var handle []byte
	if err := h.authDB.QueryRow(
		`SELECT webauthn_handle FROM users WHERE id = 1`).Scan(&handle); err != nil {
		t.Fatal(err)
	}
	return handle
}

// The complete journey: sign in with a password, enroll a passkey, sign out,
// and get back in with the passkey alone.
func TestPasskeyEnrollAndSignIn(t *testing.T) {
	h := newHarness(t)
	h.setupAccount(t)

	// Before enrolling, the login screen has no reason to offer the button.
	if _, body := h.do("GET", "/api/status", nil); body["passkeys"] != false {
		t.Fatalf("status offers passkeys before one exists: %v", body)
	}

	a := authtest.New(t)
	salt := h.enrollPasskey(t, a, "Phone")

	// It shows up as an unlock method, with the label the user gave it.
	code, body := h.do("GET", "/api/me", nil)
	if code != 200 {
		t.Fatalf("me: %d", code)
	}
	var found bool
	for _, raw := range body["unlock_via"].([]any) {
		sl := raw.(map[string]any)
		if sl["kind"] == "passkey" {
			found = true
			if sl["label"] != "Phone" {
				t.Errorf("label = %v", sl["label"])
			}
		}
	}
	if !found {
		t.Fatalf("the passkey is not listed: %v", body["unlock_via"])
	}

	h.do("POST", "/api/logout", nil)
	if _, body := h.do("GET", "/api/status", nil); body["passkeys"] != true {
		t.Fatalf("status hides the passkey: %v", body)
	}

	code, body = h.signInWithPasskey(t, a, salt)
	if code != 200 {
		t.Fatalf("passkey sign-in: %d %v", code, body)
	}
	if body["username"] != "ralf" {
		t.Fatalf("signed in as %v", body["username"])
	}
	// And the session is real: a protected endpoint answers.
	if code, _ := h.do("GET", "/api/me", nil); code != 200 {
		t.Fatalf("the passkey session does not work: %d", code)
	}
}

// The library has to be readable through a passkey session, which only holds if
// the passkey unwrapped the same DEK the password did.
func TestPasskeySessionSeesTheLibrary(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	h.uploadPhoto(t, "before.jpg", testPhoto(t, 400, 300), 32<<10)

	a := authtest.New(t)
	salt := h.enrollPasskey(t, a, "Phone")
	h.do("POST", "/api/logout", nil)

	if code, body := h.signInWithPasskey(t, a, salt); code != 200 {
		t.Fatalf("passkey sign-in: %d %v", code, body)
	}
	code, body := h.do("GET", "/api/media", nil)
	if code != 200 {
		t.Fatalf("media: %d", code)
	}
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("%d photos visible through the passkey session, want 1", len(items))
	}
	// Reading the pixels needs the DEK, not just the database.
	m := items[0].(map[string]any)
	resp, _ := h.raw(t, "GET", "/api/media/"+itoa(int(m["id"].(float64)))+"/thumb", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("thumbnail through a passkey session: %d", resp.StatusCode)
	}
}

func TestPasskeyEnrollmentNeedsASession(t *testing.T) {
	h := newHarness(t)
	h.setupAccount(t)
	h.do("POST", "/api/logout", nil)

	for _, path := range []string{
		"/api/passkeys/register/begin",
		"/api/passkeys/register/finish",
		"/api/passkeys/register/abort",
	} {
		if code, _ := h.do("POST", path, nil); code != http.StatusUnauthorized {
			t.Errorf("%s without a session: %d, want 401", path, code)
		}
	}
	if code, _ := h.do("DELETE", "/api/passkeys/1", nil); code != http.StatusUnauthorized {
		t.Errorf("removing a passkey without a session: %d", code)
	}
}

// Without a registered passkey there is nothing to sign in with, and saying so
// is better than handing out a challenge nothing can answer.
func TestSignInWithoutAPasskey(t *testing.T) {
	h := newHarness(t)
	h.setupAccount(t)
	h.do("POST", "/api/logout", nil)

	if code, _ := h.do("POST", "/api/passkeys/login/begin", nil); code != 404 {
		t.Fatalf("login/begin with no passkey: %d, want 404", code)
	}
}

// An abandoned enrollment must leave nothing that could later half-work.
func TestAbortedEnrollmentLeavesNothing(t *testing.T) {
	h := newHarness(t)
	h.setupAccount(t)

	_, body := h.do("POST", "/api/passkeys/register/begin", nil)
	ceremony := body["ceremony"]
	if code, _ := h.do("POST", "/api/passkeys/register/abort",
		map[string]any{"ceremony": ceremony}); code != 204 {
		t.Fatalf("abort: %d", code)
	}

	a := authtest.New(t)
	code, _ := h.do("POST", "/api/passkeys/register/finish", map[string]any{
		"ceremony":   ceremony,
		"label":      "Phone",
		"unlock_key": base64.RawURLEncoding.EncodeToString(a.UnlockKey([]byte("s"), auth.PRFInfo)),
		"credential": json.RawMessage(a.Create(testRPID, testOrigin, []byte("challenge"))),
	})
	if code == 204 {
		t.Fatal("an aborted ceremony was completed anyway")
	}
	h.do("POST", "/api/logout", nil)
	if code, _ := h.do("POST", "/api/passkeys/login/begin", nil); code != 404 {
		t.Fatal("an aborted enrollment left a usable passkey")
	}
}

// Removing a passkey has to close the door behind it.
func TestRemovedPasskeyCannotSignIn(t *testing.T) {
	h := newHarness(t)
	h.setupAccount(t)
	a := authtest.New(t)
	h.enrollPasskey(t, a, "Phone")

	var slotID int
	_, body := h.do("GET", "/api/me", nil)
	for _, raw := range body["unlock_via"].([]any) {
		if sl := raw.(map[string]any); sl["kind"] == "passkey" {
			slotID = int(sl["id"].(float64))
		}
	}
	if code, _ := h.do("DELETE", "/api/passkeys/"+itoa(slotID), nil); code != 204 {
		t.Fatalf("remove: %d", code)
	}
	h.do("POST", "/api/logout", nil)

	if code, _ := h.do("POST", "/api/passkeys/login/begin", nil); code != 404 {
		t.Fatalf("a removed passkey is still offered: %d", code)
	}
}

// The password and recovery slots are the way back in. Neither may be deleted
// through the passkey route, whatever id is passed.
func TestTheWayBackInCannotBeRemoved(t *testing.T) {
	h := newHarness(t)
	h.setupAccount(t)

	_, body := h.do("GET", "/api/me", nil)
	for _, raw := range body["unlock_via"].([]any) {
		sl := raw.(map[string]any)
		if sl["kind"] == "passkey" {
			continue
		}
		id := itoa(int(sl["id"].(float64)))
		if code, _ := h.do("DELETE", "/api/passkeys/"+id, nil); code == 204 {
			t.Fatalf("the %v slot was removed", sl["kind"])
		}
	}
	// Still able to sign in with the password.
	h.do("POST", "/api/logout", nil)
	if code, _ := h.do("POST", "/api/login",
		map[string]any{"username": "ralf", "password": "a-good-password"}); code != 200 {
		t.Fatal("the password no longer works")
	}
}

// A sign-in that presents a valid assertion but the wrong key must fail exactly
// like any other failed login, and say nothing about which half was wrong.
func TestPasskeySignInWithTheWrongKey(t *testing.T) {
	h := newHarness(t)
	h.setupAccount(t)
	a := authtest.New(t)
	h.enrollPasskey(t, a, "Phone")
	h.do("POST", "/api/logout", nil)

	code, body := h.signInWithPasskey(t, a, []byte("not the enrolled salt"))
	if code != http.StatusUnauthorized {
		t.Fatalf("sign-in with the wrong key: %d %v", code, body)
	}
	if body["error"] != "login failed" {
		t.Fatalf("error message gives detail away: %v", body["error"])
	}
	if code, _ := h.do("GET", "/api/me", nil); code != http.StatusUnauthorized {
		t.Fatal("a session was created anyway")
	}
}

// A challenge is single use.
func TestPasskeyChallengeCannotBeReplayed(t *testing.T) {
	h := newHarness(t)
	h.setupAccount(t)
	a := authtest.New(t)
	salt := h.enrollPasskey(t, a, "Phone")
	h.do("POST", "/api/logout", nil)

	_, begin := h.do("POST", "/api/passkeys/login/begin", nil)
	payload := map[string]any{
		"ceremony":   begin["ceremony"],
		"unlock_key": base64.RawURLEncoding.EncodeToString(a.UnlockKey(salt, auth.PRFInfo)),
		"credential": json.RawMessage(a.Get(testRPID, testOrigin, challengeOf(t, begin), h.userHandle(t))),
	}
	if code, body := h.do("POST", "/api/passkeys/login/finish", payload); code != 200 {
		t.Fatalf("first use: %d %v", code, body)
	}
	h.do("POST", "/api/logout", nil)
	if code, _ := h.do("POST", "/api/passkeys/login/finish", payload); code == 200 {
		t.Fatal("the same assertion was accepted twice")
	}
}

// With no relying party configured the endpoints have to say so rather than
// panic or half-answer.
func TestPasskeysDisabled(t *testing.T) {
	h := newHarness(t)
	h.server.RP = nil
	h.setupAccount(t)

	if code, _ := h.do("POST", "/api/passkeys/register/begin", nil); code != http.StatusNotImplemented {
		t.Errorf("register/begin with passkeys off: %d, want 501", code)
	}
	h.do("POST", "/api/logout", nil)
	if code, _ := h.do("POST", "/api/passkeys/login/begin", nil); code != http.StatusNotImplemented {
		t.Errorf("login/begin with passkeys off: %d, want 501", code)
	}
	if _, body := h.do("GET", "/api/status", nil); body["passkeys"] != false {
		t.Error("status offers passkeys with none configured")
	}
}

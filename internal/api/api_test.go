package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rverst/obscura/internal/auth"
	"github.com/rverst/obscura/internal/blob"
	"github.com/rverst/obscura/internal/library"
	"github.com/rverst/obscura/internal/media"
	"github.com/rverst/obscura/internal/store"
	"github.com/rverst/obscura/internal/upload"
)

type harness struct {
	t       *testing.T
	srv     *httptest.Server
	client  *http.Client
	dataDir string
	slots   *auth.Store
	authDB  *sql.DB
	server  *Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dataDir := t.TempDir()

	authDB, err := store.OpenPlain(filepath.Join(dataDir, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { authDB.Close() })
	if err := store.Migrate(authDB, store.SetAuth); err != nil {
		t.Fatal(err)
	}

	sessions := auth.NewManager(dataDir, time.Hour)
	t.Cleanup(sessions.CloseAll)

	blobs, err := blob.NewStore(filepath.Join(dataDir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	// Media endpoints need a thumbnailer; where it is missing the media tests
	// skip individually rather than failing the whole harness, so the auth
	// tests still run on a machine without libvips.
	var lib *library.Library
	if thumbs, err := media.NewThumbnailer(); err == nil {
		video, _ := media.NewVideo() // nil when ffmpeg is absent
		lib = library.New(blobs, thumbs, video, 2)
	}
	uploads := upload.NewManager(blobs, 16<<20, time.Hour)
	t.Cleanup(uploads.CloseAll)

	slots := auth.NewStore(authDB)
	// Pinned rather than taken from the request: httptest serves on
	// http://127.0.0.1:port, and an address is not a usable RP ID. Pinning is
	// also what a real deployment behind a proxy looks like.
	rp, err := auth.NewRelyingParty(slots, testOrigin, "obscura test")
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{
		Slots:    slots,
		RP:       rp,
		Sessions: sessions,
		Library:  lib,
		Uploads:  uploads,
		Blobs:    blobs,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Secure:   false, // httptest serves over plain http
	}
	srv := httptest.NewServer(s.Routes())
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	return &harness{
		t: t, srv: srv, client: &http.Client{Jar: jar},
		dataDir: dataDir, slots: slots, authDB: authDB, server: s,
	}
}

// newServerWithAssets builds a Server whose routes include the static handler,
// for tests about the boundary between API routes and the SPA fallback.
func newServerWithAssets(t *testing.T) *Server {
	t.Helper()
	dataDir := t.TempDir()
	authDB, err := store.OpenPlain(filepath.Join(dataDir, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { authDB.Close() })
	if err := store.Migrate(authDB, store.SetAuth); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewManager(dataDir, time.Hour)
	t.Cleanup(sessions.CloseAll)

	return &Server{
		Slots:       auth.NewStore(authDB),
		Sessions:    sessions,
		Assets:      testAssets(),
		AssetsBuilt: true,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func (h *harness) do(method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, rdr)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()

	out := map[string]any{}
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

func TestFullLoginFlow(t *testing.T) {
	h := newHarness(t)

	// --- Fresh: not initialized, not logged in ---------------------------
	code, body := h.do("GET", "/api/status", nil)
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if body["initialized"] != false || body["authenticated"] != false {
		t.Fatalf("a fresh server reports %v", body)
	}

	// Protected endpoints must be closed.
	if code, _ := h.do("GET", "/api/me", nil); code != 401 {
		t.Fatalf("/api/me without login: %d, want 401", code)
	}

	// --- Setup ------------------------------------------------------------
	code, body = h.do("POST", "/api/setup", map[string]string{
		"username": "ralf", "password": "a-good-password",
	})
	if code != 200 {
		t.Fatalf("setup: %d %v", code, body)
	}
	recovery, _ := body["recovery_code"].(string)
	if len(recovery) == 0 {
		t.Fatal("no recovery code returned")
	}
	if !strings.Contains(recovery, "-") {
		t.Fatalf("recovery code is not grouped: %q", recovery)
	}

	// Setup logs the user straight in.
	code, body = h.do("GET", "/api/status", nil)
	if body["initialized"] != true || body["authenticated"] != true {
		t.Fatalf("after setup: %v", body)
	}

	code, body = h.do("GET", "/api/me", nil)
	if code != 200 || body["username"] != "ralf" {
		t.Fatalf("/api/me: %d %v", code, body)
	}
	// The password and recovery slots must exist.
	if slots, ok := body["unlock_via"].([]any); !ok || len(slots) != 2 {
		t.Fatalf("unlock_via = %v, want 2 slots", body["unlock_via"])
	}

	// --- A second setup is rejected ---------------------------------------
	if code, _ := h.do("POST", "/api/setup", map[string]string{
		"username": "eve", "password": "takeover-attempt",
	}); code != 409 {
		t.Fatalf("second setup: %d, want 409", code)
	}

	// --- Logout ------------------------------------------------------------
	if code, _ := h.do("POST", "/api/logout", nil); code != 204 {
		t.Fatalf("logout: %d", code)
	}
	if code, _ := h.do("GET", "/api/me", nil); code != 401 {
		t.Fatalf("/api/me after logout: %d, want 401", code)
	}

	// --- Wrong password ----------------------------------------------------
	code, body = h.do("POST", "/api/login", map[string]string{
		"username": "ralf", "password": "wrong-wrong",
	})
	if code != 401 {
		t.Fatalf("wrong password: %d, want 401", code)
	}
	// The response must not reveal whether the user exists.
	msgWrong, _ := body["error"].(string)

	code, body = h.do("POST", "/api/login", map[string]string{
		"username": "nosuchuser", "password": "wrong-wrong",
	})
	if code != 401 {
		t.Fatalf("unknown user: %d, want 401", code)
	}
	if msgUnknown, _ := body["error"].(string); msgUnknown != msgWrong {
		t.Fatalf("error messages are distinguishable: %q vs %q", msgWrong, msgUnknown)
	}

	// --- Log in properly ---------------------------------------------------
	if code, body := h.do("POST", "/api/login", map[string]string{
		"username": "ralf", "password": "a-good-password",
	}); code != 200 {
		t.Fatalf("login: %d %v", code, body)
	}

	// --- Change the password ------------------------------------------------
	if code, body := h.do("POST", "/api/password", map[string]string{
		"old_password": "a-good-password", "new_password": "a-new-password",
	}); code != 204 {
		t.Fatalf("password change: %d %v", code, body)
	}
	// The session stays valid -- the DEK did not change.
	if code, _ := h.do("GET", "/api/me", nil); code != 200 {
		t.Fatalf("/api/me after the password change: %d, want 200", code)
	}

	h.do("POST", "/api/logout", nil)

	if code, _ := h.do("POST", "/api/login", map[string]string{
		"username": "ralf", "password": "a-good-password",
	}); code != 401 {
		t.Fatal("the old password still works")
	}
	if code, _ := h.do("POST", "/api/login", map[string]string{
		"username": "ralf", "password": "a-new-password",
	}); code != 200 {
		t.Fatal("the new password does not work")
	}
	h.do("POST", "/api/logout", nil)

	// --- The recovery code still works after the change --------------------
	if code, body := h.do("POST", "/api/login/recovery", map[string]string{
		"username": "ralf", "code": strings.ToLower(recovery),
	}); code != 200 {
		t.Fatalf("recovery login: %d %v", code, body)
	}

	// --- Nothing may be readable on disk -----------------------------------
	photos := filepath.Join(h.dataDir, "photos.db")
	raw, err := os.ReadFile(photos)
	if err != nil {
		t.Fatalf("photos.db is missing: %v", err)
	}
	if bytes.Contains(raw, []byte("ralf")) {
		t.Fatal("the username is stored in plaintext in photos.db")
	}
	if bytes.HasPrefix(raw, []byte("SQLite format 3")) {
		t.Fatal("photos.db is unencrypted")
	}
	// auth.db must not contain the recovery code.
	authRaw, err := os.ReadFile(filepath.Join(h.dataDir, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(authRaw, []byte(strings.ReplaceAll(recovery, "-", ""))) {
		t.Fatal("the recovery code is stored in plaintext in auth.db")
	}
	if bytes.Contains(authRaw, []byte("a-new-password")) {
		t.Fatal("the password is stored in plaintext in auth.db")
	}
}

func TestInvalidJSON(t *testing.T) {
	h := newHarness(t)
	req, _ := http.NewRequest("POST", h.srv.URL+"/api/login", strings.NewReader("{broken"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("broken JSON: %d, want 400", resp.StatusCode)
	}
}

func TestShortPasswordRejected(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.do("POST", "/api/setup", map[string]string{
		"username": "ralf", "password": "short",
	}); code != 400 {
		t.Fatalf("too-short password: %d, want 400", code)
	}
}

// After the last session logs out no vault may remain open -- otherwise the DEK
// would still be sitting in memory.
func TestVaultClosesAfterLastLogout(t *testing.T) {
	dataDir := t.TempDir()
	authDB, err := store.OpenPlain(filepath.Join(dataDir, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer authDB.Close()
	if err := store.Migrate(authDB, store.SetAuth); err != nil {
		t.Fatal(err)
	}

	sessions := auth.NewManager(dataDir, time.Hour)
	defer sessions.CloseAll()
	slots := auth.NewStore(authDB)

	if !sessions.Locked() {
		t.Fatal("a fresh manager reports an open vault")
	}

	dek, _, err := slots.Setup(t.Context(), "ralf", "a-good-password")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := sessions.Unlock(1, "ralf", dek)
	if err != nil {
		t.Fatal(err)
	}
	if sessions.Locked() {
		t.Fatal("no vault is open after Unlock")
	}

	sessions.Logout(sess.ID)
	if !sessions.Locked() {
		t.Fatal("the vault is still open after the last logout -- the DEK remains in memory")
	}
}

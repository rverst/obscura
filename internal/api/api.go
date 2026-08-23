// Package api provides the HTTP interface.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/rverst/obscura/internal/auth"
	"github.com/rverst/obscura/internal/blob"
	"github.com/rverst/obscura/internal/library"
	"github.com/rverst/obscura/internal/upload"
)

// SessionCookie is the name of the session cookie.
const SessionCookie = "obscura_session"

// Server bundles the handlers' dependencies.
type Server struct {
	Slots    *auth.Store
	Sessions *auth.Manager
	// RP runs the WebAuthn ceremonies. Nil disables passkeys entirely; the
	// endpoints then answer 501 rather than half-working.
	RP      *auth.RelyingParty
	Library *library.Library
	Uploads *upload.Manager
	Blobs   *blob.Store
	Log     *slog.Logger

	// Assets is the embedded frontend. When nil the API still works and the
	// UI routes explain that no frontend was built, which is a useful state
	// during development and for API-only deployments.
	Assets fs.FS
	// AssetsBuilt distinguishes a real build from the committed placeholder.
	AssetsBuilt bool

	// Secure controls the cookie's Secure flag. Always true in production
	// behind TLS; only switchable for local development over http, because
	// otherwise the browser refuses to set the cookie at all.
	Secure bool
}

// Routes builds the router.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/login/recovery", s.handleLoginRecovery)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	// Passkeys. Signing in needs no session; enrolling does, because the DEK
	// has to be wrapped with the key the browser derives.
	mux.HandleFunc("POST /api/passkeys/login/begin", s.handlePasskeyLoginBegin)
	mux.HandleFunc("POST /api/passkeys/login/finish", s.handlePasskeyLoginFinish)
	mux.Handle("POST /api/passkeys/register/begin", s.requireSession(http.HandlerFunc(s.handlePasskeyRegisterBegin)))
	mux.Handle("POST /api/passkeys/register/finish", s.requireSession(http.HandlerFunc(s.handlePasskeyRegisterFinish)))
	mux.Handle("POST /api/passkeys/register/abort", s.requireSession(http.HandlerFunc(s.handlePasskeyRegisterAbort)))
	mux.Handle("DELETE /api/passkeys/{id}", s.requireSession(http.HandlerFunc(s.handleRemovePasskey)))

	mux.Handle("GET /api/me", s.requireSession(http.HandlerFunc(s.handleMe)))
	mux.Handle("POST /api/password", s.requireSession(http.HandlerFunc(s.handleChangePassword)))

	// Uploads: create, ask the offset, append, finish. Deliberately small --
	// see internal/upload for why this is not tus.
	mux.Handle("POST /api/uploads", s.requireSession(http.HandlerFunc(s.handleCreateUpload)))
	mux.Handle("GET /api/uploads/{id}", s.requireSession(http.HandlerFunc(s.handleUploadStatus)))
	mux.Handle("PATCH /api/uploads/{id}", s.requireSession(http.HandlerFunc(s.handleUploadChunk)))
	mux.Handle("POST /api/uploads/{id}/finish", s.requireSession(http.HandlerFunc(s.handleFinishUpload)))
	mux.Handle("DELETE /api/uploads/{id}", s.requireSession(http.HandlerFunc(s.handleAbortUpload)))

	mux.Handle("POST /api/media/check", s.requireSession(http.HandlerFunc(s.handleCheckHash)))
	mux.Handle("GET /api/media", s.requireSession(http.HandlerFunc(s.handleListMedia)))
	mux.Handle("GET /api/media/{id}", s.requireSession(http.HandlerFunc(s.handleGetMedia)))
	mux.Handle("GET /api/media/{id}/thumb", s.requireSession(http.HandlerFunc(s.handleMediaContent("thumb"))))
	mux.Handle("GET /api/media/{id}/preview", s.requireSession(http.HandlerFunc(s.handleMediaContent("preview"))))
	mux.Handle("GET /api/media/{id}/original", s.requireSession(http.HandlerFunc(s.handleMediaContent("original"))))

	// Trash. DELETE on the media resource is reversible; permanent deletion is
	// a separate path, so no client destroys a photograph by mistyping a verb.
	mux.Handle("GET /api/trash", s.requireSession(http.HandlerFunc(s.handleListTrash)))
	mux.Handle("POST /api/trash/empty", s.requireSession(http.HandlerFunc(s.handleEmptyTrash)))
	mux.Handle("DELETE /api/media/{id}", s.requireSession(http.HandlerFunc(s.handleTrashMedia)))
	mux.Handle("POST /api/media/{id}/restore", s.requireSession(http.HandlerFunc(s.handleRestoreMedia)))
	mux.Handle("DELETE /api/media/{id}/purge", s.requireSession(http.HandlerFunc(s.handlePurgeMedia)))

	mux.Handle("GET /api/albums", s.requireSession(http.HandlerFunc(s.handleListAlbums)))
	mux.Handle("POST /api/albums", s.requireSession(http.HandlerFunc(s.handleCreateAlbum)))
	mux.Handle("GET /api/albums/{id}", s.requireSession(http.HandlerFunc(s.handleGetAlbum)))
	mux.Handle("PATCH /api/albums/{id}", s.requireSession(http.HandlerFunc(s.handleUpdateAlbum)))
	mux.Handle("DELETE /api/albums/{id}", s.requireSession(http.HandlerFunc(s.handleDeleteAlbum)))
	mux.Handle("PUT /api/albums/{id}/cover", s.requireSession(http.HandlerFunc(s.handleSetAlbumCover)))

	// Sub-albums: the breadcrumb, the move that builds the tree, and the move
	// that arranges one level of it.
	mux.Handle("GET /api/albums/{id}/path", s.requireSession(http.HandlerFunc(s.handleAlbumPath)))
	mux.Handle("PATCH /api/albums/{id}/parent", s.requireSession(http.HandlerFunc(s.handleMoveAlbum)))
	mux.Handle("PATCH /api/albums/{id}/position", s.requireSession(http.HandlerFunc(s.handleMoveAlbumTo)))

	// Favourites name the end state rather than toggling; see the handler.
	mux.Handle("PUT /api/media/{id}/favorite", s.requireSession(s.handleSetFavorite(true)))
	mux.Handle("DELETE /api/media/{id}/favorite", s.requireSession(s.handleSetFavorite(false)))

	mux.Handle("GET /api/albums/{id}/items", s.requireSession(http.HandlerFunc(s.handleAlbumItems)))
	mux.Handle("POST /api/albums/{id}/items", s.requireSession(http.HandlerFunc(s.handleAddToAlbum)))
	mux.Handle("DELETE /api/albums/{id}/items/{mediaID}", s.requireSession(http.HandlerFunc(s.handleRemoveFromAlbum)))
	// The move endpoint names neighbours rather than an index -- see the handler.
	mux.Handle("PATCH /api/albums/{id}/items/{mediaID}/position", s.requireSession(http.HandlerFunc(s.handleMoveInAlbum)))

	// Registered last and on the bare root, so it catches only what the API
	// routes above did not claim.
	if s.Assets != nil {
		mux.Handle("/", staticHandler(s.Assets, s.AssetsBuilt))
	}

	return mux
}

// --- Handlers ---------------------------------------------------------------

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	init, err := s.Slots.IsInitialized(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot determine status", err)
		return
	}
	_, authed := s.session(r)

	// Whether to offer the passkey button. Asked before signing in, so it can
	// only be a yes/no for the whole server -- the alternative is a button that
	// sometimes leads nowhere.
	passkeys := false
	if s.RP != nil {
		if _, err := s.RP.OriginUsable(s.origin(r)); err == nil {
			passkeys, _ = s.Slots.AnyPasskeys(r.Context())
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"initialized":   init,
		"authenticated": authed,
		"passkeys":      passkeys,
	})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}

	dek, recovery, err := s.Slots.Setup(r.Context(), req.Username, req.Password)
	if errors.Is(err, auth.ErrAlreadyInitialized) {
		s.fail(w, r, http.StatusConflict, "already initialized", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), err)
		return
	}

	userID, _, err := s.Slots.UnlockWithPassword(r.Context(), req.Username, req.Password)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "setup failed", err)
		return
	}
	sess, err := s.Sessions.Unlock(userID, req.Username, dek)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not open vault", err)
		return
	}
	s.setCookie(w, sess)

	// The recovery code is handed out EXACTLY HERE, once, and never again --
	// what is stored is only the DEK wrapped with it.
	writeJSON(w, http.StatusOK, map[string]any{
		"username":      sess.Username,
		"recovery_code": recovery,
		"warning": "Store this code somewhere safe and separate from the server. " +
			"Without it the library is lost if the password is forgotten.",
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	userID, dek, err := s.Slots.UnlockWithPassword(r.Context(), req.Username, req.Password)
	s.finishLogin(w, r, userID, req.Username, dek, err)
}

func (s *Server) handleLoginRecovery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Code     string `json:"code"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	userID, dek, err := s.Slots.UnlockWithRecoveryCode(r.Context(), req.Username, req.Code)
	s.finishLogin(w, r, userID, req.Username, dek, err)
}

func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, userID int64, username string, dek []byte, err error) {
	if errors.Is(err, auth.ErrUnlockFailed) {
		// Deliberately without detail: no hint as to whether the user exists or
		// only the secret was wrong.
		s.fail(w, r, http.StatusUnauthorized, "login failed", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "login failed", err)
		return
	}
	sess, err := s.Sessions.Unlock(userID, username, dek)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not open vault", err)
		return
	}
	s.setCookie(w, sess)
	s.purgeExpired(sess)
	writeJSON(w, http.StatusOK, map[string]any{"username": sess.Username})
}

// purgeExpired clears anything past its retention deadline.
//
// Triggered by a login rather than a timer, because the key exists only while
// someone is signed in -- there is no moment at which a background job could
// open the database on its own. Detached from the request so a large clear-out
// does not make signing in feel slow.
func (s *Server) purgeExpired(sess *auth.Session) {
	if s.Library == nil {
		return
	}
	v := library.Vault{DB: sess.DB, DEK: sess.DEK, UserID: sess.UserID}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		n, err := s.Library.PurgeExpired(ctx, v)
		if err != nil {
			s.Log.Warn("purging expired trash failed", "err", err)
			return
		}
		if n > 0 {
			s.Log.Info("purged expired trash", "count", n)
		}
	}()
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		s.Sessions.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.Secure,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	slots, err := s.Slots.ListSlots(r.Context(), sess.UserID)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot read slots", err)
		return
	}
	out := make([]map[string]any, 0, len(slots))
	for _, sl := range slots {
		entry := map[string]any{
			"id":         sl.ID,
			"kind":       sl.Kind,
			"label":      sl.Label,
			"created_at": sl.CreatedAt.Unix(),
		}
		if sl.LastUsedAt != nil {
			entry["last_used_at"] = sl.LastUsedAt.Unix()
		}
		out = append(out, entry)
	}
	// Whether this browser can be offered enrollment at all -- see origin().
	passkeys := false
	if s.RP != nil {
		_, err := s.RP.OriginUsable(s.origin(r))
		passkeys = err == nil
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username":       sess.Username,
		"unlock_via":     out,
		"passkeys_ready": passkeys,
	})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	var req struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	err := s.Slots.ChangePassword(r.Context(), sess.Username, req.Old, req.New)
	if errors.Is(err, auth.ErrUnlockFailed) {
		s.fail(w, r, http.StatusUnauthorized, "current password is wrong", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), err)
		return
	}
	// The DEK is unchanged, so the session stays valid -- no re-login and
	// nothing to re-encrypt.
	w.WriteHeader(http.StatusNoContent)
}

// --- Middleware and helpers -------------------------------------------------

type ctxKey int

const sessionKey ctxKey = iota

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.session(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "not logged in"})
			return
		}
		ctx := context.WithValue(r.Context(), sessionKey, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) session(r *http.Request) (*auth.Session, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil, false
	}
	return s.Sessions.Get(c.Value)
}

func (s *Server) setCookie(w http.ResponseWriter, sess *auth.Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.Secure,
		// Lax rather than Strict: following a link from an email should land
		// the user in a logged-in state. Additional CSRF protection comes from
		// every mutating endpoint requiring JSON.
		SameSite: http.SameSiteLaxMode,
		// No MaxAge: a session cookie. That matches the model -- the key only
		// lives until the next server restart anyway.
	})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, code int, msg string, err error) {
	// Details go to the log only, never into the response.
	s.Log.Warn("request failed", "path", r.URL.Path, "status", code, "err", err)
	writeJSON(w, code, map[string]any{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	// Bound the size: these endpoints only accept small JSON objects, uploads
	// go through a separate path.
	return readJSONLimit(w, r, dst, 64<<10)
}

// readJSONLimit is readJSON with a different bound, for the passkey endpoints:
// an attestation object carrying a certificate chain does not fit in the small
// allowance the rest of the API works with.
func readJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return false
	}
	return true
}

// sessionFrom returns the session; only use behind requireSession.
func sessionFrom(r *http.Request) *auth.Session {
	sess, _ := r.Context().Value(sessionKey).(*auth.Session)
	return sess
}

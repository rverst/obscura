// Package api provides the HTTP interface.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/rverst/obscura/internal/auth"
)

// SessionCookie is the name of the session cookie.
const SessionCookie = "obscura_session"

// Server bundles the handlers' dependencies.
type Server struct {
	Slots    *auth.Store
	Sessions *auth.Manager
	Log      *slog.Logger

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

	mux.Handle("GET /api/me", s.requireSession(http.HandlerFunc(s.handleMe)))
	mux.Handle("POST /api/password", s.requireSession(http.HandlerFunc(s.handleChangePassword)))

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
	writeJSON(w, http.StatusOK, map[string]any{
		"initialized":   init,
		"authenticated": authed,
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
	writeJSON(w, http.StatusOK, map[string]any{"username": sess.Username})
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
		out = append(out, map[string]any{
			"id":         sl.ID,
			"kind":       sl.Kind,
			"label":      sl.Label,
			"created_at": sl.CreatedAt.Unix(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username":   sess.Username,
		"unlock_via": out,
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
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
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

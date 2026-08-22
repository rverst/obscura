package api

import (
	"errors"
	"net/http"

	"github.com/rverst/obscura/internal/library"
)

// handleSetFavorite marks or unmarks a photo.
//
// PUT and DELETE on the same path rather than a toggle: a toggle depends on
// what the client believes the current state is, and two taps that race would
// land wherever they land. Naming the desired end state makes a repeat harmless.
func (s *Server) handleSetFavorite(favorite bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.mediaID(w, r)
		if !ok {
			return
		}
		at, err := library.SetFavorite(r.Context(), s.vault(r), id, favorite)
		if errors.Is(err, library.ErrNotFound) {
			s.fail(w, r, http.StatusNotFound, "no such media", err)
			return
		}
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "cannot change the favourite", err)
			return
		}
		out := map[string]any{"favorite": favorite}
		if at != nil {
			out["favorited_at"] = at.Unix()
		}
		writeJSON(w, http.StatusOK, out)
	}
}

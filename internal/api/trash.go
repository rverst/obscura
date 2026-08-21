package api

import (
	"errors"
	"net/http"

	"github.com/rverst/obscura/internal/library"
)

// handleTrashMedia moves a photo to the trash.
//
// DELETE on the media resource is the reversible step, not the destructive one.
// Permanent deletion is a separate call against a separate path, so a client
// cannot destroy a photograph by getting one verb wrong.
func (s *Server) handleTrashMedia(w http.ResponseWriter, r *http.Request) {
	id, ok := s.mediaID(w, r)
	if !ok {
		return
	}
	err := library.Trash(r.Context(), s.vault(r), id)
	if errors.Is(err, library.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such media", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot move to trash", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRestoreMedia(w http.ResponseWriter, r *http.Request) {
	id, ok := s.mediaID(w, r)
	if !ok {
		return
	}
	err := library.Restore(r.Context(), s.vault(r), id)
	if errors.Is(err, library.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "not in the trash", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot restore", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListTrash(w http.ResponseWriter, r *http.Request) {
	items, err := library.ListTrash(r.Context(), s.vault(r))
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot list the trash", err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		entry := mediaJSON(it.Media)
		entry["deleted_at"] = it.DeletedAt.Unix()
		// The deadline travels with the item so the client can say how long is
		// left rather than repeating a policy it would have to keep in sync.
		entry["purge_at"] = it.PurgeAt.Unix()
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handlePurgeMedia deletes one item permanently.
func (s *Server) handlePurgeMedia(w http.ResponseWriter, r *http.Request) {
	id, ok := s.mediaID(w, r)
	if !ok {
		return
	}
	err := s.Library.Purge(r.Context(), s.vault(r), id)
	if errors.Is(err, library.ErrNotFound) {
		// Also the answer when the item is in the library but not the trash:
		// purging is only ever the second step.
		s.fail(w, r, http.StatusNotFound, "not in the trash", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot delete permanently", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleEmptyTrash(w http.ResponseWriter, r *http.Request) {
	n, err := s.Library.EmptyTrash(r.Context(), s.vault(r))
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot empty the trash", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"purged": n})
}

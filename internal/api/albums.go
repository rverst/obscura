package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/rverst/obscura/internal/library"
)

func (s *Server) handleListAlbums(w http.ResponseWriter, r *http.Request) {
	albums, err := library.ListAlbums(r.Context(), s.vault(r))
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot list albums", err)
		return
	}
	out := make([]map[string]any, 0, len(albums))
	for _, a := range albums {
		out = append(out, albumJSON(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"albums": out})
}

func (s *Server) handleCreateAlbum(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		ParentID    *int64 `json:"parent_id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	a, err := library.CreateAlbum(r.Context(), s.vault(r), req.Title, req.Description, req.ParentID)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), err)
		return
	}
	writeJSON(w, http.StatusCreated, albumJSON(a))
}

func (s *Server) handleGetAlbum(w http.ResponseWriter, r *http.Request) {
	id, ok := s.albumID(w, r)
	if !ok {
		return
	}
	a, err := library.GetAlbum(r.Context(), s.vault(r), id)
	if errors.Is(err, library.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such album", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot read album", err)
		return
	}
	writeJSON(w, http.StatusOK, albumJSON(a))
}

func (s *Server) handleUpdateAlbum(w http.ResponseWriter, r *http.Request) {
	id, ok := s.albumID(w, r)
	if !ok {
		return
	}
	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	err := library.UpdateAlbum(r.Context(), s.vault(r), id, req.Title, req.Description)
	if errors.Is(err, library.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such album", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteAlbum(w http.ResponseWriter, r *http.Request) {
	id, ok := s.albumID(w, r)
	if !ok {
		return
	}
	// The count includes sub-albums: deleting a parent takes its children with
	// it, and the client needs to know more than one row changed.
	n, err := library.DeleteAlbum(r.Context(), s.vault(r), id)
	if errors.Is(err, library.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such album", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot delete album", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

// handleMoveAlbum re-parents an album, or lifts it to the top level.
func (s *Server) handleMoveAlbum(w http.ResponseWriter, r *http.Request) {
	id, ok := s.albumID(w, r)
	if !ok {
		return
	}
	var req struct {
		ParentID *int64 `json:"parent_id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	err := library.SetAlbumParent(r.Context(), s.vault(r), id, req.ParentID)
	if errors.Is(err, library.ErrAlbumCycle) {
		s.fail(w, r, http.StatusConflict, "an album cannot be moved inside itself", err)
		return
	}
	if errors.Is(err, library.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such album", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAlbumPath returns the breadcrumb, root first.
func (s *Server) handleAlbumPath(w http.ResponseWriter, r *http.Request) {
	id, ok := s.albumID(w, r)
	if !ok {
		return
	}
	path, err := library.AlbumPath(r.Context(), s.vault(r), id)
	if errors.Is(err, library.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such album", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot read album path", err)
		return
	}
	out := make([]map[string]any, 0, len(path))
	for _, a := range path {
		out = append(out, map[string]any{"id": a.ID, "title": a.Title})
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": out})
}

// handleAlbumItems returns the album's photos in their custom order.
func (s *Server) handleAlbumItems(w http.ResponseWriter, r *http.Request) {
	id, ok := s.albumID(w, r)
	if !ok {
		return
	}
	items, err := library.AlbumItems(r.Context(), s.vault(r), id)
	if errors.Is(err, library.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such album", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot list album items", err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		entry := mediaJSON(it.Media)
		// The client needs the key only to reason about ordering locally; the
		// server remains the authority on what it becomes after a move.
		entry["sort_key"] = it.SortKey
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleAddToAlbum(w http.ResponseWriter, r *http.Request) {
	id, ok := s.albumID(w, r)
	if !ok {
		return
	}
	var req struct {
		MediaIDs []int64 `json:"media_ids"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	added, err := library.AddToAlbum(r.Context(), s.vault(r), id, req.MediaIDs)
	if errors.Is(err, library.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such album", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot add to album", err)
		return
	}
	// Reports what actually happened rather than echoing the request: some ids
	// may already have been members, and the client should be able to say so.
	writeJSON(w, http.StatusOK, map[string]any{
		"added":     added,
		"requested": len(req.MediaIDs),
	})
}

func (s *Server) handleRemoveFromAlbum(w http.ResponseWriter, r *http.Request) {
	albumID, ok := s.albumID(w, r)
	if !ok {
		return
	}
	mediaID, err := strconv.ParseInt(r.PathValue("mediaID"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid media id", err)
		return
	}
	err = library.RemoveFromAlbum(r.Context(), s.vault(r), albumID, mediaID)
	if errors.Is(err, library.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "not in this album", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot remove from album", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMoveInAlbum repositions a photo between two neighbours.
//
// The body names the neighbours rather than a target index. An index would be
// ambiguous as soon as the album changed since the client rendered it; naming
// the neighbours describes an intent that survives that.
func (s *Server) handleMoveInAlbum(w http.ResponseWriter, r *http.Request) {
	albumID, ok := s.albumID(w, r)
	if !ok {
		return
	}
	mediaID, err := strconv.ParseInt(r.PathValue("mediaID"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid media id", err)
		return
	}

	var req struct {
		// null on either side means the very start or the very end.
		After  *int64 `json:"after"`
		Before *int64 `json:"before"`
	}
	if !readJSON(w, r, &req) {
		return
	}

	key, err := library.MoveInAlbum(r.Context(), s.vault(r), albumID, mediaID, req.After, req.Before)
	if errors.Is(err, library.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "album, photo or neighbour not found", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sort_key": key})
}

func (s *Server) handleSetAlbumCover(w http.ResponseWriter, r *http.Request) {
	id, ok := s.albumID(w, r)
	if !ok {
		return
	}
	var req struct {
		MediaID *int64 `json:"media_id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	err := library.SetAlbumCover(r.Context(), s.vault(r), id, req.MediaID)
	if errors.Is(err, library.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such album", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) albumID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid album id", err)
		return 0, false
	}
	return id, true
}

func albumJSON(a *library.Album) map[string]any {
	out := map[string]any{
		"id":          a.ID,
		"title":       a.Title,
		"description": a.Description,
		"item_count":  a.ItemCount,
		"total_count": a.TotalCount,
		"created_at":  a.CreatedAt.Unix(),
	}
	if a.ParentID != nil {
		out["parent_id"] = *a.ParentID
	}
	if a.CoverMediaID != nil {
		out["cover_media_id"] = *a.CoverMediaID
	}
	// The resolved cover travels as a full media record, not just an id: the
	// tile needs the version token to build a cacheable URL, and a second
	// round trip per album would defeat the point of resolving them in one
	// query.
	if a.Cover != nil {
		out["cover"] = mediaJSON(a.Cover)
	}
	return out
}

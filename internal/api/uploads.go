package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/rverst/obscura/internal/library"
	"github.com/rverst/obscura/internal/media"
	"github.com/rverst/obscura/internal/upload"
)

// uploadOffsetHeader carries where a chunk belongs in the file.
//
// A header rather than a query parameter so it survives proxies and shows up in
// logs next to the request it describes.
const uploadOffsetHeader = "Upload-Offset"

func (s *Server) handleCreateUpload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	}
	if !readJSON(w, r, &req) {
		return
	}

	sess := sessionFrom(r)
	up, err := s.Uploads.Create(sess.DEK, sess.UserID, req.Filename, req.Size)
	if errors.Is(err, upload.ErrTooLarge) {
		s.fail(w, r, http.StatusRequestEntityTooLarge, err.Error(), err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"upload_id": up.ID,
		"offset":    up.Received(),
	})
}

// handleUploadStatus is how a resuming client finds out where to continue.
func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	up, err := s.Uploads.Status(r.PathValue("id"), sess.UserID)
	if errors.Is(err, upload.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such upload", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot read upload", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"upload_id": up.ID,
		"filename":  up.Filename,
		"offset":    up.Received(),
	})
}

// handleUploadChunk appends one chunk.
//
// The body is raw bytes, not multipart: multipart would mean buffering or
// parsing a whole part before any of it could be encrypted, and the point of
// this path is that bytes go straight into an encrypting writer.
func (s *Server) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	raw := r.Header.Get(uploadOffsetHeader)
	if raw == "" {
		s.fail(w, r, http.StatusBadRequest, uploadOffsetHeader+" header is required", nil)
		return
	}
	offset, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || offset < 0 {
		s.fail(w, r, http.StatusBadRequest, "invalid "+uploadOffsetHeader, err)
		return
	}

	sess := sessionFrom(r)
	up, err := s.Uploads.Append(r.PathValue("id"), sess.UserID, offset, r.Body)
	switch {
	case errors.Is(err, upload.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such upload", err)
		return
	case errors.Is(err, upload.ErrOffsetMismatch):
		// 409 rather than 400: the request is well formed, it just does not fit
		// the server's state. The client should re-read the status and resume.
		s.fail(w, r, http.StatusConflict, err.Error(), err)
		return
	case errors.Is(err, upload.ErrTooLarge):
		s.fail(w, r, http.StatusRequestEntityTooLarge, err.Error(), err)
		return
	case err != nil:
		s.fail(w, r, http.StatusInternalServerError, "cannot store chunk", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"offset": up.Received()})
}

// handleFinishUpload commits the blob and ingests it.
func (s *Server) handleFinishUpload(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	res, err := s.Uploads.Finish(r.PathValue("id"), sess.UserID)
	if errors.Is(err, upload.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such upload", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot finalise upload", err)
		return
	}

	m, err := s.Library.Ingest(r.Context(), s.vault(r), library.Ingestion{
		BlobID:   res.BlobID,
		Filename: res.Filename,
		Size:     res.Size,
		SHA256:   res.SHA256,
	})
	if errors.Is(err, library.ErrDuplicate) {
		// Not an error from the user's point of view -- the photo is in the
		// library, which is what they wanted. The existing record comes back so
		// the client can link to it.
		writeJSON(w, http.StatusOK, map[string]any{
			"duplicate": true,
			"media":     mediaJSON(m),
		})
		return
	}
	if errors.Is(err, media.ErrNotAnImage) {
		// A file the decoder cannot read is a bad upload, not a server fault.
		s.fail(w, r, http.StatusUnsupportedMediaType, "file could not be decoded as an image", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot process upload", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"duplicate": false,
		"media":     mediaJSON(m),
	})
}

func (s *Server) handleAbortUpload(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	err := s.Uploads.Abort(r.PathValue("id"), sess.UserID)
	if errors.Is(err, upload.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such upload", err)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot abort upload", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package api

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/rverst/obscura/internal/blob"
	"github.com/rverst/obscura/internal/library"
)

// derivativeMaxAge is how long a browser may reuse a derivative whose URL names
// the exact content it wants.
//
// Only a versioned URL may be cached this way. The bare path carries a media
// id, and ids are sequential: after a database is reset, /api/media/1/thumb is
// a different photograph. Marking that immutable told browsers never to ask
// again, and they duly served the previous library's thumbnails -- with the
// lightbox and the grid disagreeing, because their entries expired differently.
//
// Private because these are someone's photographs and must never sit in a
// shared proxy.
const derivativeMaxAge = 365 * 24 * time.Hour

func (s *Server) vault(r *http.Request) library.Vault {
	sess := sessionFrom(r)
	return library.Vault{DB: sess.DB, DEK: sess.DEK, UserID: sess.UserID}
}

// handleListMedia returns a page of the timeline, newest first.
func (s *Server) handleListMedia(w http.ResponseWriter, r *http.Request) {
	opt := library.ListOptions{}
	q := r.URL.Query()

	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			s.fail(w, r, http.StatusBadRequest, "invalid limit", err)
			return
		}
		opt.Limit = n
	}
	// Keyset cursor: both halves or neither, otherwise paging would silently
	// restart from the top and loop forever.
	before, beforeID := q.Get("before"), q.Get("before_id")
	if (before == "") != (beforeID == "") {
		s.fail(w, r, http.StatusBadRequest,
			"before and before_id must be given together", nil)
		return
	}
	if before != "" {
		secs, err := strconv.ParseInt(before, 10, 64)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "invalid before", err)
			return
		}
		id, err := strconv.ParseInt(beforeID, 10, 64)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "invalid before_id", err)
			return
		}
		t := time.Unix(secs, 0)
		opt.Before, opt.BeforeID = &t, id
	}

	items, err := library.List(r.Context(), s.vault(r), opt)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot list media", err)
		return
	}

	out := make([]map[string]any, 0, len(items))
	for _, m := range items {
		out = append(out, mediaJSON(m))
	}
	resp := map[string]any{"items": out}
	// Hand the client the cursor for the next page rather than making it
	// reconstruct one from the last item.
	if len(items) > 0 {
		last := items[len(items)-1]
		resp["next"] = map[string]any{
			"before":    last.SortTime().Unix(),
			"before_id": last.ID,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetMedia(w http.ResponseWriter, r *http.Request) {
	m, ok := s.lookupMedia(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, mediaJSON(m))
}

// handleMediaContent serves a derivative or the original.
//
// The blob reader is seekable, so http.ServeContent can answer range requests
// directly: that is what makes video scrubbing work without decrypting a file
// from the start on every seek.
func (s *Server) handleMediaContent(variant string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, ok := s.lookupMedia(w, r)
		if !ok {
			return
		}

		var blobID, contentType string
		switch variant {
		case "thumb":
			blobID, contentType = m.BlobThumb, "image/jpeg"
		case "preview":
			blobID, contentType = m.BlobPreview, "image/jpeg"
		default:
			blobID, contentType = m.BlobOrig, m.MIME
		}
		if blobID == "" {
			// A video has no image derivatives until M5; that is a missing
			// variant, not a broken server.
			s.fail(w, r, http.StatusNotFound, "no "+variant+" for this item", nil)
			return
		}

		sess := sessionFrom(r)
		rd, err := s.Blobs.Open(sess.DEK, blobID)
		if errors.Is(err, blob.ErrNotFound) {
			s.fail(w, r, http.StatusNotFound, "content missing", err)
			return
		}
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "cannot read content", err)
			return
		}
		defer rd.Close()

		w.Header().Set("Content-Type", contentType)
		// The blob id is random and changes whenever content is regenerated,
		// which makes it an exact ETag.
		w.Header().Set("ETag", `"`+blobID+`"`)

		// A URL that names the content it wants can be cached indefinitely,
		// because a different blob means a different URL. A bare path cannot:
		// it must be revalidated, and the ETag makes that cheap. Correctness
		// deliberately does not depend on the client remembering the parameter.
		if r.URL.Query().Get("v") == blobID {
			w.Header().Set("Cache-Control",
				fmt.Sprintf("private, max-age=%d, immutable", int(derivativeMaxAge.Seconds())))
		} else {
			w.Header().Set("Cache-Control", "private, no-cache")
		}
		if variant == "original" {
			w.Header().Set("Content-Disposition",
				`attachment; filename*=UTF-8''`+urlEncode(m.Filename))
		}

		// A zero modtime tells ServeContent to skip Last-Modified rather than
		// invent one; the ETag already covers validation.
		http.ServeContent(w, r, m.Filename, time.Time{}, rd)
	}
}

// handleCheckHash tells a client whether content is already stored.
//
// This is what makes re-adding a folder cheap: the client hashes locally and
// asks before sending a single byte.
func (s *Server) handleCheckHash(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SHA256 string `json:"sha256"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	sum, err := hex.DecodeString(req.SHA256)
	if err != nil || len(sum) != 32 {
		s.fail(w, r, http.StatusBadRequest, "sha256 must be 64 hex characters", err)
		return
	}

	m, err := library.FindBySHA256(r.Context(), s.vault(r), sum)
	if errors.Is(err, library.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"exists": false})
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "lookup failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exists": true, "media": mediaJSON(m)})
}

func (s *Server) lookupMedia(w http.ResponseWriter, r *http.Request) (*library.Media, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid media id", err)
		return nil, false
	}
	m, err := library.Get(r.Context(), s.vault(r), id)
	if errors.Is(err, library.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such media", err)
		return nil, false
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot read media", err)
		return nil, false
	}
	return m, true
}

func mediaJSON(m *library.Media) map[string]any {
	out := map[string]any{
		"id":         m.ID,
		"kind":       m.Kind,
		"filename":   m.Filename,
		"mime":       m.MIME,
		"bytes":      m.Bytes,
		"sha256":     hex.EncodeToString(m.SHA256),
		"width":      m.Width,
		"height":     m.Height,
		"created_at": m.CreatedAt.Unix(),
		"has_thumb":  m.BlobThumb != "",
		// Cache-busting tokens, one per derivative. Opaque to the client: it
		// only needs them to build a URL that changes when the content does.
		// The values are random blob ids and reveal nothing about the picture.
		"v": map[string]any{
			"thumb":    m.BlobThumb,
			"preview":  m.BlobPreview,
			"original": m.BlobOrig,
		},
	}
	if m.TakenAt != nil {
		out["taken_at"] = m.TakenAt.Unix()
	}
	if m.DurationMs > 0 {
		out["duration_ms"] = m.DurationMs
	}

	// Camera details are only interesting when present; emitting empty strings
	// would make the client test every field before rendering.
	exif := map[string]any{}
	if m.CameraMake != "" {
		exif["make"] = m.CameraMake
	}
	if m.CameraModel != "" {
		exif["model"] = m.CameraModel
	}
	if m.Lens != "" {
		exif["lens"] = m.Lens
	}
	if m.ISO > 0 {
		exif["iso"] = m.ISO
	}
	if m.FNumber > 0 {
		exif["f_number"] = m.FNumber
	}
	if m.Exposure != "" {
		exif["exposure"] = m.Exposure
	}
	if m.FocalLen > 0 {
		exif["focal_length"] = m.FocalLen
	}
	if len(exif) > 0 {
		out["exif"] = exif
	}
	if m.GPSLat != nil && m.GPSLon != nil {
		out["gps"] = map[string]any{"lat": *m.GPSLat, "lon": *m.GPSLon}
	}
	return out
}

// urlEncode percent-encodes a filename for Content-Disposition.
func urlEncode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			out = append(out, c)
			continue
		}
		out = append(out, '%', hexDigits[c>>4], hexDigits[c&0x0F])
	}
	return string(out)
}

package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

// setupAccount creates the single account and signs in.
//
// Separate from setupAndLogin because it asks nothing of the image tools: tests
// that never touch a photograph should still run on a machine without libvips
// rather than quietly skipping.
func (h *harness) setupAccount(t *testing.T) {
	t.Helper()
	if code, body := h.do("POST", "/api/setup", map[string]string{
		"username": "ralf", "password": "a-good-password",
	}); code != 200 {
		t.Fatalf("setup: %d %v", code, body)
	}
}

// setupAndLogin gets the harness to a state where media endpoints are usable.
func (h *harness) setupAndLogin(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("vipsthumbnail"); err != nil {
		t.Skip("vipsthumbnail not installed -- run these through `make test` (Docker)")
	}
	h.setupAccount(t)
}

func (h *harness) raw(t *testing.T, method, path string, body io.Reader, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, h.srv.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func testPhoto(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 0x40, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// uploadPhoto runs the full create/append/finish dance the client will use.
func (h *harness) uploadPhoto(t *testing.T, name string, content []byte, chunkSize int) map[string]any {
	t.Helper()
	code, body := h.do("POST", "/api/uploads", map[string]any{
		"filename": name, "size": len(content),
	})
	if code != 201 {
		t.Fatalf("create upload: %d %v", code, body)
	}
	id, _ := body["upload_id"].(string)
	if id == "" {
		t.Fatalf("no upload_id in %v", body)
	}

	for off := 0; off < len(content); off += chunkSize {
		end := min(off+chunkSize, len(content))
		resp, raw := h.raw(t, "PATCH", "/api/uploads/"+id,
			bytes.NewReader(content[off:end]),
			map[string]string{uploadOffsetHeader: itoa(off)})
		if resp.StatusCode != 200 {
			t.Fatalf("chunk at %d: %d %s", off, resp.StatusCode, raw)
		}
	}

	code, body = h.do("POST", "/api/uploads/"+id+"/finish", nil)
	if code != 201 && code != 200 {
		t.Fatalf("finish: %d %v", code, body)
	}
	return body
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func mediaOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	m, ok := body["media"].(map[string]any)
	if !ok {
		t.Fatalf("no media object in %v", body)
	}
	return m
}

func TestUploadAndServeRoundtrip(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)

	content := testPhoto(t, 1200, 900)
	body := h.uploadPhoto(t, "holiday.jpg", content, 64<<10)

	if body["duplicate"] != false {
		t.Fatalf("first upload reported as duplicate: %v", body)
	}
	m := mediaOf(t, body)
	if m["filename"] != "holiday.jpg" {
		t.Fatalf("filename = %v", m["filename"])
	}
	if m["width"].(float64) != 1200 || m["height"].(float64) != 900 {
		t.Fatalf("dimensions %v x %v", m["width"], m["height"])
	}
	id := itoa(int(m["id"].(float64)))

	// The original must come back byte for byte -- it is the archival copy.
	resp, raw := h.raw(t, "GET", "/api/media/"+id+"/original", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("original: %d", resp.StatusCode)
	}
	if !bytes.Equal(raw, content) {
		t.Fatalf("original differs: got %d bytes, want %d", len(raw), len(content))
	}

	for _, variant := range []string{"thumb", "preview"} {
		resp, raw := h.raw(t, "GET", "/api/media/"+id+"/"+variant, nil, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", variant, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
			t.Fatalf("%s Content-Type = %q", variant, ct)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("%s is not a decodable JPEG: %v", variant, err)
		}
		if variant == "thumb" && (cfg.Width > 256 || cfg.Height > 256) {
			t.Fatalf("thumb is %dx%d, exceeds its box", cfg.Width, cfg.Height)
		}
		// Photos must not be cacheable by a shared proxy.
		if cc := resp.Header.Get("Cache-Control"); !bytes.Contains([]byte(cc), []byte("private")) {
			t.Fatalf("%s Cache-Control = %q, must be private", variant, cc)
		}
	}
}

// Range support is what makes video scrubbing work later; it has to be real,
// not a full-body response with a 206 slapped on it.
func TestOriginalSupportsRangeRequests(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)

	content := testPhoto(t, 2000, 1500)
	m := mediaOf(t, h.uploadPhoto(t, "big.jpg", content, 128<<10))
	id := itoa(int(m["id"].(float64)))

	resp, raw := h.raw(t, "GET", "/api/media/"+id+"/original", nil,
		map[string]string{"Range": "bytes=100-199"})
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status %d, want 206", resp.StatusCode)
	}
	if len(raw) != 100 {
		t.Fatalf("got %d bytes, want 100", len(raw))
	}
	if !bytes.Equal(raw, content[100:200]) {
		t.Fatal("the returned range is not the requested one")
	}
}

// A chunked upload that is interrupted must be resumable from the offset the
// server reports -- that is the entire reason this protocol exists.
func TestResumeInterruptedUpload(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	content := testPhoto(t, 900, 700)

	code, body := h.do("POST", "/api/uploads", map[string]any{
		"filename": "interrupted.jpg", "size": len(content),
	})
	if code != 201 {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["upload_id"].(string)

	half := len(content) / 2
	if resp, raw := h.raw(t, "PATCH", "/api/uploads/"+id,
		bytes.NewReader(content[:half]),
		map[string]string{uploadOffsetHeader: "0"}); resp.StatusCode != 200 {
		t.Fatalf("first half: %d %s", resp.StatusCode, raw)
	}

	// Client reconnects and asks where it got to.
	code, body = h.do("GET", "/api/uploads/"+id, nil)
	if code != 200 {
		t.Fatalf("status: %d %v", code, body)
	}
	offset := int(body["offset"].(float64))
	if offset != half {
		t.Fatalf("server reports offset %d, want %d", offset, half)
	}

	if resp, raw := h.raw(t, "PATCH", "/api/uploads/"+id,
		bytes.NewReader(content[offset:]),
		map[string]string{uploadOffsetHeader: itoa(offset)}); resp.StatusCode != 200 {
		t.Fatalf("resume: %d %s", resp.StatusCode, raw)
	}

	code, body = h.do("POST", "/api/uploads/"+id+"/finish", nil)
	if code != 201 {
		t.Fatalf("finish: %d %v", code, body)
	}
	mid := itoa(int(mediaOf(t, body)["id"].(float64)))

	resp, raw := h.raw(t, "GET", "/api/media/"+mid+"/original", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("original: %d", resp.StatusCode)
	}
	if !bytes.Equal(raw, content) {
		t.Fatal("the resumed upload did not reassemble correctly")
	}
}

func TestChunkWithGapIsRejected(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)

	code, body := h.do("POST", "/api/uploads", map[string]any{"filename": "x.jpg", "size": 0})
	if code != 201 {
		t.Fatalf("create: %d", code)
	}
	id := body["upload_id"].(string)

	h.raw(t, "PATCH", "/api/uploads/"+id, bytes.NewReader([]byte("hello")),
		map[string]string{uploadOffsetHeader: "0"})
	// Jump past what the server holds.
	resp, _ := h.raw(t, "PATCH", "/api/uploads/"+id, bytes.NewReader([]byte("world")),
		map[string]string{uploadOffsetHeader: "1000"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, want 409 so the client knows to re-read the offset", resp.StatusCode)
	}
}

func TestChunkRequiresOffsetHeader(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)

	code, body := h.do("POST", "/api/uploads", map[string]any{"filename": "x.jpg", "size": 0})
	if code != 201 {
		t.Fatalf("create: %d", code)
	}
	resp, _ := h.raw(t, "PATCH", "/api/uploads/"+body["upload_id"].(string),
		bytes.NewReader([]byte("data")), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
}

func TestDuplicateUploadReportsExisting(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	content := testPhoto(t, 500, 400)

	first := mediaOf(t, h.uploadPhoto(t, "a.jpg", content, 32<<10))
	second := h.uploadPhoto(t, "a-copy.jpg", content, 32<<10)

	if second["duplicate"] != true {
		t.Fatalf("second upload not flagged as duplicate: %v", second)
	}
	if mediaOf(t, second)["id"] != first["id"] {
		t.Fatal("duplicate did not point at the existing record")
	}

	code, body := h.do("GET", "/api/media", nil)
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	if items := body["items"].([]any); len(items) != 1 {
		t.Fatalf("%d items in the library, want 1", len(items))
	}
}

// The client hashes locally and asks before sending anything, which is what
// makes re-adding a folder cheap.
func TestCheckHashBeforeUploading(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	content := testPhoto(t, 300, 200)

	sum := sha256hex(content)
	code, body := h.do("POST", "/api/media/check", map[string]string{"sha256": sum})
	if code != 200 {
		t.Fatalf("check: %d %v", code, body)
	}
	if body["exists"] != false {
		t.Fatalf("unknown content reported as present: %v", body)
	}

	h.uploadPhoto(t, "known.jpg", content, 16<<10)

	code, body = h.do("POST", "/api/media/check", map[string]string{"sha256": sum})
	if code != 200 {
		t.Fatalf("check after upload: %d %v", code, body)
	}
	if body["exists"] != true {
		t.Fatalf("stored content reported as absent: %v", body)
	}
	if mediaOf(t, body)["filename"] != "known.jpg" {
		t.Fatal("check returned the wrong record")
	}
}

func TestCheckHashRejectsMalformed(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	for _, bad := range []string{"", "xyz", "abcd"} {
		if code, _ := h.do("POST", "/api/media/check", map[string]string{"sha256": bad}); code != 400 {
			t.Errorf("sha256=%q accepted with status %d", bad, code)
		}
	}
}

func TestListPagesWithCursor(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	for _, dim := range []int{100, 200, 300} {
		h.uploadPhoto(t, "p.jpg", testPhoto(t, dim, dim), 32<<10)
	}

	code, body := h.do("GET", "/api/media?limit=2", nil)
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	first := body["items"].([]any)
	if len(first) != 2 {
		t.Fatalf("%d items on page one, want 2", len(first))
	}
	if total, _ := body["total"].(float64); total != 3 {
		t.Fatalf("total = %v, want 3 across all pages", body["total"])
	}
	next, ok := body["next"].(map[string]any)
	if !ok {
		t.Fatal("no cursor handed back for the next page")
	}

	code, body = h.do("GET", "/api/media?limit=2&before="+
		itoa(int(next["before"].(float64)))+"&before_id="+
		itoa(int(next["before_id"].(float64))), nil)
	if code != 200 {
		t.Fatalf("page two: %d", code)
	}
	second := body["items"].([]any)
	if len(second) != 1 {
		t.Fatalf("%d items on page two, want 1", len(second))
	}

	seen := map[any]bool{}
	for _, it := range append(first, second...) {
		id := it.(map[string]any)["id"]
		if seen[id] {
			t.Fatalf("item %v appeared on both pages", id)
		}
		seen[id] = true
	}
}

func TestListUnfiledOnly(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	ids := h.uploadIDs(t, 3)
	albumID := h.makeAlbum(t, "Sorted")
	h.do("POST", "/api/albums/"+albumID+"/items", map[string]any{"media_ids": ids[:2]})

	code, body := h.do("GET", "/api/media?unfiled=1", nil)
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	items := body["items"].([]any)
	if len(items) != 1 || int64(items[0].(map[string]any)["id"].(float64)) != ids[2] {
		t.Fatalf("unfiled items = %v, want only %d", items, ids[2])
	}
	if total, _ := body["total"].(float64); total != 1 {
		t.Fatalf("total = %v, want 1", body["total"])
	}
}

func TestListRejectsHalfCursor(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	// A cursor is only meaningful as a pair; half of one would silently restart
	// paging from the top and loop.
	if code, _ := h.do("GET", "/api/media?before=1600000000", nil); code != 400 {
		t.Errorf("before without before_id: status %d, want 400", code)
	}
	if code, _ := h.do("GET", "/api/media?before_id=5", nil); code != 400 {
		t.Errorf("before_id without before: status %d, want 400", code)
	}
}

func TestUnknownMediaIs404(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	for _, path := range []string{"/api/media/9999", "/api/media/9999/thumb", "/api/media/9999/original"} {
		if code, _ := h.do("GET", path, nil); code != 404 {
			t.Errorf("%s: status %d, want 404", path, code)
		}
	}
}

// Every media endpoint must be closed without a session -- these are the paths
// that actually serve someone's photos.
func TestMediaEndpointsRequireSession(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	m := mediaOf(t, h.uploadPhoto(t, "secret.jpg", testPhoto(t, 200, 200), 32<<10))
	id := itoa(int(m["id"].(float64)))

	h.do("POST", "/api/logout", nil)

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/media"},
		{"GET", "/api/media/" + id},
		{"GET", "/api/media/" + id + "/thumb"},
		{"GET", "/api/media/" + id + "/preview"},
		{"GET", "/api/media/" + id + "/original"},
		{"POST", "/api/media/check"},
		{"POST", "/api/uploads"},
	} {
		resp, _ := h.raw(t, tc.method, tc.path, bytes.NewReader([]byte("{}")),
			map[string]string{"Content-Type": "application/json"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without a session: status %d, want 401",
				tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestAbortUploadDiscardsIt(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)

	code, body := h.do("POST", "/api/uploads", map[string]any{"filename": "x.jpg", "size": 0})
	if code != 201 {
		t.Fatalf("create: %d", code)
	}
	id := body["upload_id"].(string)
	h.raw(t, "PATCH", "/api/uploads/"+id, bytes.NewReader([]byte("partial")),
		map[string]string{uploadOffsetHeader: "0"})

	if code, _ := h.do("DELETE", "/api/uploads/"+id, nil); code != 204 {
		t.Fatalf("abort: %d", code)
	}
	if code, _ := h.do("GET", "/api/uploads/"+id, nil); code != 404 {
		t.Fatalf("aborted upload still addressable: %d", code)
	}
}

func TestUndecodableUploadIsRejected(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)

	code, body := h.do("POST", "/api/uploads", map[string]any{"filename": "broken.jpg", "size": 0})
	if code != 201 {
		t.Fatalf("create: %d", code)
	}
	id := body["upload_id"].(string)
	h.raw(t, "PATCH", "/api/uploads/"+id, bytes.NewReader([]byte("definitely not an image")),
		map[string]string{uploadOffsetHeader: "0"})

	code, body = h.do("POST", "/api/uploads/"+id+"/finish", nil)
	if code != http.StatusUnsupportedMediaType {
		t.Fatalf("status %d, want 415 -- %v", code, body)
	}
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Media ids are sequential, so /api/media/1/thumb names a different photograph
// after a library is reset. Telling a browser that URL is immutable made it
// serve the previous library's thumbnails and never ask again -- which is
// exactly what was reported.
func TestBareDerivativeURLIsRevalidated(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	m := mediaOf(t, h.uploadPhoto(t, "p.jpg", testPhoto(t, 400, 300), 32<<10))
	id := itoa(int(m["id"].(float64)))

	resp, _ := h.raw(t, "GET", "/api/media/"+id+"/thumb", nil, nil)
	cc := resp.Header.Get("Cache-Control")
	if strings.Contains(cc, "immutable") {
		t.Fatalf("Cache-Control = %q -- a bare, reusable URL must not claim to be immutable", cc)
	}
	if !strings.Contains(cc, "no-cache") {
		t.Fatalf("Cache-Control = %q, want revalidation", cc)
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatal("no ETag, so revalidation would refetch the whole image every time")
	}
}

// A URL that names the blob it wants cannot go stale, so it may be cached hard.
func TestVersionedDerivativeURLIsImmutable(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	m := mediaOf(t, h.uploadPhoto(t, "p.jpg", testPhoto(t, 400, 300), 32<<10))
	id := itoa(int(m["id"].(float64)))

	versions, ok := m["v"].(map[string]any)
	if !ok {
		t.Fatalf("no version tokens in the media object: %v", m)
	}
	token, _ := versions["thumb"].(string)
	if token == "" {
		t.Fatal("no thumb version token")
	}

	resp, _ := h.raw(t, "GET", "/api/media/"+id+"/thumb?v="+token, nil, nil)
	cc := resp.Header.Get("Cache-Control")
	if !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=") {
		t.Fatalf("Cache-Control = %q, want a long immutable cache", cc)
	}
	// Never in a shared cache: these are someone's photographs.
	if !strings.Contains(cc, "private") {
		t.Fatalf("Cache-Control = %q, must be private", cc)
	}

	// A stale or guessed token must not unlock the long cache.
	resp, _ = h.raw(t, "GET", "/api/media/"+id+"/thumb?v=wrongtoken", nil, nil)
	if strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatal("a mismatched version token was treated as immutable")
	}
}

// Two different photographs must never share a derivative URL, or a cache will
// serve one for the other.
func TestVersionTokensDifferPerPhoto(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	a := mediaOf(t, h.uploadPhoto(t, "a.jpg", testPhoto(t, 400, 300), 32<<10))
	b := mediaOf(t, h.uploadPhoto(t, "b.jpg", testPhoto(t, 500, 200), 32<<10))

	va := a["v"].(map[string]any)
	vb := b["v"].(map[string]any)
	for _, variant := range []string{"thumb", "preview", "original"} {
		if va[variant] == vb[variant] {
			t.Errorf("%s token is identical for two different photos: %v", variant, va[variant])
		}
	}
}

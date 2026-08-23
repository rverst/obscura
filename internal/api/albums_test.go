package api

import (
	"net/http"
	"strconv"
	"testing"
)

// atoi turns an id back into a number, for the request bodies that name one
// album as another's neighbour.
func atoi(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		panic(err)
	}
	return n
}

// makeAlbum creates an album and returns its id as a path segment.
func (h *harness) makeAlbum(t *testing.T, title string) string {
	t.Helper()
	code, body := h.do("POST", "/api/albums", map[string]string{"title": title})
	if code != 201 {
		t.Fatalf("create album: %d %v", code, body)
	}
	return itoa(int(body["id"].(float64)))
}

// uploadIDs uploads n distinct photos and returns their media ids.
func (h *harness) uploadIDs(t *testing.T, n int) []int64 {
	t.Helper()
	var out []int64
	for i := 0; i < n; i++ {
		body := h.uploadPhoto(t, "p.jpg", testPhoto(t, 200+i*20, 150), 32<<10)
		out = append(out, int64(mediaOf(t, body)["id"].(float64)))
	}
	return out
}

func (h *harness) albumOrder(t *testing.T, albumID string) []int64 {
	t.Helper()
	code, body := h.do("GET", "/api/albums/"+albumID+"/items", nil)
	if code != 200 {
		t.Fatalf("album items: %d %v", code, body)
	}
	var out []int64
	for _, it := range body["items"].([]any) {
		out = append(out, int64(it.(map[string]any)["id"].(float64)))
	}
	return out
}

func sameIDs(got []int64, want ...int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestAlbumLifecycle(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)

	albumID := h.makeAlbum(t, "Crete 2019")

	code, body := h.do("GET", "/api/albums", nil)
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	albums := body["albums"].([]any)
	if len(albums) != 1 {
		t.Fatalf("%d albums, want 1", len(albums))
	}
	if albums[0].(map[string]any)["item_count"].(float64) != 0 {
		t.Fatalf("new album is not empty: %v", albums[0])
	}

	if code, _ := h.do("PATCH", "/api/albums/"+albumID, map[string]string{
		"title": "Kreta 2019", "description": "Strandurlaub",
	}); code != 204 {
		t.Fatalf("update: %d", code)
	}
	_, body = h.do("GET", "/api/albums/"+albumID, nil)
	if body["title"] != "Kreta 2019" {
		t.Fatalf("title = %v", body["title"])
	}

	// Deleting reports how many albums went, because it takes sub-albums with
	// it and the client should be able to say so.
	code, body = h.do("DELETE", "/api/albums/"+albumID, nil)
	if code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if body["deleted"].(float64) != 1 {
		t.Fatalf("deleted %v albums, want 1", body["deleted"])
	}
	if code, _ := h.do("GET", "/api/albums/"+albumID, nil); code != 404 {
		t.Fatalf("deleted album still readable: %d", code)
	}
}

func TestAlbumRejectsBlankTitle(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	if code, _ := h.do("POST", "/api/albums", map[string]string{"title": "   "}); code != 400 {
		t.Fatalf("blank title: %d, want 400", code)
	}
}

// The feature the project exists for, over HTTP.
func TestReorderAlbumOverAPI(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	albumID := h.makeAlbum(t, "Trip")
	m := h.uploadIDs(t, 4)

	code, body := h.do("POST", "/api/albums/"+albumID+"/items",
		map[string]any{"media_ids": m})
	if code != 200 || body["added"].(float64) != 4 {
		t.Fatalf("add: %d %v", code, body)
	}
	if got := h.albumOrder(t, albumID); !sameIDs(got, m...) {
		t.Fatalf("initial order = %v, want %v", got, m)
	}

	// Drag the last photo to the front: no "after", "before" is the first item.
	code, body = h.do("PATCH",
		"/api/albums/"+albumID+"/items/"+itoa(int(m[3]))+"/position",
		map[string]any{"after": nil, "before": m[0]})
	if code != 200 {
		t.Fatalf("move to front: %d %v", code, body)
	}
	if _, ok := body["sort_key"].(string); !ok {
		t.Fatalf("no sort_key returned: %v", body)
	}
	if got := h.albumOrder(t, albumID); !sameIDs(got, m[3], m[0], m[1], m[2]) {
		t.Fatalf("after move to front = %v", got)
	}

	// And into the middle.
	if code, body := h.do("PATCH",
		"/api/albums/"+albumID+"/items/"+itoa(int(m[3]))+"/position",
		map[string]any{"after": m[0], "before": m[1]}); code != 200 {
		t.Fatalf("move to middle: %d %v", code, body)
	}
	if got := h.albumOrder(t, albumID); !sameIDs(got, m[0], m[3], m[1], m[2]) {
		t.Fatalf("after move to middle = %v", got)
	}

	// And to the end: no "before".
	if code, _ := h.do("PATCH",
		"/api/albums/"+albumID+"/items/"+itoa(int(m[0]))+"/position",
		map[string]any{"after": m[2], "before": nil}); code != 200 {
		t.Fatal("move to end failed")
	}
	if got := h.albumOrder(t, albumID); !sameIDs(got, m[3], m[1], m[2], m[0]) {
		t.Fatalf("after move to end = %v", got)
	}
}

// The order has to survive a reload, or drag-and-drop is theatre.
func TestOrderSurvivesReload(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	albumID := h.makeAlbum(t, "Trip")
	m := h.uploadIDs(t, 3)
	h.do("POST", "/api/albums/"+albumID+"/items", map[string]any{"media_ids": m})

	h.do("PATCH", "/api/albums/"+albumID+"/items/"+itoa(int(m[2]))+"/position",
		map[string]any{"after": nil, "before": m[0]})
	want := h.albumOrder(t, albumID)

	// A fresh session is as close to a reload as the harness gets.
	h.do("POST", "/api/logout", nil)
	h.do("POST", "/api/login", map[string]string{
		"username": "ralf", "password": "a-good-password",
	})
	if got := h.albumOrder(t, albumID); !sameIDs(got, want...) {
		t.Fatalf("order after re-login = %v, want %v", got, want)
	}
}

func TestMoveRejectsUnknownNeighbour(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	albumID := h.makeAlbum(t, "Trip")
	m := h.uploadIDs(t, 2)
	h.do("POST", "/api/albums/"+albumID+"/items", map[string]any{"media_ids": m})

	if code, _ := h.do("PATCH",
		"/api/albums/"+albumID+"/items/"+itoa(int(m[0]))+"/position",
		map[string]any{"after": 999999, "before": nil}); code != 404 {
		t.Fatalf("unknown neighbour: %d, want 404", code)
	}
}

func TestAddReportsWhatItActuallyDid(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	albumID := h.makeAlbum(t, "Trip")
	m := h.uploadIDs(t, 3)

	h.do("POST", "/api/albums/"+albumID+"/items", map[string]any{"media_ids": m[:2]})
	// Overlapping selection: one new, two already there.
	code, body := h.do("POST", "/api/albums/"+albumID+"/items", map[string]any{"media_ids": m})
	if code != 200 {
		t.Fatalf("add: %d", code)
	}
	if body["added"].(float64) != 1 || body["requested"].(float64) != 3 {
		t.Fatalf("add reported %v, want added 1 of 3", body)
	}
}

// Removing a photo from an album must never remove the photo.
func TestRemoveFromAlbumKeepsPhotoOverAPI(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	albumID := h.makeAlbum(t, "Trip")
	m := h.uploadIDs(t, 2)
	h.do("POST", "/api/albums/"+albumID+"/items", map[string]any{"media_ids": m})

	if code, _ := h.do("DELETE",
		"/api/albums/"+albumID+"/items/"+itoa(int(m[0])), nil); code != 204 {
		t.Fatal("remove failed")
	}
	if got := h.albumOrder(t, albumID); !sameIDs(got, m[1]) {
		t.Fatalf("album = %v", got)
	}
	if code, _ := h.do("GET", "/api/media/"+itoa(int(m[0])), nil); code != 200 {
		t.Fatal("the photo was deleted along with its album membership")
	}
}

// Deleting an album must never delete photographs.
func TestDeleteAlbumKeepsPhotosOverAPI(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	albumID := h.makeAlbum(t, "Trip")
	m := h.uploadIDs(t, 2)
	h.do("POST", "/api/albums/"+albumID+"/items", map[string]any{"media_ids": m})

	h.do("DELETE", "/api/albums/"+albumID, nil)

	code, body := h.do("GET", "/api/media", nil)
	if code != 200 {
		t.Fatalf("list media: %d", code)
	}
	if len(body["items"].([]any)) != 2 {
		t.Fatalf("%d photos survived the album deletion, want 2", len(body["items"].([]any)))
	}
}

func TestAlbumCover(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	albumID := h.makeAlbum(t, "Trip")
	m := h.uploadIDs(t, 2)
	h.do("POST", "/api/albums/"+albumID+"/items", map[string]any{"media_ids": m})

	if code, _ := h.do("PUT", "/api/albums/"+albumID+"/cover",
		map[string]any{"media_id": m[1]}); code != 204 {
		t.Fatal("set cover failed")
	}
	_, body := h.do("GET", "/api/albums/"+albumID, nil)
	if int64(body["cover_media_id"].(float64)) != m[1] {
		t.Fatalf("cover = %v", body["cover_media_id"])
	}
}

func TestAlbumEndpointsRequireSession(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	albumID := h.makeAlbum(t, "Trip")
	h.do("POST", "/api/logout", nil)

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/albums"},
		{"POST", "/api/albums"},
		{"GET", "/api/albums/" + albumID},
		{"PATCH", "/api/albums/" + albumID},
		{"DELETE", "/api/albums/" + albumID},
		{"GET", "/api/albums/" + albumID + "/items"},
		{"POST", "/api/albums/" + albumID + "/items"},
		{"PATCH", "/api/albums/" + albumID + "/items/1/position"},
		{"PATCH", "/api/albums/" + albumID + "/position"},
	} {
		code, _ := h.do(tc.method, tc.path, map[string]any{})
		if code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session: %d, want 401", tc.method, tc.path, code)
		}
	}
}

// The tile order is the list order: the client draws the albums in the order
// they arrive, so this is the whole contract.
func TestReorderAlbumsOverAPI(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)

	first := h.makeAlbum(t, "Alps")
	second := h.makeAlbum(t, "Bornholm")
	third := h.makeAlbum(t, "Croatia")

	if got := h.albumTitles(t); !sameTitles(got, "Alps", "Bornholm", "Croatia") {
		t.Fatalf("albums arrive as %v", got)
	}

	code, body := h.do("PATCH", "/api/albums/"+third+"/position",
		map[string]any{"before": atoi(first)})
	if code != 204 {
		t.Fatalf("move album: %d %v", code, body)
	}
	if got := h.albumTitles(t); !sameTitles(got, "Croatia", "Alps", "Bornholm") {
		t.Fatalf("after the move: %v", got)
	}

	// Named neighbour that is the album itself: a position relative to nothing.
	if code, _ := h.do("PATCH", "/api/albums/"+second+"/position",
		map[string]any{"after": atoi(second)}); code != 400 {
		t.Errorf("moving an album next to itself: %d, want 400", code)
	}
	if code, _ := h.do("PATCH", "/api/albums/"+second+"/position",
		map[string]any{"after": 9999}); code != 404 {
		t.Errorf("unknown neighbour: %d, want 404", code)
	}
}

func (h *harness) albumTitles(t *testing.T) []string {
	t.Helper()
	code, body := h.do("GET", "/api/albums", nil)
	if code != 200 {
		t.Fatalf("list albums: %d %v", code, body)
	}
	var out []string
	for _, a := range body["albums"].([]any) {
		out = append(out, a.(map[string]any)["title"].(string))
	}
	return out
}

func sameTitles(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestUnknownAlbumIs404(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	for _, path := range []string{"/api/albums/9999", "/api/albums/9999/items"} {
		if code, _ := h.do("GET", path, nil); code != 404 {
			t.Errorf("%s: %d, want 404", path, code)
		}
	}
}

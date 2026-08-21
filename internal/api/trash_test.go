package api

import (
	"net/http"
	"testing"
)

func TestTrashLifecycleOverAPI(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	m := mediaOf(t, h.uploadPhoto(t, "holiday.jpg", testPhoto(t, 400, 300), 32<<10))
	id := itoa(int(m["id"].(float64)))

	// DELETE is the reversible step.
	if code, _ := h.do("DELETE", "/api/media/"+id, nil); code != 204 {
		t.Fatalf("trash: %d", code)
	}
	if code, _ := h.do("GET", "/api/media/"+id, nil); code != 404 {
		t.Fatalf("trashed photo still in the library: %d", code)
	}
	code, body := h.do("GET", "/api/media", nil)
	if len(body["items"].([]any)) != 0 {
		t.Fatalf("timeline still shows %d items", len(body["items"].([]any)))
	}

	code, body = h.do("GET", "/api/trash", nil)
	if code != 200 {
		t.Fatalf("trash: %d", code)
	}
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("%d items in the trash, want 1", len(items))
	}
	// The deadline travels with the item, so the client need not repeat policy.
	entry := items[0].(map[string]any)
	if entry["purge_at"] == nil || entry["deleted_at"] == nil {
		t.Fatalf("no deadline on the trashed item: %v", entry)
	}

	if code, _ := h.do("POST", "/api/media/"+id+"/restore", nil); code != 204 {
		t.Fatalf("restore: %d", code)
	}
	if code, _ := h.do("GET", "/api/media/"+id, nil); code != 200 {
		t.Fatalf("restored photo is not back: %d", code)
	}
}

// Permanent deletion must be a second, separate step. Getting a verb wrong on
// the media resource must never destroy a photograph.
func TestPurgeRequiresTrashingFirst(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	m := mediaOf(t, h.uploadPhoto(t, "safe.jpg", testPhoto(t, 300, 200), 32<<10))
	id := itoa(int(m["id"].(float64)))

	if code, _ := h.do("DELETE", "/api/media/"+id+"/purge", nil); code != 404 {
		t.Fatalf("purging an untrashed photo: %d, want 404", code)
	}
	if code, _ := h.do("GET", "/api/media/"+id, nil); code != 200 {
		t.Fatal("the photo was destroyed anyway")
	}

	h.do("DELETE", "/api/media/"+id, nil)
	if code, _ := h.do("DELETE", "/api/media/"+id+"/purge", nil); code != 204 {
		t.Fatalf("purge after trashing: %d", code)
	}
	code, body := h.do("GET", "/api/trash", nil)
	if code != 200 || len(body["items"].([]any)) != 0 {
		t.Fatalf("trash is not empty: %v", body)
	}
}

func TestEmptyTrashOverAPI(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	for i := 0; i < 3; i++ {
		m := mediaOf(t, h.uploadPhoto(t, "p.jpg", testPhoto(t, 200+i*30, 150), 32<<10))
		h.do("DELETE", "/api/media/"+itoa(int(m["id"].(float64))), nil)
	}
	code, body := h.do("POST", "/api/trash/empty", nil)
	if code != 200 {
		t.Fatalf("empty: %d", code)
	}
	if body["purged"].(float64) != 3 {
		t.Fatalf("purged %v, want 3", body["purged"])
	}
}

func TestSearchOverAPI(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	for i, name := range []string{"DSC01234.jpg", "holiday-crete.jpg", "beach.jpg"} {
		h.uploadPhoto(t, name, testPhoto(t, 200+i*40, 150), 32<<10)
	}

	code, body := h.do("GET", "/api/media?q=holiday", nil)
	if code != 200 {
		t.Fatalf("search: %d %v", code, body)
	}
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("%d hits, want 1", len(items))
	}
	if items[0].(map[string]any)["filename"] != "holiday-crete.jpg" {
		t.Fatalf("wrong hit: %v", items[0])
	}
	if body["query"] != "holiday" {
		t.Fatalf("query not echoed: %v", body["query"])
	}
}

// A search box receives whatever someone types. FTS5 syntax errors must not
// surface as failed requests.
func TestSearchSurvivesPunctuationOverAPI(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	h.uploadPhoto(t, "DSC01234.jpg", testPhoto(t, 300, 200), 32<<10)

	for _, q := range []string{`"`, `*`, `AND`, `NEAR(`, `a OR b`, `-`} {
		code, body := h.do("GET", "/api/media?q="+urlEncode(q), nil)
		if code != 200 {
			t.Errorf("query %q: %d %v", q, code, body)
		}
	}
}

func TestTrashedMediaIsNotSearchable(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	m := mediaOf(t, h.uploadPhoto(t, "findme.jpg", testPhoto(t, 300, 200), 32<<10))

	if _, body := h.do("GET", "/api/media?q=findme", nil); len(body["items"].([]any)) != 1 {
		t.Fatal("not found before trashing")
	}
	h.do("DELETE", "/api/media/"+itoa(int(m["id"].(float64))), nil)
	if _, body := h.do("GET", "/api/media?q=findme", nil); len(body["items"].([]any)) != 0 {
		t.Fatal("a trashed photo is still searchable")
	}
}

func TestTrashEndpointsRequireSession(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin(t)
	m := mediaOf(t, h.uploadPhoto(t, "p.jpg", testPhoto(t, 200, 200), 32<<10))
	id := itoa(int(m["id"].(float64)))
	h.do("POST", "/api/logout", nil)

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/trash"},
		{"POST", "/api/trash/empty"},
		{"DELETE", "/api/media/" + id},
		{"POST", "/api/media/" + id + "/restore"},
		{"DELETE", "/api/media/" + id + "/purge"},
	} {
		if code, _ := h.do(tc.method, tc.path, nil); code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session: %d, want 401", tc.method, tc.path, code)
		}
	}
}

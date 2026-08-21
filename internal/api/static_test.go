package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html":             {Data: []byte("<!doctype html><title>obscura</title>")},
		"assets/index-abc123.js": {Data: []byte("console.log(1)")},
	}
}

func get(t *testing.T, h http.Handler, path string) (*http.Response, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	resp := rec.Result()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestServesIndexAtRoot(t *testing.T) {
	resp, body := get(t, staticHandler(testAssets(), true), "/")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if !strings.Contains(body, "obscura") {
		t.Fatalf("body = %q", body)
	}
	// A cached shell means a browser keeps loading an old bundle after upgrade.
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
}

func TestServesHashedAsset(t *testing.T) {
	resp, body := get(t, staticHandler(testAssets(), true), "/assets/index-abc123.js")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if body != "console.log(1)" {
		t.Fatalf("body = %q", body)
	}
	// Fingerprinted filenames can never change content, so they cache forever.
	cc := resp.Header.Get("Cache-Control")
	if !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=") {
		t.Fatalf("Cache-Control = %q", cc)
	}
}

// Client-side routes must survive a reload or a shared link.
func TestUnknownPathFallsBackToIndex(t *testing.T) {
	h := staticHandler(testAssets(), true)
	for _, path := range []string{"/album/42", "/settings", "/deep/nested/route"} {
		resp, body := get(t, h, path)
		if resp.StatusCode != 200 {
			t.Errorf("%s: status %d, want 200", path, resp.StatusCode)
		}
		if !strings.Contains(body, "obscura") {
			t.Errorf("%s: did not serve the app shell", path)
		}
	}
}

// A mistyped API path must return the mux's JSON 404, not an HTML page a client
// would then fail to parse. The fallback is registered on "/" and the API
// routes are more specific, so the mux resolves this -- worth pinning, since a
// regression would produce very confusing client errors.
func TestAPIPathsDoNotFallBackToHTML(t *testing.T) {
	h := newServerWithAssets(t).Routes()

	resp, body := get(t, h, "/api/definitely-not-a-route")
	if strings.Contains(body, "<!doctype") || strings.Contains(body, "<title>") {
		t.Fatalf("an unknown /api path served HTML: %q", body)
	}
	if resp.StatusCode == 200 {
		t.Fatalf("unknown API path returned 200")
	}
}

func TestKnownAPIRouteStillReachable(t *testing.T) {
	h := newServerWithAssets(t).Routes()
	resp, body := get(t, h, "/api/status")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q -- the static handler shadowed the API", ct)
	}
}

// Without a frontend build the app should say so, rather than 404 in a way that
// looks like a routing bug.
func TestUnbuiltFrontendExplainsItself(t *testing.T) {
	resp, body := get(t, staticHandler(fstest.MapFS{}, false), "/")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", resp.StatusCode)
	}
	if !strings.Contains(body, "npm run build") {
		t.Fatalf("message does not say what to do: %q", body)
	}
}

// Path traversal must not escape the embedded filesystem.
func TestTraversalIsContained(t *testing.T) {
	h := staticHandler(testAssets(), true)
	for _, path := range []string{"/../secret", "/assets/../../etc/passwd"} {
		resp, body := get(t, h, path)
		if strings.Contains(body, "root:") {
			t.Fatalf("%s escaped the asset filesystem", path)
		}
		if resp.StatusCode != 200 && resp.StatusCode != 404 {
			t.Errorf("%s: unexpected status %d", path, resp.StatusCode)
		}
	}
}

// A cached service worker keeps serving an old application forever: it is the
// thing that decides what else gets cached, so it must never be stale itself.
func TestServiceWorkerIsNotCached(t *testing.T) {
	assets := testAssets()
	assets["sw.js"] = &fstest.MapFile{Data: []byte("self.addEventListener('fetch', () => {})")}

	resp, body := get(t, staticHandler(assets, true), "/sw.js")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if !strings.Contains(body, "addEventListener") {
		t.Fatalf("body = %q", body)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
	// Needed for a worker served from the root to control the whole scope.
	if resp.Header.Get("Service-Worker-Allowed") != "/" {
		t.Fatal("Service-Worker-Allowed is not set")
	}
}

func TestManifestIsServed(t *testing.T) {
	assets := testAssets()
	assets["manifest.webmanifest"] = &fstest.MapFile{Data: []byte(`{"name":"obscura"}`)}

	resp, body := get(t, staticHandler(assets, true), "/manifest.webmanifest")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if !strings.Contains(body, "obscura") {
		t.Fatalf("body = %q", body)
	}
}

// Go does not know the .webmanifest extension, so without help the manifest
// goes out as text/plain and some browsers refuse to install the app.
func TestManifestHasTheRightContentType(t *testing.T) {
	assets := testAssets()
	assets["manifest.webmanifest"] = &fstest.MapFile{Data: []byte(`{"name":"obscura"}`)}

	resp, _ := get(t, staticHandler(assets, true), "/manifest.webmanifest")
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/manifest+json") {
		t.Fatalf("Content-Type = %q, want application/manifest+json", ct)
	}
}

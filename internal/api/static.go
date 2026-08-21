package api

import (
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// assetMaxAge is how long a hashed asset may be cached.
//
// Vite fingerprints filenames, so a given URL's content can never change. The
// entry HTML is the opposite and must never be cached, or a browser keeps
// loading an old bundle after an upgrade.
const assetMaxAge = 365 * 24 * 60 * 60

func init() {
	// Go does not know this extension, so the manifest would go out as
	// text/plain -- which some browsers refuse, leaving the app uninstallable
	// for a reason that shows up nowhere obvious.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// staticHandler serves the embedded frontend.
//
// Unknown paths fall back to index.html rather than 404, so client-side routes
// survive a reload or a shared link. The fallback deliberately stops at /api/,
// which the mux has already claimed: a mistyped API path must return a JSON
// error, not an HTML page that a client would then fail to parse.
func staticHandler(assets fs.FS, built bool) http.Handler {
	if !built {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(
				"The frontend is not built into this binary.\n\n" +
					"Run `npm run build` in web/, or build the Docker image, which does it for you.\n"))
		})
	}

	files := http.FileServerFS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An unknown path under /api must not receive the app shell. A client
		// expecting JSON would try to parse HTML and fail in a way that points
		// nowhere near the actual mistake.
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such endpoint"})
			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")

		if name != "" && name != "index.html" {
			if _, err := fs.Stat(assets, name); err == nil {
				switch {
				case strings.HasPrefix(name, "assets/"):
					// Fingerprinted, therefore safe to cache for a long time.
					w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(assetMaxAge)+", immutable")
				case name == "sw.js":
					// A cached service worker would keep serving an old app
					// forever: it is the thing that decides what else is
					// cached, so it must never be stale itself.
					w.Header().Set("Cache-Control", "no-cache")
					w.Header().Set("Service-Worker-Allowed", "/")
				}
				files.ServeHTTP(w, r)
				return
			}
		}

		// Everything else is the app shell.
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	})
}

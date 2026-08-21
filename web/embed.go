// Package web embeds the built frontend into the binary.
//
// One artefact ships: there is no separate asset directory to keep in sync with
// the executable, and nothing to misconfigure in a reverse proxy.
package web

import (
	"embed"
	"io/fs"
)

// dist is produced by `npm run build`.
//
// The "all:" prefix includes dotfiles, which is what lets a committed
// dist/.gitkeep satisfy the embed directive in a fresh clone that has never run
// a Node build. Without it, go:embed fails on an empty directory and the Go
// build would depend on a Node toolchain being present.
//
//go:embed all:dist
var dist embed.FS

// Assets returns the built frontend rooted at dist.
func Assets() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}

// Built reports whether a real frontend build is embedded, as opposed to just
// the placeholder. Lets the server say so plainly instead of serving a 404 that
// looks like a routing bug.
func Built() bool {
	_, err := fs.Stat(dist, "dist/index.html")
	return err == nil
}

// Package media turns uploaded files into the derivatives obscura serves:
// thumbnails for the grid and a larger preview for the lightbox.
//
// Image processing runs libvips as a subprocess (vipsthumbnail) rather than
// through cgo bindings. That decision was measured, not assumed: a cold cgo
// build of govips took 976s against 84s for the cgo-free build, and keeping
// cgo out is what makes "GOARCH=arm64 go build" a single command with no
// toolchain. The cost is roughly 20ms of process startup per image, which is
// noise next to an import that runs for minutes.
package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Derivative sizes, in pixels on the long edge.
const (
	ThumbSize   = 256  // grid
	PreviewSize = 1600 // lightbox
)

// jpegQuality trades size against artefacts. 82 is the usual sweet spot for
// photographic content; the original is always kept anyway.
const jpegQuality = 82

// defaultTimeout bounds a single conversion. A corrupt or hostile file must not
// be able to pin a worker forever -- on a Pi there are only a couple of them.
const defaultTimeout = 2 * time.Minute

// ErrNotAnImage reports that the input could not be decoded as an image.
var ErrNotAnImage = errors.New("media: not a decodable image")

// Thumbnailer produces JPEG derivatives by piping through vipsthumbnail.
type Thumbnailer struct {
	bin     string
	timeout time.Duration
}

// NewThumbnailer locates vipsthumbnail and fails loudly if it is missing.
//
// Failing at startup rather than on the first upload is deliberate: a missing
// binary is a deployment error, and discovering it when the first photo is
// already half uploaded is a bad time to find out.
func NewThumbnailer() (*Thumbnailer, error) {
	bin, err := exec.LookPath("vipsthumbnail")
	if err != nil {
		return nil, fmt.Errorf(
			"media: vipsthumbnail not found in PATH (install the vips-tools package): %w", err)
	}
	return &Thumbnailer{bin: bin, timeout: defaultTimeout}, nil
}

// Thumbnail reads an image from src and writes a JPEG of at most size pixels on
// the long edge to dst.
//
// Everything moves through pipes, so no plaintext ever reaches the filesystem.
// libvips reads the literal filename "stdin" as its standard input, and an
// output target of ".jpg" means "write JPEG to stdout" -- both verified against
// the container image this ships with.
//
// Aspect ratio is preserved: the image is fitted inside a size x size box
// rather than cropped, so the grid never silently cuts someone's head off.
// EXIF orientation is applied automatically; --no-rotate would be the opt-out.
func (t *Thumbnailer) Thumbnail(ctx context.Context, src io.Reader, dst io.Writer, size int) error {
	if size <= 0 {
		return fmt.Errorf("media: invalid size %d", size)
	}
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	box := strconv.Itoa(size)
	cmd := exec.CommandContext(ctx, t.bin,
		"stdin",
		"--size", box+"x"+box,
		"-o", ".jpg[Q="+strconv.Itoa(jpegQuality)+",strip]",
	)
	cmd.Stdin = src
	cmd.Stdout = dst

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// The environment is inherited, which matters for TMPDIR: for images too
	// large to hold in memory libvips falls back to a memory-mapped temp file
	// there. Deployment must therefore back TMPDIR with tmpfs, or a plaintext
	// copy can land on persistent storage behind the encryption. VIPS_WARNING
	// only keeps routine warnings out of stderr so it stays useful for errors.
	cmd.Env = append(cmd.Environ(), "VIPS_WARNING=0")

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("media: vipsthumbnail timed out after %s", t.timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		// libvips reports an undecodable input as an unknown file format; that
		// is a bad upload, not a server fault, and callers need to tell the
		// difference to pick an HTTP status.
		if strings.Contains(msg, "not a known file format") ||
			strings.Contains(msg, "unable to thumbnail") {
			return fmt.Errorf("%w: %s", ErrNotAnImage, firstLine(msg))
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("media: vipsthumbnail: %s", firstLine(msg))
	}
	return nil
}

// StripsMetadata reports whether derivatives keep EXIF.
//
// They do not: the [strip] flag above removes it. The metadata belongs in the
// encrypted database, not in a JPEG that gets served to a browser -- otherwise
// GPS coordinates would travel with every thumbnail.
func (t *Thumbnailer) StripsMetadata() bool { return true }

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

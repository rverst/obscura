# obscura

A self-hosted photo and video viewer that stores everything encrypted at rest and
is small enough to run on a Raspberry Pi.

Inspired by [photoview](https://github.com/photoview/photoview), but with a
different set of goals: upload instead of directory scanning, drag-and-drop
custom ordering inside albums, and encryption that covers the metadata as well
as the files.

> **Status: early.** Milestones 1 (authentication, key management), 2 (upload,
> thumbnails, metadata), 3 (gallery UI), 4 (albums with drag-and-drop ordering)
> and 5 (video) are done. Search, the trash and passkeys are still ahead.

## Security model

The threat this protects against is a **stolen disk, a leaked backup, or a
break-in while nobody is logged in**. It is explicitly *not* designed to stop an
attacker who has root on the running machine during an active session.

- One random Data Encryption Key (DEK) encrypts the metadata database and all
  blobs. It is never written to disk in the clear.
- The DEK is wrapped once per unlock method — password, recovery code, and later
  passkey — following the LUKS key-slot model. Changing the password rewraps a
  single row instead of re-encrypting the library.
- The DEK is derived at login and lives **only in memory**. Restarting the
  server therefore locks the library until someone logs in again. That is
  intended behaviour.
- `photos.db` is fully encrypted, so filenames, EXIF data and album titles never
  hit the disk in readable form. `auth.db` is deliberately *not* encrypted — it
  has to be readable before any key exists — and holds nothing but salts, KDF
  parameters and wrapped DEK copies.

**If both the password and the recovery code are lost, the library is
unrecoverable.** There is no back door; that is the point.

Encryption at rest is not a backup. Keep one.

## Requirements

Docker. Nothing else — the build is cgo-free, so no Go toolchain, no system
libraries and no cross-compiler are needed, even when targeting a Pi.

## Usage

```sh
make help        # list all targets

make test        # gofmt, vet and the test suite, inside Docker
make build       # runtime image for the local platform
make build-pi    # cross-build for a Raspberry Pi (linux/arm64)

make up          # start the stack, http://127.0.0.1:8080
make logs
make down
```

First run needs a one-time setup call, which returns the recovery code exactly
once:

```sh
curl -sX POST http://127.0.0.1:8080/api/setup \
  -H 'Content-Type: application/json' \
  -d '{"username":"you","password":"a-good-password"}'
```

Write that recovery code down and store it away from the server.

### Albums

Albums are views onto the library, not containers: deleting an album never
deletes photographs, and removing a photo from one leaves the photo alone.

Each album keeps its own order. Dragging a photo writes exactly one row, whatever
the album's size, because a position is a string that can always be subdivided
rather than an integer that would renumber everything after it. Ten thousand
photos appended to an album produce a four-character key.

Uploading and reordering both live in an album's **edit mode**. Outside it the
album is for looking at: nothing is draggable and there is no drop target.

### Video

Videos are stored exactly as uploaded and never transcoded — the Pi 5 has no
hardware encoder at all, so re-encoding would run in software and occupy the
machine for minutes per clip. Playback streams the original with range requests,
so seeking works without downloading the file first.

ffmpeg is used only to pull a poster frame and read the duration, and it is
optional: without it videos still upload, are stored and play, they simply get
no still image.

### Uploading

Uploads are chunked and resumable, because a multi-gigabyte video pushed over
wifi to a Pi that dies at 90% is the normal case rather than the edge case.

1. **Ask first.** `POST /api/media/check` with the file's SHA-256 (hex) answers
   whether that content is already stored. The client hashes locally, so
   re-adding a folder costs nothing.
2. **Open an upload.** `POST /api/uploads` with `{"filename":…,"size":…}`
   returns an `upload_id`.
3. **Send chunks.** `PATCH /api/uploads/{id}` with the raw bytes as the body and
   an `Upload-Offset` header saying where they belong. The response reports the
   new offset.
4. **Finish.** `POST /api/uploads/{id}/finish` derives the thumbnails, reads
   EXIF and returns the media record.

If a connection drops, `GET /api/uploads/{id}` reports how many bytes the server
holds — resume from exactly there. Resending an overlap is safe; leaving a gap
is refused with a 409.

Media is served at `/api/media/{id}/thumb`, `/preview` and `/original`. The
original answers range requests, which is what video scrubbing will use in M5.

### Resource use

Measured on arm64 against a running container, with 24 MP photos:

| State | RSS |
|---|---|
| Locked (nobody logged in) | 15 MB |
| Unlocked | 81 MB |
| After importing 5 × 24 MP | 81 MB |

The step on unlock is the WebAssembly SQLite runtime being instantiated — what a
cgo-free build costs. Importing adds no memory at all, because image processing
happens in a short-lived subprocess rather than in-process.

The image is 232 MB. ffmpeg accounts for 78 MB of that and libvips for most of
the rest; the Go binary and the embedded frontend are 15 MB together, and the
frontend itself is 45 kB, 16 kB gzipped.

Almost all of the ffmpeg weight is encoder libraries this program never invokes,
but Alpine has no decode-only build and compiling one from source would
reintroduce a very long build. Leaving ffmpeg out is supported and saves the
78 MB, at the cost of poster frames.

### Running it for real

`docker-compose.yml` binds to loopback and passes `-dev`, which drops the
cookie's `Secure` flag so the app is reachable over plain http on localhost.

For an actual deployment, put a TLS reverse proxy (Caddy, nginx) in front and
drop `-dev`. obscura speaks plain http and does not terminate TLS itself —
without a proxy the password travels over the network in the clear.

### Fast inner loop

`make test-native` and `make run-native` skip Docker and use a local Go
toolchain. Docker remains the authoritative path: it is what runs on the Pi.

## Language

The codebase is English throughout — comments, error strings, log messages and
HTTP responses. English is the application's primary language for now; a
translation layer can be added later if it is needed.

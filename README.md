# obscura

A self-hosted photo and video viewer that stores everything encrypted at rest and
is small enough to run on a Raspberry Pi.

Inspired by [photoview](https://github.com/photoview/photoview), but with a
different set of goals: upload instead of directory scanning, drag-and-drop
custom ordering inside albums, and encryption that covers the metadata as well
as the files.

> **Status: early.** Milestones 1 to 10 are done: authentication and key
> management, upload, the gallery, nested albums with drag-and-drop ordering,
> video, search, trash, favourites, PWA support, passkeys and a deployment
> setup.

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

The target has to run a 64-bit OS; on a Pi that means a Pi 4, 5 or Zero 2 W.

## Usage

```sh
make help        # list all targets

make test        # gofmt, vet and the test suite, inside Docker
make build       # runtime image for the local platform
make build-pi    # cross-build for a Raspberry Pi (linux/arm64)

make up          # start the stack, http://localhost:8080
make logs
make down
```

First run needs a one-time setup call, which returns the recovery code exactly
once:

```sh
curl -sX POST http://localhost:8080/api/setup \
  -H 'Content-Type: application/json' \
  -d '{"username":"you","password":"a-good-password"}'
```

Write that recovery code down and store it away from the server.

Reach the app at `http://localhost:8080`, not `http://127.0.0.1:8080`. Both go
to the same place, but passkeys need a hostname — see below.

### Getting around

Albums are the home screen, shown as tiles. There is no sidebar: search lives in
the top bar because it applies to the whole library wherever you are, and the
account button holds the trash, the unlock methods and the lock.

**All photos** is a fixed tile at the top level — the whole library is the one
place a given photo is certain to be.

The favourites filter sits in the top bar and applies to every view, including
search and the lightbox. A filter that some screens honour and others quietly
ignore reads as broken rather than scoped. Press `f` in the lightbox to mark,
`i` to show or hide the details panel.

The lightbox carries three marks and nothing else — favourite, details, close —
and they step out of the way, along with the arrows and the cursor, a couple of
seconds after the pointer stops moving. Any movement brings them back. What is on
screen is a photograph, not a toolbar. Deleting is deliberately not among them:
it lives in the grid behind **Edit**, because a delete one key away from the
arrow keys is a delete waiting to happen by accident.

### Albums

Albums are views onto the library, not containers: deleting an album never
deletes photographs, and removing a photo from one leaves the photo alone.

Albums nest — Croatia holds 2024 and 2025 — and a breadcrumb walks back out. One
drag does two jobs: dropped on the middle of another tile an album nests inside
it, dropped near a tile's left or right edge it takes a place beside it, and an
insertion bar says which before the button comes up. A move that would make an
album its own ancestor is refused, and the refusal is shown rather than
swallowed, because from the outside a rejected drag just looks like nothing
happened. Deleting an album takes its sub-albums with it and says how many went.

Dropping an album on a **breadcrumb** moves it back out, to that level — the way
back up that nesting-by-drag otherwise lacks. Sub-album tiles carry the same move
as a button, for touch screens and for anyone who would rather not drag.

Levels are ordered independently, and a new album lands at the end of its own
level rather than in the middle of an arrangement someone made by hand.

A tile shows the whole subtree's count and a cover the server works out: the one
pinned by hand if there is one, otherwise the album's newest photo, otherwise the
cover of its first sub-album — so a parent that holds nothing itself still shows
a picture, and shows the picture from the tile directly beneath it rather than
one from somewhere further down. The lightbox's detail panel pins the open photo
as the cover, and offers itself again to hand the choice back, so the override is
not a door that only shuts once.

Each album keeps its own order. Dragging a photo writes exactly one row, whatever
the album's size, because a position is a string that can always be subdivided
rather than an integer that would renumber everything after it. Ten thousand
photos appended to an album produce a four-character key. Albums use the same
kind of key, but a reorder renumbers the whole level: a level holds tens of
albums, not thousands, and every album that predates the column shares the same
empty key, so there is frequently no gap to subdivide.

An album is filled two ways, both in edit mode: uploading files, and
**Add from library** for photographs already here. Without the second, removing a
photo from an album was a one-way door — the only way back in was to upload the
file again, which is why that × felt more dangerous than the trash, the one place
that has always had a way back.

Making an album, uploading, reordering and deleting all live in **edit mode** —
at the top level as well, which is why the top level has an **Edit** button of
its own. Outside it the library is for looking at: nothing is draggable, there is
no drop target, and there is no tile for making an album standing in front of the
real ones.

### Addresses

Every view has a URL, so Back works, a reload lands where it left off, and a
photograph can be linked to:

```
/                  the library          /a/{id}      an album
/all               the timeline         /trash       the trash
/search?q=…        results              …/p/{id}     a photo open on top of any of them
```

Navigation is one-directional: an action calls the router, the router sets the
state. Nothing watches the state in order to write history, so the two cannot
argue. Opening a photo pushes an entry and closing it pops that entry, while
stepping between photos replaces — walking two hundred frames should leave one
thing to go back to, not two hundred. A link followed while the library is locked
survives the login screen without being stashed anywhere: nothing on the way in
touches the address bar.

Deep links have always been served: the static handler falls back to the app
shell for every path outside `/api/`.

### Video

Videos are stored exactly as uploaded and never transcoded — the Pi 5 has no
hardware encoder at all, so re-encoding would run in software and occupy the
machine for minutes per clip. Playback streams the original with range requests,
so seeking works without downloading the file first.

ffmpeg is used only to pull a poster frame and read the duration, and it is
optional: without it videos still upload, are stored and play, they simply get
no still image.

### Deleting

Deleting is two steps. The first moves a photo to the trash, where it stays
recoverable for 30 days; the second, behind a separate endpoint, is permanent. A
self-hosted library often has no backup behind it, so a mis-click needs to stay
reversible long enough for someone to notice.

Trashing a photo does not disturb album curation: it leaves every album while it
is in the trash and returns to its old position when restored.

Expired items are cleared after a login rather than on a timer — the key exists
only while someone is signed in, so there is no moment at which a background job
could open the database by itself.

### Search

Filenames and camera names, as an ordinary full-text query inside the encrypted
database. That is what encrypting the whole file rather than individual columns
bought: no blind indexes, and no downloading the library to filter it in the
browser.

### Passkeys

A passkey here is not a second factor. It is a third way to unwrap the key that
decrypts the library, alongside the password and the recovery code, and it works
through the WebAuthn PRF extension: an ordinary passkey signature differs on
every use and is useless as key material, while PRF returns the same 32 bytes
for the same salt every time. The browser runs that through HKDF and the server
wraps the data key with the result — one more key slot, no photo re-encrypted.

Add one under **Unlock methods**. Nothing is stored until the device has
actually produced a key: PRF support is still patchy across browsers, platforms
and authenticators, and a passkey that signs in but cannot decrypt would be
worse than none at all. A device that cannot do it says so, and nothing is
saved. The password always keeps working.

Two things have to hold before the button appears:

- **A hostname, not an address.** WebAuthn requires the relying party ID to be a
  domain, so `http://localhost:8080` works and `http://127.0.0.1:8080` does not.
  Same server; on the second one the app reports passkeys as unavailable.
- **https, or localhost.** Browsers refuse WebAuthn outside a secure context.

For a real deployment, pass `-origin https://photos.example.com`. Without it the
relying party takes its address from each request, which keeps the app working
under whatever hostname it is reached by — but then verifying the origin against
a value from that same request proves nothing on its own. What still holds is
the browser's own rule that the relying party ID must match the real origin, so
a forged `Host` header breaks the ceremony rather than redirecting it. Pinning
restores the full check.

Removing a passkey deletes its slot and its credential together. The password
and recovery slots cannot be removed at all — they are the way back in.

### Installing on a phone

The app is installable. Thumbnails are cached for offline browsing, bounded to a
few hundred so it cannot exhaust a phone's storage quota. The API is never
cached — a stale library listing would be worse than being offline.

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

`docker-compose.yml` in the repository root is for development: it binds to
loopback and passes `-dev`, which drops the cookie's `Secure` flag so the app
works over plain http on localhost. Do not expose it.

`deploy/` holds the real thing — obscura behind Caddy, which gets and renews
the certificate on its own:

```sh
cd deploy
cp env.example .env          # then set OBSCURA_DOMAIN
docker compose -f docker-compose.prod.yml up -d
```

The domain has to resolve to the machine before you start, because Caddy proves
control of it to obtain the certificate.

Three things in there are load-bearing rather than decorative:

- **obscura publishes no ports.** The only way in is through Caddy, so there is
  no plain-http door left open on the host.
- **`-origin` is pinned** to the domain. Without it the passkey relying party
  comes from the request, and checking an origin against a value taken from
  that same request proves nothing.
- **Compression is an explicit list.** Photos, videos and every derivative are
  already compressed; running them through gzip again spends Pi CPU to make
  them slightly larger.

If the host already runs Traefik, `docker-compose.traefik.yml` replaces Caddy
with labels on the obscura container. It expects an external `proxy` network,
entrypoints `http` and `https`, a resolver `le` and two file-provider entries
(`https-redirect`, `secure-tls`) — adjust the labels to your names:

```sh
docker compose -f docker-compose.traefik.yml up -d
```

If you would rather use nginx, the requirements are: terminate TLS, forward to
port 8080, set `X-Forwarded-Proto` and `X-Forwarded-Host`, and do not impose a
request body limit or a short upload timeout — a video arrives in 8 MB chunks
and can take a while.

### Backups

Encryption is not a backup. A disk that dies takes an encrypted library with it
just as thoroughly as a plaintext one.

```sh
make backup                              # development stack, into ./backups
deploy/backup.sh -f docker-compose.prod.yml /mnt/backup     # production
```

The script stops the container before copying, and that is the whole point.
obscura runs its databases in WAL mode, so recent commits live in `auth.db-wal`
and `photos.db-wal` until they are checkpointed. **A copy of `auth.db` taken
from a running server opens cleanly, looks plausible, and is missing whatever
happened most recently.** A clean shutdown checkpoints the log and leaves one
self-contained file; the downtime is a few seconds.

Two things the archive does not do for you:

1. **It is not encrypted as it stands.** The photos and the metadata database
   inside it are, but `auth.db` is deliberately plaintext — it has to be
   readable before any key exists. Encrypt each snapshot before it leaves the
   machine, with a key unrelated to your obscura password:

   ```sh
   age -r age1... obscura-20260822T083143Z.tar > obscura-20260822T083143Z.tar.age
   ```

   That also closes a smaller leak. obscura encrypts the metadata database
   deterministically, so someone holding several *plain* snapshots can see which
   4 KB pages changed between them — not their contents, and the photos
   themselves are unaffected. Encrypting each snapshot separately makes them
   indistinguishable.

2. **Keep the recovery code somewhere else entirely.** Beside the backup it is
   not a recovery code, it is the key taped to the door.

Restoring is extracting the archive into an empty volume and starting the
server; it wants no special handling. Do it once, on purpose, before you need
to — a backup nobody has ever restored is a guess.

### Known issues

**Which passkey provider takes the prompt.** obscura fetches each passkey
challenge before the button is pressed, so the click goes straight to the
browser. That matters where a browser extension such as Bitwarden or 1Password
handles passkeys itself, as in Chrome. The extension only gets the prompt while
the click still counts as a user gesture, and a network round trip in between
can end that. If the wrong provider still answers, the browser's passkey
settings and the extension's own settings decide who is offered.

Safari always shows the system dialog. A password manager registered with
macOS, such as Bitwarden, can take the passkey from there, and that is expected.
Whether the passkey can unlock anything is a separate question, because that
depends on the browser passing PRF through. In testing, Safari returned no PRF
result at all, neither through Bitwarden nor through Apple Passwords, and the
[PRF test page](https://www.passkeyprf.com/) agreed. Chrome is the browser to
use for enrolling. Bitwarden's PRF support appears to depend on it.

One gap remains. Some browsers only switch PRF on when a passkey is created and
need a second prompt to evaluate it. That second prompt follows the first
rather than a click, so on those browsers it may still reach the system
provider.

Worth telling the two failure modes apart: if the wrong provider takes the
prompt you get a passkey saved in the wrong place. If the provider that answers
cannot do the PRF extension, obscura refuses the enrollment outright and says
so. Nothing is saved on the server, and your password is untouched. The
provider will usually have stored the passkey by then. It is useless here and
can be deleted there.

### Fast inner loop

`make test-native` and `make run-native` skip Docker and use a local Go
toolchain. Docker remains the authoritative path: it is what runs on the Pi.

## Language

The codebase is English throughout — comments, error strings, log messages and
HTTP responses. English is the application's primary language for now; a
translation layer can be added later if it is needed.

## Licence

MIT — see [LICENSE](LICENSE).

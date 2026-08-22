#!/bin/sh
# Take a consistent snapshot of an obscura data volume.
#
# Why this is a script and not a cp:
#
# obscura runs its databases in WAL mode. Recent commits live in auth.db-wal
# and photos.db-wal until they are checkpointed, so copying auth.db on its own
# gives you a file that opens cleanly, looks plausible, and is missing whatever
# happened most recently. Copying all three files while the server is running is
# no better -- they are three files changing independently, and nothing stops
# the copy landing between two of them.
#
# So this stops the container first. A clean shutdown checkpoints the WAL and
# leaves one self-contained database file behind. Downtime is a few seconds.
#
# The archive is NOT compressed. Almost all of it is encrypted blobs, which are
# incompressible by construction; gzip would spend a lot of Pi CPU to make the
# file marginally larger.

set -eu

usage() {
	cat >&2 <<EOF
usage: $0 [-f COMPOSE_FILE] [-s SERVICE] [-v VOLUME] DESTINATION_DIR

Stops the service, snapshots its data volume into DESTINATION_DIR, starts it
again. The resulting archive is unencrypted -- see the note it prints.

  -f  compose file (default: docker-compose.prod.yml)
  -s  service name (default: obscura)
  -v  volume name  (default: taken from the compose project)
EOF
	exit 2
}

compose_file="docker-compose.prod.yml"
service="obscura"
volume=""

while getopts "f:s:v:h" opt; do
	case "$opt" in
	f) compose_file=$OPTARG ;;
	s) service=$OPTARG ;;
	v) volume=$OPTARG ;;
	*) usage ;;
	esac
done
shift $((OPTIND - 1))
[ $# -eq 1 ] || usage

dest=$1
[ -d "$dest" ] || { echo "no such directory: $dest" >&2; exit 1; }
dest=$(cd "$dest" && pwd)

compose="docker compose -f $compose_file"

# Ask the container what it actually mounts at /data, rather than reconstructing
# the name. Compose prefixes volumes with the project, which comes from the
# directory name and can be overridden -- guessing it gets this wrong in exactly
# the setups where a wrong answer is expensive.
if [ -z "$volume" ]; then
	container=$($compose ps -aq "$service" 2>/dev/null | head -1)
	if [ -n "$container" ]; then
		volume=$(docker inspect -f \
			'{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' \
			"$container" 2>/dev/null || true)
	fi
fi
if [ -z "$volume" ]; then
	echo "cannot work out which volume $service uses -- pass -v" >&2
	exit 1
fi
docker volume inspect "$volume" >/dev/null 2>&1 || {
	echo "volume not found: $volume (pass -v)" >&2
	exit 1
}
echo "data volume: $volume"

stamp=$(date -u +%Y%m%dT%H%M%SZ)
archive="obscura-$stamp.tar"

# Whatever happens below, do not leave the service stopped.
running=$($compose ps -q "$service" 2>/dev/null || true)
restart() {
	if [ -n "$running" ]; then
		echo "starting $service again"
		$compose start "$service" >/dev/null
	fi
}
trap restart EXIT INT TERM

if [ -n "$running" ]; then
	echo "stopping $service so the write-ahead logs are checkpointed"
	$compose stop "$service" >/dev/null
fi

echo "writing $dest/$archive"
docker run --rm \
	-v "$volume":/data:ro \
	-v "$dest":/backup \
	alpine:3.22 \
	tar cf "/backup/$archive" -C /data .

# A backup nobody has ever read is a guess. Check that the archive contains the
# file without which nothing else in it can be decrypted.
if ! tar tf "$dest/$archive" | grep -q '^\./auth\.db$'; then
	echo "the archive has no auth.db -- refusing to call this a backup" >&2
	rm -f "$dest/$archive"
	exit 1
fi
if tar tf "$dest/$archive" | grep -q -- '-wal$'; then
	echo "warning: a write-ahead log survived the shutdown; the snapshot may be" >&2
	echo "         mid-transaction. Check that $service really stopped." >&2
fi

size=$(du -h "$dest/$archive" | cut -f1)
cat <<EOF

Wrote $dest/$archive ($size)

This archive is NOT encrypted as it stands. The photos and the metadata
database inside it are, but auth.db is deliberately plaintext, and anyone who
also has your password or recovery code can open the whole library from it.

Two things to do with it:

  1. Encrypt it before it leaves this machine, with a key that is not derived
     from your obscura password:

         age -r age1... "$archive" > "$archive.age"

     This also closes a smaller leak: obscura encrypts the metadata database
     deterministically, so someone holding several plain snapshots can see
     which pages changed between them -- not their contents. Encrypting each
     snapshot separately makes them indistinguishable.

  2. Keep your recovery code somewhere else entirely. Stored beside the
     backup it is not a recovery code, it is the key taped to the door.
EOF

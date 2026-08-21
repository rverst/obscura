-- photos.db: fully encrypted (adiantum VFS + page checksums).
--
-- Because the whole file is encrypted, individual columns need no special
-- treatment: filenames, EXIF and album titles are plaintext as far as SQL is
-- concerned, but land encrypted on disk. That is why ORDER BY, JOIN and FTS5
-- all work normally -- no blind indexes required.

CREATE TABLE users(
    id         INTEGER PRIMARY KEY,
    username   TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE media(
    id            INTEGER PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    kind          TEXT NOT NULL CHECK(kind IN ('photo','video')),

    -- SHA-256 over the PLAINTEXT. Enables dedup and "already uploaded?" checks
    -- without decrypting anything. It sits inside the encrypted database, so it
    -- reveals nothing to the outside.
    sha256        BLOB NOT NULL,

    orig_filename TEXT NOT NULL,
    bytes         INTEGER NOT NULL,
    mime          TEXT NOT NULL DEFAULT '',

    width         INTEGER,
    height        INTEGER,
    duration_ms   INTEGER,          -- video only
    orientation   INTEGER,          -- EXIF orientation, 1..8

    taken_at      INTEGER,          -- unix seconds, from EXIF
    camera_make   TEXT,
    camera_model  TEXT,
    lens          TEXT,
    iso           INTEGER,
    f_number      REAL,
    exposure      TEXT,
    focal_len     REAL,
    gps_lat       REAL,
    gps_lon       REAL,

    -- Blob IDs; the payload itself lives DARE-encrypted under blobs/.
    blob_orig     TEXT NOT NULL,
    blob_preview  TEXT,
    blob_thumb    TEXT,

    created_at    INTEGER NOT NULL,
    deleted_at    INTEGER            -- trash; NULL means active
) STRICT;

CREATE INDEX idx_media_taken   ON media(user_id, taken_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_media_deleted ON media(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE UNIQUE INDEX idx_media_sha ON media(user_id, sha256);

CREATE TABLE albums(
    id            INTEGER PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title         TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    cover_media_id INTEGER REFERENCES media(id) ON DELETE SET NULL,
    created_at    INTEGER NOT NULL,
    deleted_at    INTEGER
) STRICT;

CREATE INDEX idx_albums_user ON albums(user_id) WHERE deleted_at IS NULL;

-- Custom ordering within an album.
--
-- sort_key is deliberately TEXT and not INTEGER: with fractional indexing a
-- reorder inserts a key "between" its two neighbours ("a5" between "a" and
-- "b"). That makes every move exactly ONE row update -- whether the album holds
-- 10 photos or 10,000. With consecutive integers it would be thousands in the
-- worst case.
CREATE TABLE album_items(
    album_id  INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    media_id  INTEGER NOT NULL REFERENCES media(id)  ON DELETE CASCADE,
    sort_key  TEXT    NOT NULL,
    added_at  INTEGER NOT NULL,
    PRIMARY KEY(album_id, media_id)
) STRICT;

-- Covers the main query: "all photos of an album in custom order".
CREATE INDEX idx_album_items_order ON album_items(album_id, sort_key);

CREATE TABLE tags(
    id      INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    UNIQUE(user_id, name)
) STRICT;

CREATE TABLE media_tags(
    media_id INTEGER NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    tag_id   INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
    PRIMARY KEY(media_id, tag_id)
) STRICT;

-- Full-text search over the fields that hang directly off media.
--
-- External-content table: the index does not store the values twice but points
-- back into media by rowid. The triggers below keep it in sync. Tags and album
-- titles are deliberately NOT in here -- they live in other tables and get
-- folded in during M6, when search is actually built.
CREATE VIRTUAL TABLE media_fts USING fts5(
    orig_filename,
    camera_make,
    camera_model,
    content='media',
    content_rowid='id'
);

CREATE TRIGGER media_fts_insert AFTER INSERT ON media BEGIN
    INSERT INTO media_fts(rowid, orig_filename, camera_make, camera_model)
    VALUES (new.id, new.orig_filename, new.camera_make, new.camera_model);
END;

CREATE TRIGGER media_fts_delete AFTER DELETE ON media BEGIN
    INSERT INTO media_fts(media_fts, rowid, orig_filename, camera_make, camera_model)
    VALUES ('delete', old.id, old.orig_filename, old.camera_make, old.camera_model);
END;

CREATE TRIGGER media_fts_update AFTER UPDATE ON media BEGIN
    INSERT INTO media_fts(media_fts, rowid, orig_filename, camera_make, camera_model)
    VALUES ('delete', old.id, old.orig_filename, old.camera_make, old.camera_model);
    INSERT INTO media_fts(rowid, orig_filename, camera_make, camera_model)
    VALUES (new.id, new.orig_filename, new.camera_make, new.camera_model);
END;

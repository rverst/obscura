package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// applyUpTo runs migrations in order and stops after the named one, so a test
// can build a database as an older release left it.
func applyUpTo(t *testing.T, db *sql.DB, set, last string) {
	t.Helper()
	if _, err := db.Exec(
		`CREATE TABLE IF NOT EXISTS schema_migrations(
			name    TEXT PRIMARY KEY,
			applied INTEGER NOT NULL
		)`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFS.ReadDir("migrations/" + set)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		script, err := migrationFS.ReadFile("migrations/" + set + "/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if err := applyMigration(db, e.Name(), string(script)); err != nil {
			t.Fatalf("apply %s: %v", e.Name(), err)
		}
		if e.Name() == last {
			return
		}
	}
	t.Fatalf("no migration named %s in %s", last, set)
}

// Upgrading must not cost anyone their account.
//
// 0002 drops webauthn_credentials and alters users, and it runs against a real
// database the moment someone pulls a new image. A mistake there is not a
// failed test on a laptop, it is a library nobody can open again -- so the
// upgrade is exercised against a populated database rather than an empty one.
func TestAuthUpgradeKeepsExistingAccounts(t *testing.T) {
	db, err := OpenPlain(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// A database as the first release left it.
	applyUpTo(t, db, SetAuth, "0001_key_slots.sql")

	if _, err := db.Exec(
		`INSERT INTO users(id, username, created_at) VALUES(1, 'ralf', 1700000000)`); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"password", "recovery"} {
		if _, err := db.Exec(`
			INSERT INTO key_slots(user_id, kind, label, kdf_params, wrapped_dek, created_at)
			VALUES(1, ?, '', '{"t":3}', ?, 1700000000)`,
			kind, []byte("wrapped-"+kind)); err != nil {
			t.Fatal(err)
		}
	}

	if err := Migrate(db, SetAuth); err != nil {
		t.Fatalf("upgrade failed: %v", err)
	}

	var username string
	if err := db.QueryRow(`SELECT username FROM users WHERE id = 1`).Scan(&username); err != nil {
		t.Fatalf("the account did not survive the upgrade: %v", err)
	}
	if username != "ralf" {
		t.Fatalf("username = %q", username)
	}

	// The wrapped keys are the account. Losing them is losing the library.
	for _, kind := range []string{"password", "recovery"} {
		var wrapped []byte
		if err := db.QueryRow(
			`SELECT wrapped_dek FROM key_slots WHERE kind = ?`, kind).Scan(&wrapped); err != nil {
			t.Fatalf("the %s slot did not survive: %v", kind, err)
		}
		if string(wrapped) != "wrapped-"+kind {
			t.Fatalf("the %s slot changed: %q", kind, wrapped)
		}
	}

	// And the upgrade actually did its work.
	var handle []byte
	if err := db.QueryRow(`SELECT webauthn_handle FROM users WHERE id = 1`).Scan(&handle); err != nil {
		t.Fatalf("webauthn_handle is missing: %v", err)
	}
	if handle != nil {
		t.Fatalf("an existing account gained a handle it never registered: %x", handle)
	}
	if _, err := db.Exec(
		`INSERT INTO webauthn_credentials(credential_id, user_id, record, created_at)
		 VALUES(x'01', 1, '{}', 1700000000)`); err != nil {
		t.Fatalf("the rebuilt credential table is not usable: %v", err)
	}
}

// Running the whole set twice must be a no-op, or a restart would drop the
// credential table 0002 recreates -- taking every registered passkey with it.
func TestMigrationsAreNotRepeated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	db, err := OpenPlain(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := Migrate(db, SetAuth); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO users(id, username, created_at) VALUES(1, 'ralf', 1700000000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO webauthn_credentials(credential_id, user_id, record, created_at)
		 VALUES(x'01', 1, '{"id":"AQ"}', 1700000000)`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(db, SetAuth); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM webauthn_credentials`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d credentials after re-running the migrations, want 1", n)
	}
}

// The same guarantee for the photo database.
//
// 0002 alters both media and albums. Those tables hold the library itself, so
// an upgrade that dropped a column, or silently rewrote one, would be the worst
// possible bug: the photos are still on disk, encrypted, and nothing can list
// them any more.
func TestPhotosUpgradeKeepsExistingLibrary(t *testing.T) {
	db, err := OpenEncrypted(filepath.Join(t.TempDir(), "photos.db"), keyOf(9))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	applyUpTo(t, db, SetPhotos, "0001_schema.sql")

	if _, err := db.Exec(
		`INSERT INTO users(id, username, created_at) VALUES(1, 'ralf', 1700000000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO media(id, user_id, kind, sha256, orig_filename, bytes, blob_orig, created_at)
		VALUES(1, 1, 'photo', x'aabb', 'holiday.jpg', 4242, 'blob-1', 1700000000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO albums(id, user_id, title, description, created_at)
		VALUES(1, 1, 'Croatia', '', 1700000000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO album_items(album_id, media_id, sort_key, added_at)
		VALUES(1, 1, 'a0', 1700000000)`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(db, SetPhotos); err != nil {
		t.Fatalf("upgrade failed: %v", err)
	}

	// The photo, its blob pointer and its album membership all survive.
	var (
		filename string
		blob     string
		bytes    int64
	)
	if err := db.QueryRow(
		`SELECT orig_filename, blob_orig, bytes FROM media WHERE id = 1`).
		Scan(&filename, &blob, &bytes); err != nil {
		t.Fatalf("the photo did not survive: %v", err)
	}
	if filename != "holiday.jpg" || blob != "blob-1" || bytes != 4242 {
		t.Fatalf("the record changed: %s %s %d", filename, blob, bytes)
	}
	var sortKey string
	if err := db.QueryRow(
		`SELECT sort_key FROM album_items WHERE album_id = 1 AND media_id = 1`).
		Scan(&sortKey); err != nil {
		t.Fatalf("album membership did not survive: %v", err)
	}
	if sortKey != "a0" {
		t.Fatalf("the curated position changed to %q", sortKey)
	}

	// The new columns exist and default to "not set" for existing rows -- an
	// upgrade must not silently favourite the whole library or nest it.
	var favorited, parent sql.NullInt64
	if err := db.QueryRow(`SELECT favorited_at FROM media WHERE id = 1`).Scan(&favorited); err != nil {
		t.Fatalf("favorited_at is missing: %v", err)
	}
	if favorited.Valid {
		t.Fatal("an existing photo came out of the upgrade marked as a favourite")
	}
	if err := db.QueryRow(`SELECT parent_id FROM albums WHERE id = 1`).Scan(&parent); err != nil {
		t.Fatalf("parent_id is missing: %v", err)
	}
	if parent.Valid {
		t.Fatal("an existing album came out of the upgrade nested under something")
	}

	// And the full-text index still answers, which is the part most likely to
	// break quietly when its content table is altered.
	var hits int
	if err := db.QueryRow(
		`SELECT count(*) FROM media_fts WHERE media_fts MATCH '"holiday"*'`).Scan(&hits); err != nil {
		t.Fatalf("search broke in the upgrade: %v", err)
	}
	if hits != 1 {
		t.Fatalf("%d search hits after the upgrade, want 1", hits)
	}
}

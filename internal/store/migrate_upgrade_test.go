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

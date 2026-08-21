package store

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func keyOf(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b + byte(i)
	}
	return k
}

func openMigrated(t *testing.T, path string, key []byte) *sql.DB {
	t.Helper()
	db, err := OpenEncrypted(path, key)
	if err != nil {
		t.Fatalf("OpenEncrypted: %v", err)
	}
	if err := Migrate(db, SetPhotos); err != nil {
		db.Close()
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

func TestPhotosDBIsEncrypted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photos.db")
	key := keyOf(1)

	db := openMigrated(t, path, key)
	if _, err := db.Exec(
		`INSERT INTO users(id, username, created_at) VALUES(1,'ralf',unixepoch())`); err != nil {
		t.Fatal(err)
	}
	const secret = "BEACH-HOLIDAY-CRETE-2019.ARW"
	if _, err := db.Exec(`
		INSERT INTO media(user_id,kind,sha256,orig_filename,bytes,camera_model,blob_orig,created_at)
		VALUES(1,'photo',x'00',?,1234,'ILCE-7M4','blob-1',unixepoch())`, secret); err != nil {
		t.Fatal(err)
	}
	db.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The actual proof that metadata is encrypted along with everything else.
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("the filename is stored in plaintext in photos.db")
	}
	if bytes.Contains(raw, []byte("ILCE-7M4")) {
		t.Fatal("the camera model is stored in plaintext in photos.db")
	}
	if bytes.HasPrefix(raw, []byte("SQLite format 3")) {
		t.Fatal("the SQLite header is unencrypted")
	}
}

func TestWrongKeyIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photos.db")

	db := openMigrated(t, path, keyOf(1))
	db.Close()

	bad, err := OpenEncrypted(path, keyOf(99))
	if err != nil {
		return // rejected at open time -- also fine
	}
	defer bad.Close()
	if err := bad.QueryRow(`SELECT count(*) FROM users`).Scan(new(int)); err == nil {
		t.Fatal("SECURITY PROBLEM: the database could be read with the wrong key")
	}
}

func TestUnreadableWithoutKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photos.db")

	db := openMigrated(t, path, keyOf(1))
	db.Close()

	plain, err := OpenPlain(path)
	if err != nil {
		return
	}
	defer plain.Close()
	if err := plain.QueryRow(`SELECT count(*) FROM users`).Scan(new(int)); err == nil {
		t.Fatal("SECURITY PROBLEM: the database could be read without a key")
	}
}

func TestDataSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photos.db")
	key := keyOf(7)

	db := openMigrated(t, path, key)
	if _, err := db.Exec(
		`INSERT INTO users(id,username,created_at) VALUES(1,'ralf',unixepoch())`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	again := openMigrated(t, path, key) // migrating again must be a no-op
	defer again.Close()
	var name string
	if err := again.QueryRow(`SELECT username FROM users WHERE id=1`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "ralf" {
		t.Fatalf("username = %q", name)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photos.db")
	key := keyOf(3)

	db, err := OpenEncrypted(path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for i := 0; i < 3; i++ {
		if err := Migrate(db, SetPhotos); err != nil {
			t.Fatalf("Migrate run %d: %v", i+1, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d migration records after 3 runs, want 1", n)
	}
}

func TestChecksumsEnabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photos.db")

	db := openMigrated(t, path, keyOf(5))
	defer db.Close()

	// cksumvfs reserves 8 bytes at the end of each page for the checksum.
	var reserved int
	if err := db.QueryRow(`PRAGMA main.reserve_bytes`).Scan(&reserved); err != nil {
		t.Skipf("reserve_bytes not queryable: %v", err)
	}
	if reserved != 8 {
		t.Fatalf("reserve_bytes = %d, want 8 -- checksums are not active", reserved)
	}
}

func TestFTS5TriggersKeepIndexInSync(t *testing.T) {
	dir := t.TempDir()
	db := openMigrated(t, filepath.Join(dir, "photos.db"), keyOf(11))
	defer db.Close()

	if _, err := db.Exec(
		`INSERT INTO users(id,username,created_at) VALUES(1,'ralf',unixepoch())`); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`
		INSERT INTO media(user_id,kind,sha256,orig_filename,bytes,camera_make,camera_model,blob_orig,created_at)
		VALUES(1,'photo',x'01','DSC01234.ARW',100,'SONY','ILCE-7M4','blob-1',unixepoch())
		RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	count := func(q string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(
			`SELECT count(*) FROM media_fts WHERE media_fts MATCH ?`, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if got := count(`DSC01234*`); got != 1 {
		t.Fatalf("after INSERT: %d hits, want 1", got)
	}
	if got := count(`ILCE*`); got != 1 {
		t.Fatalf("camera model not indexed: %d hits", got)
	}

	if _, err := db.Exec(
		`UPDATE media SET orig_filename='HOLIDAY99.ARW' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if got := count(`DSC01234*`); got != 0 {
		t.Fatalf("after UPDATE the old name still yields %d hits", got)
	}
	if got := count(`HOLIDAY99*`); got != 1 {
		t.Fatalf("after UPDATE the new name yields %d hits", got)
	}

	if _, err := db.Exec(`DELETE FROM media WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if got := count(`HOLIDAY99*`); got != 0 {
		t.Fatalf("after DELETE the index still holds %d hits", got)
	}
}

func TestAuthDBMigration(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenPlain(filepath.Join(dir, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db, SetAuth); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO users(id,username,created_at) VALUES(1,'ralf',unixepoch())`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO key_slots(user_id,kind,wrapped_dek,created_at)
		VALUES(1,'password',x'deadbeef',unixepoch())`); err != nil {
		t.Fatal(err)
	}
	// The CHECK constraint must reject unknown slot kinds.
	if _, err := db.Exec(`
		INSERT INTO key_slots(user_id,kind,wrapped_dek,created_at)
		VALUES(1,'nonsense',x'00',unixepoch())`); err == nil {
		t.Fatal("an unknown slot kind was accepted")
	}
}

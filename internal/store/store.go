// Package store wraps database access.
//
// obscura uses two separate SQLite files:
//
//   - auth.db   unencrypted, tiny. Holds only the key slots (salts, KDF
//     parameters, wrapped DEK copies). It must be readable BEFORE any
//     key exists -- otherwise login would be a chicken-and-egg
//     problem. Without the password its contents are worthless.
//
//   - photos.db encrypted (adiantum VFS) with a key derived from the DEK.
//     Holds all metadata: filenames, EXIF, albums, tags.
package store

import (
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum" // registers the "adiantum" VFS
)

//go:embed migrations
var migrationFS embed.FS

// Migration sets. auth.db and photos.db have separate schemas and separate
// version counters.
const (
	SetAuth   = "auth"
	SetPhotos = "photos"
)

// Pragmas applied to every connection.
//
// temp_store=memory is not tuning here but a security measure: otherwise
// SQLite spills temporary files to disk, and those contain plaintext pages
// from the database we just went to the trouble of encrypting.
var connPragmas = []string{
	`PRAGMA temp_store = memory`,
	`PRAGMA foreign_keys = on`,
	`PRAGMA busy_timeout = 5000`,
	`PRAGMA synchronous = normal`,
}

// OpenEncrypted opens the encrypted metadata database.
//
// dbKey is the database key derived from the DEK (crypto.DBKey), not the DEK
// itself.
func OpenEncrypted(path string, dbKey []byte) (*sql.DB, error) {
	if len(dbKey) == 0 {
		return nil, fmt.Errorf("store: empty database key")
	}
	// The key is set via PRAGMA rather than as a URI parameter: as a URI
	// parameter it would be readable from elsewhere in the process through
	// vfs.Filename.URIParameters.
	hexKey := hex.EncodeToString(dbKey)

	db, err := driver.Open("file:"+path+"?vfs=adiantum&_txlock=immediate",
		func(c *sqlite3.Conn) error {
			if err := c.Exec(fmt.Sprintf(`PRAGMA hexkey=%q;`, hexKey)); err != nil {
				return fmt.Errorf("set key: %w", err)
			}
			// Page checksums. The checksum is computed over the plaintext page
			// and then encrypted along with it -- so without the key no valid
			// page can be forged. Idempotent; the VACUUM it needs runs only on
			// the very first activation.
			if err := c.EnableChecksums("main"); err != nil {
				return fmt.Errorf("enable checksums: %w", err)
			}
			// With ncruces, FTS5 is not a compile flag but an extension that
			// has to be registered per connection.
			if err := fts5.Register(c); err != nil {
				return fmt.Errorf("register fts5: %w", err)
			}
			for _, p := range connPragmas {
				if err := c.Exec(p + ";"); err != nil {
					return fmt.Errorf("%s: %w", p, err)
				}
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := prepare(db, path); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// OpenPlain opens an unencrypted database (auth.db).
func OpenPlain(path string) (*sql.DB, error) {
	db, err := driver.Open("file:"+path+"?_txlock=immediate",
		func(c *sqlite3.Conn) error {
			for _, p := range connPragmas {
				if err := c.Exec(p + ";"); err != nil {
					return fmt.Errorf("%s: %w", p, err)
				}
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := prepare(db, path); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func prepare(db *sql.DB, path string) error {
	// WAL allows readers to run alongside a writer. Must happen outside the
	// per-connection callback because it is a durable property of the file,
	// not of the connection.
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		return fmt.Errorf("store: enable WAL on %s: %w", path, err)
	}
	// SQLite tolerates exactly one writer. A pool of writers would only produce
	// SQLITE_BUSY; readers still run concurrently thanks to WAL.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	return nil
}

// --- Migrations -------------------------------------------------------------

// Migrate brings the schema of set (SetAuth or SetPhotos) up to date. Applied
// migrations are recorded in schema_migrations and never run twice.
func Migrate(db *sql.DB, set string) error {
	if _, err := db.Exec(
		`CREATE TABLE IF NOT EXISTS schema_migrations(
			name    TEXT PRIMARY KEY,
			applied INTEGER NOT NULL
		)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	dir := "migrations/" + set
	entries, err := migrationFS.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("store: read migrations %q: %w", set, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // 0001_..., 0002_...: lexicographic == chronological

	for _, name := range names {
		var done int
		switch err := db.QueryRow(
			`SELECT 1 FROM schema_migrations WHERE name = ?`, name).Scan(&done); err {
		case nil:
			continue // already applied
		case sql.ErrNoRows:
		default:
			return fmt.Errorf("store: check migration status %s: %w", name, err)
		}

		sqlBytes, err := migrationFS.ReadFile(dir + "/" + name)
		if err != nil {
			return fmt.Errorf("store: read %s: %w", name, err)
		}
		if err := applyMigration(db, name, string(sqlBytes)); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(db *sql.DB, name, script string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin transaction for %s: %w", name, err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(script); err != nil {
		return fmt.Errorf("store: migration %s: %w", name, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations(name, applied) VALUES(?, unixepoch())`, name); err != nil {
		return fmt.Errorf("store: record %s: %w", name, err)
	}
	return tx.Commit()
}

//go:build unix

package main

import "syscall"

// tightenUmask makes every file this process creates owner-only (0600).
//
// SQLite creates its database, -wal and -shm files with 0644 by default, which
// leaves them readable by any local user. photos.db is encrypted, so that would
// mostly be untidy -- but auth.db is deliberately not, and handing its wrapped
// DEK and salts to any local account makes an offline attack on the password
// possible. The data directory is 0700 as well; this is the second layer.
func tightenUmask() {
	syscall.Umask(0o077)
}

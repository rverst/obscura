//go:build !unix

package main

// tightenUmask is a no-op on platforms without a umask. obscura targets Linux
// (Raspberry Pi) and macOS for development; this exists so the build does not
// break elsewhere.
func tightenUmask() {}

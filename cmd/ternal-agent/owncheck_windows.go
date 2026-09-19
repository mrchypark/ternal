//go:build windows

package main

// The agent never runs elevated repairs on Windows; ownership repair is a
// no-op there and verification is best-effort true.
func rootOwned(path string) bool { return false }

func fileOwnedBy(path string, uid, gid int) bool { return true }

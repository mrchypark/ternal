//go:build !windows

package main

import (
	"os"
	"syscall"
)

func rootOwned(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	n, ok := st.Sys().(*syscall.Stat_t)
	return ok && n.Uid == 0
}

func fileOwnedBy(path string, uid, gid int) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	n, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(n.Uid) == uid && int(n.Gid) == gid
}

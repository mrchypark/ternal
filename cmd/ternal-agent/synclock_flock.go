//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireSyncLock takes a kernel-held advisory lock on path.ternal-lock.
// The kernel releases it on any process death, so crashes leave no residue.
// ponytail: flock over a lockfile; no PID bookkeeping, no reaper.
func acquireSyncLock(path string) (func(), error) {
	lockPath := path + ".ternal-lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("authorized_keys synchronization is already running")
	}
	return func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}, nil
}

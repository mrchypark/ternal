//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// maxSyncLockAge bounds how long a sync may hold the lock. Windows has no
// kernel-released flock, so a lock older than this is treated as crashed
// residue and stolen. Concurrent writers only race a fetch-and-rename of
// the same snapshot, which the generation check already serializes.
const maxSyncLockAge = 10 * time.Minute

type syncLockOwner struct {
	PID       int   `json:"pid"`
	StartedAt int64 `json:"started_at"`
}

func acquireSyncLock(path string) (func(), error) {
	lockPath := path + ".ternal-lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(lockPath, 0700); err != nil {
		if !os.IsExist(err) {
			return nil, err
		}
		if !staleSyncLock(lockPath) {
			return nil, fmt.Errorf("authorized_keys synchronization is already running")
		}
		if err := os.RemoveAll(lockPath); err != nil {
			return nil, err
		}
		if err := os.Mkdir(lockPath, 0700); err != nil {
			return nil, err
		}
	}
	owner, _ := json.Marshal(syncLockOwner{PID: os.Getpid(), StartedAt: time.Now().Unix()})
	if err := os.WriteFile(filepath.Join(lockPath, "owner.json"), owner, 0600); err != nil {
		_ = os.RemoveAll(lockPath)
		return nil, err
	}
	return func() { _ = os.RemoveAll(lockPath) }, nil
}

func staleSyncLock(lockPath string) bool {
	data, err := os.ReadFile(filepath.Join(lockPath, "owner.json"))
	if err != nil {
		return true
	}
	var owner syncLockOwner
	if json.Unmarshal(data, &owner) != nil {
		return true
	}
	return time.Since(time.Unix(owner.StartedAt, 0)) > maxSyncLockAge
}

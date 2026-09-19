//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSyncLockChildHolder(t *testing.T) {
	if os.Getenv("TERNAL_TEST_LOCK_HOLDER") != "1" {
		return
	}
	unlock, err := acquireSyncLock(os.Getenv("TERNAL_TEST_LOCK_PATH"))
	if err != nil {
		os.Exit(2)
	}
	defer unlock()
	if _, err := os.Stdout.WriteString("ready\n"); err != nil {
		os.Exit(3)
	}
	select {}
}

func TestSyncLockReleasedOnCrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorized_keys")
	child := exec.Command(os.Args[0], "-test.run=TestSyncLockChildHolder")
	child.Env = append(os.Environ(), "TERNAL_TEST_LOCK_HOLDER=1", "TERNAL_TEST_LOCK_PATH="+path)
	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make([]byte, 6)
	n := 0
	for n < len(ready) {
		m, err := out.Read(ready[n:])
		n += m
		if err != nil {
			_ = child.Wait()
			t.Fatalf("lock holder exited early: %v", err)
		}
	}
	if string(ready) != "ready\n" {
		_ = child.Wait()
		t.Fatalf("unexpected holder output %q", ready)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	unlock, err := acquireSyncLock(path)
	if err != nil {
		t.Fatalf("lock survived crashed holder: %v", err)
	}
	unlock()
}

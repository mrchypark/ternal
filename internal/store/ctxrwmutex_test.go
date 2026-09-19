package store

import (
	"context"
	"testing"
	"time"
)

// TestCtxRWMutexCancelledWriterWakesReaders reproduces the missed-wakeup bug:
// a reader queues behind the last waiting writer; when that writer's context is
// cancelled it must broadcast, or the reader sleeps forever with no owner.
func TestCtxRWMutexCancelledWriterWakesReaders(t *testing.T) {
	var m ctxRWMutex
	writerCtx, cancelWriter := context.WithCancel(context.Background())

	// Hold the write lock so the writer queues behind it.
	if !m.Lock(context.Background()) {
		t.Fatal("initial lock")
	}
	writerDone := make(chan bool, 1)
	go func() { writerDone <- m.Lock(writerCtx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.Lock()
		waiting := m.wwaiting
		m.mu.Unlock()
		if waiting == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("writer never queued")
		}
		time.Sleep(time.Millisecond)
	}

	// Queue a reader behind the waiting writer, then release the lock and
	// cancel the writer while it is the only waiter.
	readerDone := make(chan bool, 1)
	go func() { readerDone <- m.RLock(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	cancelWriter()
	m.Unlock()

	if acquired := <-writerDone; acquired {
		t.Fatal("cancelled writer acquired the lock")
	}
	select {
	case acquired := <-readerDone:
		if !acquired {
			t.Fatal("reader failed to acquire")
		}
		m.RUnlock()
	case <-time.After(2 * time.Second):
		t.Fatal("reader stayed asleep after the last waiting writer was cancelled")
	}
}

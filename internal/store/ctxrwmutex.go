package store

import (
	"context"
	"sync"
)

// ctxRWMutex is an RWMutex whose acquisition honors context cancellation.
// Waiters queue on a broadcast channel holding no resources, so a cancelled
// wait releases nothing and disturbs no owner. Waiting writers take
// precedence over new readers to avoid writer starvation.
// ponytail: bespoke primitive, but sync.Mutex cannot cancel acquisition
// and the reaper alternative releases locks it does not own.
type ctxRWMutex struct {
	mu       sync.Mutex
	readers  int
	writer   bool
	wwaiting int
	notify   chan struct{}
}

func (m *ctxRWMutex) broadcast() {
	close(m.notify)
	m.notify = make(chan struct{})
}

func (m *ctxRWMutex) RLock(ctx context.Context) bool {
	for {
		m.mu.Lock()
		if m.notify == nil {
			m.notify = make(chan struct{})
		}
		if !m.writer && m.wwaiting == 0 {
			m.readers++
			m.mu.Unlock()
			return true
		}
		ch := m.notify
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return false
		}
	}
}

func (m *ctxRWMutex) RUnlock() {
	m.mu.Lock()
	m.readers--
	if m.readers == 0 {
		m.broadcast()
	}
	m.mu.Unlock()
}

func (m *ctxRWMutex) Lock(ctx context.Context) bool {
	m.mu.Lock()
	if m.notify == nil {
		m.notify = make(chan struct{})
	}
	if !m.writer && m.readers == 0 {
		m.writer = true
		m.mu.Unlock()
		return true
	}
	m.wwaiting++
	for {
		ch := m.notify
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			m.mu.Lock()
			m.wwaiting--
			// A cancelled writer releases no lock, so queued readers wait on
			// wwaiting alone; the last one out must broadcast or they sleep
			// forever with no owner.
			if m.wwaiting == 0 {
				m.broadcast()
			}
			m.mu.Unlock()
			return false
		}
		m.mu.Lock()
		if !m.writer && m.readers == 0 {
			m.writer = true
			m.wwaiting--
			m.mu.Unlock()
			return true
		}
	}
}

func (m *ctxRWMutex) Unlock() {
	m.mu.Lock()
	m.writer = false
	m.broadcast()
	m.mu.Unlock()
}

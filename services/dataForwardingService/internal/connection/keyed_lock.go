package connection

import (
	"context"
	"sync"
)

type keyedLockEntry struct {
	semaphore chan struct{}
	refs      int
}

type keyedLocker struct {
	mu      sync.Mutex
	entries map[string]*keyedLockEntry
}

func newKeyedLocker() *keyedLocker {
	return &keyedLocker{entries: make(map[string]*keyedLockEntry)}
}

func (l *keyedLocker) Lock(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	entry := l.entries[key]
	if entry == nil {
		entry = &keyedLockEntry{semaphore: make(chan struct{}, 1)}
		l.entries[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	select {
	case entry.semaphore <- struct{}{}:
		return func() {
			<-entry.semaphore
			l.releaseReference(key, entry)
		}, nil
	case <-ctx.Done():
		l.releaseReference(key, entry)
		return nil, ctx.Err()
	}
}

func (l *keyedLocker) releaseReference(key string, entry *keyedLockEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry.refs--
	if entry.refs == 0 {
		delete(l.entries, key)
	}
}

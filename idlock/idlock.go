// Package idlock provides a per-key mutex so operations on the same container id
// serialize while different ids proceed in parallel. It is shared by the HTTP
// mutation handlers (C1) and the supervisor (P3) so a user action and a
// supervision restart on the same container can never race (spec §6).
package idlock

import "sync"

// KeyedMutex hands out one mutex per key, created on first use.
type KeyedMutex struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func New() *KeyedMutex {
	return &KeyedMutex{m: map[string]*sync.Mutex{}}
}

// Lock acquires the mutex for key and returns its unlock function. Usage:
//
//	unlock := k.Lock(id)
//	defer unlock()
func (k *KeyedMutex) Lock(key string) func() {
	k.mu.Lock()
	mu, ok := k.m[key]
	if !ok {
		mu = &sync.Mutex{}
		k.m[key] = mu
	}
	k.mu.Unlock()

	mu.Lock()
	return mu.Unlock
}

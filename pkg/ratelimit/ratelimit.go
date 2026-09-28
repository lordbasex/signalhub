// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

// Package ratelimit keeps one token bucket per key (for example, per
// client IP).
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Keyed allows perMinute events per key, with bursts of up to perMinute.
// Idle keys are forgotten so memory does not grow forever.
type Keyed struct {
	limit rate.Limit
	burst int
	idle  time.Duration
	now   func() time.Time

	mu        sync.Mutex
	entries   map[string]*entry
	lastSweep time.Time
}

type entry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// New builds a Keyed limiter. perMinute <= 0 disables limiting.
func New(perMinute int) *Keyed {
	return newWithClock(perMinute, time.Now)
}

func newWithClock(perMinute int, now func() time.Time) *Keyed {
	k := &Keyed{
		limit:   rate.Inf,
		idle:    10 * time.Minute,
		now:     now,
		entries: make(map[string]*entry),
	}
	if perMinute > 0 {
		k.limit = rate.Every(time.Minute / time.Duration(perMinute))
		k.burst = perMinute
	}
	return k
}

// Allow reports whether one more event for key is allowed now.
func (k *Keyed) Allow(key string) bool {
	if k.limit == rate.Inf {
		return true
	}
	now := k.now()
	k.mu.Lock()
	defer k.mu.Unlock()
	if now.Sub(k.lastSweep) > time.Minute {
		for key, e := range k.entries {
			if now.Sub(e.lastSeen) > k.idle {
				delete(k.entries, key)
			}
		}
		k.lastSweep = now
	}
	e, ok := k.entries[key]
	if !ok {
		e = &entry{limiter: rate.NewLimiter(k.limit, k.burst)}
		k.entries[key] = e
	}
	e.lastSeen = now
	return e.limiter.AllowN(now, 1)
}

// Len returns how many keys are tracked (for tests and metrics).
func (k *Keyed) Len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.entries)
}

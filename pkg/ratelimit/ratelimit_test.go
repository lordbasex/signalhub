// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

package ratelimit

import (
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

func TestBurstThenRefill(t *testing.T) {
	clk := &clock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	k := newWithClock(5, clk.Now)
	for i := 0; i < 5; i++ {
		if !k.Allow("1.2.3.4") {
			t.Fatalf("attempt %d rejected", i+1)
		}
	}
	if k.Allow("1.2.3.4") {
		t.Fatal("6th attempt in the same minute allowed")
	}
	if !k.Allow("5.6.7.8") {
		t.Fatal("another IP was limited")
	}
	clk.Advance(12 * time.Second) // one token every 60s/5
	if !k.Allow("1.2.3.4") {
		t.Fatal("token not refilled")
	}
}

func TestIdleKeysAreForgotten(t *testing.T) {
	clk := &clock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	k := newWithClock(5, clk.Now)
	k.Allow("a")
	k.Allow("b")
	clk.Advance(11 * time.Minute)
	k.Allow("c")
	if k.Len() != 1 {
		t.Fatalf("tracked %d keys, want 1", k.Len())
	}
}

func TestDisabled(t *testing.T) {
	k := New(0)
	for i := 0; i < 100; i++ {
		if !k.Allow("x") {
			t.Fatal("disabled limiter rejected")
		}
	}
}

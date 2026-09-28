// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

package repositories

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lordbasex/signalhub/internal/models"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestMemory() (*Memory, *fakeClock) {
	clk := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return NewMemory(clk.Now), clk
}

func pairing(code, owner string, expires time.Time) models.Pairing {
	return models.Pairing{Code: code, App: "app", DeviceID: "dev", OwnerPeerID: owner, ExpiresAt: expires}
}

func TestPairingIsSingleUse(t *testing.T) {
	ctx := context.Background()
	m, clk := newTestMemory()
	if err := m.SavePairing(ctx, pairing("111111111", "owner", clk.Now().Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := m.TakePairing(ctx, "111111111"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.TakePairing(ctx, "111111111"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second take: got %v, want ErrNotFound", err)
	}
}

func TestTakePairingIsAtomic(t *testing.T) {
	ctx := context.Background()
	m, clk := newTestMemory()
	_ = m.SavePairing(ctx, pairing("222222222", "owner", clk.Now().Add(time.Minute)))
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.TakePairing(ctx, "222222222"); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("code redeemed %d times, want 1", wins.Load())
	}
}

func TestExpiredPairing(t *testing.T) {
	ctx := context.Background()
	m, clk := newTestMemory()
	_ = m.SavePairing(ctx, pairing("333333333", "owner", clk.Now().Add(time.Minute)))
	clk.Advance(time.Minute)
	if _, err := m.TakePairing(ctx, "333333333"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestSavePairingConflict(t *testing.T) {
	ctx := context.Background()
	m, clk := newTestMemory()
	_ = m.SavePairing(ctx, pairing("444444444", "a", clk.Now().Add(time.Minute)))
	if err := m.SavePairing(ctx, pairing("444444444", "b", clk.Now().Add(time.Minute))); !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v, want ErrConflict", err)
	}
	// An expired holder does not block reuse of the code.
	clk.Advance(2 * time.Minute)
	if err := m.SavePairing(ctx, pairing("444444444", "b", clk.Now().Add(time.Minute))); err != nil {
		t.Fatalf("reuse after expiry: %v", err)
	}
}

func TestPurgeExpired(t *testing.T) {
	ctx := context.Background()
	m, clk := newTestMemory()
	_ = m.SavePairing(ctx, pairing("555555555", "a", clk.Now().Add(time.Minute)))
	_ = m.SavePairing(ctx, pairing("666666666", "b", clk.Now().Add(time.Hour)))
	clk.Advance(2 * time.Minute)
	if n := m.PurgeExpired(); n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
	if _, err := m.TakePairing(ctx, "666666666"); err != nil {
		t.Fatalf("live code was purged: %v", err)
	}
}

func TestDeletePairingsByOwner(t *testing.T) {
	ctx := context.Background()
	m, clk := newTestMemory()
	_ = m.SavePairing(ctx, pairing("777777777", "a", clk.Now().Add(time.Minute)))
	_ = m.SavePairing(ctx, pairing("888888888", "b", clk.Now().Add(time.Minute)))
	_ = m.DeletePairingsByOwner(ctx, "a")
	if _, err := m.TakePairing(ctx, "777777777"); !errors.Is(err, ErrNotFound) {
		t.Fatal("owner code survived")
	}
	if _, err := m.TakePairing(ctx, "888888888"); err != nil {
		t.Fatal("other owner code was deleted")
	}
}

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	m, _ := newTestMemory()

	if _, err := m.AttachPeer(ctx, "owner", "p1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("attach before the owner session exists should fail")
	}
	s, err := m.EnsureOwnerSession(ctx, "app", "owner", "S1")
	if err != nil || s.ID != "S1" || len(s.Peers) != 0 {
		t.Fatalf("ensure: %+v %v", s, err)
	}
	// Idempotent: the existing session is returned, the new ID ignored.
	if s, _ := m.EnsureOwnerSession(ctx, "app", "owner", "S2"); s.ID != "S1" {
		t.Fatalf("second ensure created %q", s.ID)
	}
	if _, err := m.EnsureOwnerSession(ctx, "other-app", "owner", "S2"); !errors.Is(err, ErrConflict) {
		t.Fatal("owner switched app")
	}

	s, err = m.AttachPeer(ctx, "owner", "p1")
	if err != nil || s.ID != "S1" || len(s.Peers) != 1 {
		t.Fatalf("first attach: %+v %v", s, err)
	}
	s, err = m.AttachPeer(ctx, "owner", "p2")
	if err != nil || len(s.Peers) != 2 {
		t.Fatalf("second attach: %+v %v", s, err)
	}
	if _, err := m.AttachPeer(ctx, "owner", "p1"); !errors.Is(err, ErrConflict) {
		t.Fatal("peer attached twice")
	}
	if _, err := m.AttachPeer(ctx, "owner", "owner"); !errors.Is(err, ErrConflict) {
		t.Fatal("owner attached to itself")
	}
	if _, err := m.EnsureOwnerSession(ctx, "app", "p1", "S3"); !errors.Is(err, ErrConflict) {
		t.Fatal("a member became owner of a second session")
	}
	if _, err := m.AttachPeer(ctx, "p1", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatal("a member was treated as an owner")
	}

	// A member leaves: the session survives.
	before, wasOwner, err := m.DetachPeer(ctx, "p1")
	if err != nil || wasOwner || !before.Has("p1") {
		t.Fatalf("detach member: %+v %v %v", before, wasOwner, err)
	}
	if s, _ := m.SessionByPeer(ctx, "owner"); s.Has("p1") {
		t.Fatal("p1 still in session")
	}

	// The owner leaves: the whole session is gone.
	before, wasOwner, err = m.DetachPeer(ctx, "owner")
	if err != nil || !wasOwner || len(before.Peers) != 1 {
		t.Fatalf("detach owner: %+v %v %v", before, wasOwner, err)
	}
	for _, p := range []string{"owner", "p2"} {
		if _, err := m.SessionByPeer(ctx, p); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s still indexed", p)
		}
	}
	if _, _, err := m.DetachPeer(ctx, "owner"); !errors.Is(err, ErrNotFound) {
		t.Fatal("second detach should be ErrNotFound")
	}
}

func TestSessionByPeerReturnsCopy(t *testing.T) {
	ctx := context.Background()
	m, _ := newTestMemory()
	_, _ = m.EnsureOwnerSession(ctx, "app", "owner", "S1")
	_, _ = m.AttachPeer(ctx, "owner", "p1")
	s, _ := m.SessionByPeer(ctx, "owner")
	s.Peers[0] = "mutated"
	if s2, _ := m.SessionByPeer(ctx, "owner"); s2.Peers[0] != "p1" {
		t.Fatal("caller mutated repository state")
	}
}

func room(id, owner, session string, public bool, created time.Time) models.Room {
	return models.Room{ID: id, App: "app", SessionID: session, OwnerPeerID: owner, Public: public, Meta: []byte(`{"n":1}`), CreatedAt: created}
}

func TestRooms(t *testing.T) {
	ctx := context.Background()
	m, clk := newTestMemory()
	_, _ = m.EnsureOwnerSession(ctx, "app", "owner", "S1")
	_, _ = m.EnsureOwnerSession(ctx, "app", "owner2", "S2")
	t0 := clk.Now()

	if err := m.SaveRoom(ctx, room("R1", "owner", "S1", true, t0.Add(time.Second)), 2); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveRoom(ctx, room("R1", "owner2", "S2", true, t0), 2); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate room id accepted")
	}
	if err := m.SaveRoom(ctx, room("R2", "owner", "S1", false, t0), 2); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveRoom(ctx, room("R3", "owner", "S1", true, t0), 2); !errors.Is(err, ErrConflict) {
		t.Fatal("room limit not enforced")
	}
	if err := m.SaveRoom(ctx, room("R4", "owner", "S2", true, t0), 2); !errors.Is(err, ErrNotFound) {
		t.Fatal("room saved into a session the owner does not own")
	}
	if err := m.SaveRoom(ctx, room("R5", "owner2", "S2", true, t0), 2); err != nil {
		t.Fatal(err)
	}

	// Only public rooms, oldest first.
	rooms, _ := m.PublicRooms(ctx, "app")
	if len(rooms) != 2 || rooms[0].ID != "R5" || rooms[1].ID != "R1" {
		t.Fatalf("public rooms: %+v", rooms)
	}
	if rooms, _ := m.PublicRooms(ctx, "other"); len(rooms) != 0 {
		t.Fatal("rooms leaked across apps")
	}

	// Only the owner may update or delete.
	if err := m.UpdateRoomMeta(ctx, "R1", "owner2", []byte(`{}`)); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign update accepted")
	}
	if err := m.UpdateRoomMeta(ctx, "R1", "owner", []byte(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	if r, _ := m.GetRoom(ctx, "R1"); string(r.Meta) != `{"n":2}` {
		t.Fatalf("meta = %s", r.Meta)
	}
	if err := m.DeleteRoom(ctx, "R1", "owner2"); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign delete accepted")
	}
	if err := m.DeleteRoom(ctx, "R1", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetRoom(ctx, "R1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted room still readable")
	}

	// When the owner leaves, its remaining rooms go away.
	_, _, _ = m.DetachPeer(ctx, "owner")
	if _, err := m.GetRoom(ctx, "R2"); !errors.Is(err, ErrNotFound) {
		t.Fatal("room survived its owner")
	}
	if _, err := m.GetRoom(ctx, "R5"); err != nil {
		t.Fatal("another owner's room was deleted")
	}
}

func TestRoomMetaIsCopied(t *testing.T) {
	ctx := context.Background()
	m, clk := newTestMemory()
	_, _ = m.EnsureOwnerSession(ctx, "app", "owner", "S1")
	r := room("R1", "owner", "S1", true, clk.Now())
	_ = m.SaveRoom(ctx, r, 1)
	r.Meta[0] = 'X'
	got, _ := m.GetRoom(ctx, "R1")
	got.Meta[1] = 'Y'
	if again, _ := m.GetRoom(ctx, "R1"); string(again.Meta) != `{"n":1}` {
		t.Fatalf("stored meta was mutated: %s", again.Meta)
	}
}

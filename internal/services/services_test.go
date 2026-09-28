// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

package services

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lordbasex/signalhub/internal/repositories"
)

const (
	testApp    = "go-link"
	testDevice = "7f3c2a10-1b2c-4d3e-8f90-a1b2c3d4e5f6"
	// testSecret is a well-formed device_secret (43 base64url characters).
	testSecret = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

// codeSeq returns the given codes in order, to force collisions.
func codeSeq(codes ...string) func() (string, error) {
	var mu sync.Mutex
	return func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		c := codes[0]
		if len(codes) > 1 {
			codes = codes[1:]
		}
		return c, nil
	}
}

type fixture struct {
	repo     *repositories.Memory
	clk      *clock
	pairing  *PairingService
	sessions *SessionService
	rooms    *RoomService
}

func newFixture(cfg PairingConfig) *fixture {
	clk := &clock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	repo := repositories.NewMemory(clk.Now)
	cfg.Now = clk.Now
	return &fixture{
		repo:     repo,
		clk:      clk,
		pairing:  NewPairingService(repo, cfg),
		sessions: NewSessionService(repo),
		rooms:    NewRoomService(repo, RoomConfig{Now: clk.Now, MaxRoomsPerSession: 2}),
	}
}

func TestRegisterAndClaim(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{NewCode: codeSeq("113134323")})
	code, err := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if code != "113 134 323" {
		t.Fatalf("code = %q", code)
	}
	sess, err := f.pairing.Claim(ctx, "browser", testApp, code) // formatted input is accepted
	if err != nil {
		t.Fatal(err)
	}
	if sess.OwnerPeerID != "device" || !slices.Equal(sess.Peers, []string{"browser"}) || len(sess.ID) != 32 {
		t.Fatalf("unexpected session %+v", sess)
	}
}

func TestClaimTwice(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{NewCode: codeSeq("113134323")})
	code, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	if _, err := f.pairing.Claim(ctx, "b1", testApp, code); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pairing.Claim(ctx, "b2", testApp, code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("got %v, want ErrInvalidCode", err)
	}
}

func TestClaimExpired(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{TTL: time.Minute})
	code, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	f.clk.Advance(time.Minute)
	if _, err := f.pairing.Claim(ctx, "b1", testApp, code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("got %v, want ErrInvalidCode", err)
	}
}

func TestClaimFromOtherApp(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{})
	code, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	if _, err := f.pairing.Claim(ctx, "b1", "dialer", code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("got %v, want ErrInvalidCode", err)
	}
}

func TestClaimOwnCode(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{})
	code, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	// The device already owns a session, so it is rejected before the
	// code is consumed.
	if _, err := f.pairing.Claim(ctx, "device", testApp, code); !errors.Is(err, ErrAlreadyInSession) {
		t.Fatalf("got %v, want ErrAlreadyInSession", err)
	}
	if _, err := f.pairing.Claim(ctx, "b1", testApp, code); err != nil {
		t.Fatalf("code was burned: %v", err)
	}
}

func TestClaimMalformed(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{})
	for _, in := range []string{"", "12345", "abcdefghi", "1234567890"} {
		if _, err := f.pairing.Claim(ctx, "b1", testApp, in); !errors.Is(err, ErrInvalidCode) {
			t.Errorf("Claim(%q) = %v, want ErrInvalidCode", in, err)
		}
	}
}

func TestCodeCollisionRetries(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{NewCode: codeSeq("111111111", "111111111", "222222222")})
	if _, err := f.pairing.Register(ctx, "d1", testApp, testDevice, testSecret); err != nil {
		t.Fatal(err)
	}
	code, err := f.pairing.Register(ctx, "d2", testApp, testDevice, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if code != "222 222 222" {
		t.Fatalf("collision not retried, got %q", code)
	}
}

func TestCodeSpaceExhausted(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{NewCode: codeSeq("111111111")})
	_, _ = f.pairing.Register(ctx, "d1", testApp, testDevice, testSecret)
	if _, err := f.pairing.Register(ctx, "d2", testApp, testDevice, testSecret); !errors.Is(err, ErrCodeUnavailable) {
		t.Fatalf("got %v, want ErrCodeUnavailable", err)
	}
}

func TestReRegisterInvalidatesOldCode(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{NewCode: codeSeq("111111111", "222222222")})
	old, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	if _, err := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pairing.Claim(ctx, "b1", testApp, old); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("old code still valid: %v", err)
	}
}

func TestRegisterValidation(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{AllowedApps: []string{testApp}})
	if _, err := f.pairing.Register(ctx, "d", "dialer", testDevice, testSecret); !errors.Is(err, ErrAppNotAllowed) {
		t.Errorf("app: got %v", err)
	}
	if _, err := f.pairing.Register(ctx, "d", "", testDevice, testSecret); !errors.Is(err, ErrAppNotAllowed) {
		t.Errorf("empty app: got %v", err)
	}
	if _, err := f.pairing.Register(ctx, "d", testApp, "not-a-uuid", testSecret); !errors.Is(err, ErrInvalidDeviceID) {
		t.Errorf("device_id: got %v", err)
	}
}

func TestMemberCannotRegisterOrClaimAgain(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{NewCode: codeSeq("111111111", "222222222")})
	code, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	_, _ = f.pairing.Claim(ctx, "b1", testApp, code)
	if _, err := f.pairing.Register(ctx, "b1", testApp, testDevice, testSecret); !errors.Is(err, ErrAlreadyInSession) {
		t.Errorf("member register: got %v", err)
	}
	code2, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	if _, err := f.pairing.Claim(ctx, "b1", testApp, code2); !errors.Is(err, ErrAlreadyInSession) {
		t.Errorf("member claim: got %v", err)
	}
	// The rejected claim must not burn the code.
	if _, err := f.pairing.Claim(ctx, "b2", testApp, code2); err != nil {
		t.Errorf("code was burned: %v", err)
	}
}

func TestSecondClaimJoinsSameSession(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{NewCode: codeSeq("111111111", "222222222")})
	c1, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	s1, _ := f.pairing.Claim(ctx, "b1", testApp, c1)
	c2, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	s2, _ := f.pairing.Claim(ctx, "b2", testApp, c2)
	if s1.ID != s2.ID || len(s2.Peers) != 2 {
		t.Fatalf("sessions differ: %+v %+v", s1, s2)
	}
}

func TestAuthorizeSignalStarTopology(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{NewCode: codeSeq("111111111", "222222222")})
	c1, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	_, _ = f.pairing.Claim(ctx, "b1", testApp, c1)
	c2, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	_, _ = f.pairing.Claim(ctx, "b2", testApp, c2)

	cases := []struct {
		from, to string
		want     error
	}{
		{"device", "b1", nil},
		{"device", "b2", nil},
		{"b1", "device", nil},
		{"b1", "b2", ErrTargetForbidden},
		{"b1", "b1", ErrTargetForbidden},
		{"device", "stranger", ErrTargetForbidden},
		{"device", "", ErrTargetForbidden},
		{"stranger", "device", ErrNotInSession},
	}
	for _, c := range cases {
		if _, err := f.sessions.AuthorizeSignal(ctx, c.from, c.to); !errors.Is(err, c.want) {
			t.Errorf("%s -> %s: got %v, want %v", c.from, c.to, err, c.want)
		}
	}
}

func TestLeave(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{NewCode: codeSeq("111111111", "222222222", "333333333")})
	c1, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	s, _ := f.pairing.Claim(ctx, "b1", testApp, c1)
	c2, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	_, _ = f.pairing.Claim(ctx, "b2", testApp, c2)

	// A member leaves: only the owner is notified.
	res, err := f.sessions.Leave(ctx, "b1")
	if err != nil || res.WasOwner || res.SessionID != s.ID || !slices.Equal(res.Notify, []string{"device"}) {
		t.Fatalf("member leave: %+v %v", res, err)
	}

	// The owner leaves with a pending code: members are notified and
	// the pending code dies with the owner.
	pending, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	res, err = f.sessions.Leave(ctx, "device")
	if err != nil || !res.WasOwner || !slices.Equal(res.Notify, []string{"b2"}) {
		t.Fatalf("owner leave: %+v %v", res, err)
	}
	if _, err := f.pairing.Claim(ctx, "b3", testApp, pending); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("pending code survived owner: %v", err)
	}

	// Idempotent.
	if res, err := f.sessions.Leave(ctx, "device"); err != nil || len(res.Notify) != 0 {
		t.Fatalf("second leave: %+v %v", res, err)
	}
}

func TestRegisterCreatesOwnerSession(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{})
	if _, err := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret); err != nil {
		t.Fatal(err)
	}
	s, err := f.repo.SessionByPeer(ctx, "device")
	if err != nil || s.OwnerPeerID != "device" || s.App != testApp {
		t.Fatalf("owner session: %+v %v", s, err)
	}
	if _, err := f.pairing.Register(ctx, "device", "dialer", testDevice, testSecret); !errors.Is(err, ErrAlreadyInSession) {
		t.Fatalf("app switch: %v", err)
	}
}

func TestClaimAfterOwnerLeft(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{})
	code, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	// Simulate the owner session disappearing while the code is still stored.
	_, _, _ = f.repo.DetachPeer(ctx, "device")
	if _, err := f.pairing.Claim(ctx, "b1", testApp, code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("got %v, want ErrInvalidCode", err)
	}
}

// openRoom registers device as owner and opens a room.
func openRoom(t *testing.T, f *fixture, owner string, public bool, meta string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := f.pairing.Register(ctx, owner, testApp, testDevice, testSecret); err != nil {
		t.Fatal(err)
	}
	r, err := f.rooms.Open(ctx, owner, public, false, []byte(meta))
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func TestRoomOpenRules(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{})
	if _, err := f.rooms.Open(ctx, "nobody", true, false, nil); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("open without session: %v", err)
	}
	id := openRoom(t, f, "device", true, `{"game":"sf2"}`)
	r, _ := f.repo.GetRoom(ctx, id)
	if r.App != testApp || r.OwnerPeerID != "device" || !r.Public {
		t.Fatalf("room: %+v", r)
	}
	if _, err := f.rooms.Open(ctx, "device", true, false, []byte(`{not json`)); !errors.Is(err, ErrInvalidMeta) {
		t.Fatalf("bad meta: %v", err)
	}
	big := `{"x":"` + strings.Repeat("a", MaxMetaSize) + `"}`
	if _, err := f.rooms.Open(ctx, "device", true, false, []byte(big)); !errors.Is(err, ErrInvalidMeta) {
		t.Fatalf("big meta: %v", err)
	}
	if _, err := f.rooms.Open(ctx, "device", false, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rooms.Open(ctx, "device", false, false, nil); !errors.Is(err, ErrRoomLimit) {
		t.Fatalf("limit: %v", err)
	}
	// A member of the session cannot manage rooms.
	c, _ := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret)
	_, _ = f.pairing.Claim(ctx, "b1", testApp, c)
	if _, err := f.rooms.Open(ctx, "b1", true, false, nil); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("member open: %v", err)
	}
}

func TestRoomDirectory(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{})
	pub := openRoom(t, f, "d1", true, `{"game":"sf2"}`)
	f.clk.Advance(time.Second)
	_ = openRoom(t, f, "d2", false, `{"game":"mslug"}`)

	rooms, err := f.rooms.List(ctx, testApp)
	if err != nil || len(rooms) != 1 || rooms[0].RoomID != pub || string(rooms[0].Meta) != `{"game":"sf2"}` {
		t.Fatalf("directory: %+v %v", rooms, err)
	}
	if _, err := f.rooms.List(ctx, ""); !errors.Is(err, ErrAppNotAllowed) {
		t.Fatalf("empty app: %v", err)
	}

	if err := f.rooms.Update(ctx, "d1", pub, []byte(`{"game":"sf2","free":3}`)); err != nil {
		t.Fatal(err)
	}
	if err := f.rooms.Update(ctx, "d2", pub, []byte(`{}`)); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("foreign update: %v", err)
	}
	rooms, _ = f.rooms.List(ctx, testApp)
	if string(rooms[0].Meta) != `{"game":"sf2","free":3}` {
		t.Fatalf("meta not updated: %s", rooms[0].Meta)
	}

	if err := f.rooms.Close(ctx, "d2", pub); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("foreign close: %v", err)
	}
	if err := f.rooms.Close(ctx, "d1", pub); err != nil {
		t.Fatal(err)
	}
	if rooms, _ := f.rooms.List(ctx, testApp); len(rooms) != 0 {
		t.Fatalf("closed room listed: %+v", rooms)
	}
}

func TestRoomJoin(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{})
	priv := openRoom(t, f, "device", false, "")

	sess, room, err := f.rooms.Join(ctx, "guest", testApp, priv)
	if err != nil || room.ID != priv || sess.OwnerPeerID != "device" || !sess.Has("guest") {
		t.Fatalf("join private room: %+v %+v %v", sess, room, err)
	}
	if _, _, err := f.rooms.Join(ctx, "guest", testApp, priv); !errors.Is(err, ErrAlreadyInSession) {
		t.Fatalf("double join: %v", err)
	}
	if _, _, err := f.rooms.Join(ctx, "g2", "dialer", priv); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("other app: %v", err)
	}
	if _, _, err := f.rooms.Join(ctx, "g2", testApp, "not-a-uuid"); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("bad id: %v", err)
	}
	if _, _, err := f.rooms.Join(ctx, "g2", testApp, "3f2b9c1e-7a4d-4e0b-9c1d-2e3f4a5b6c7d"); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("unknown room: %v", err)
	}

	// Closing the room stops new joins but keeps the guest in the session.
	_ = f.rooms.Close(ctx, "device", priv)
	if _, _, err := f.rooms.Join(ctx, "g2", testApp, priv); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("join closed room: %v", err)
	}
	if s, _ := f.repo.SessionByPeer(ctx, "guest"); !s.Has("guest") {
		t.Fatal("guest dropped when the room closed")
	}
}

func TestOwnerLeaveClosesRooms(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{})
	id := openRoom(t, f, "device", true, "")
	_, _ = f.sessions.Leave(ctx, "device")
	if rooms, _ := f.rooms.List(ctx, testApp); len(rooms) != 0 {
		t.Fatal("room survived its owner")
	}
	if _, _, err := f.rooms.Join(ctx, "g", testApp, id); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("join after owner left: %v", err)
	}
}

func TestICEServers(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	p := NewICEProvider(ICEConfig{
		STUNURLs:   []string{"stun:stun.example:3478"},
		TURNURLs:   []string{"turn:turn.example:3478?transport=udp"},
		TURNSecret: "secret",
		TURNTTL:    time.Hour,
		Now:        func() time.Time { return now },
	})
	servers := p.Servers("peer")
	if len(servers) != 2 || servers[0].Username != "" {
		t.Fatalf("servers: %+v", servers)
	}
	if servers[1].Username != "1700003600:peer" || servers[1].Credential == "" {
		t.Fatalf("turn: %+v", servers[1])
	}
	// Without a secret TURN is not offered.
	if s := NewICEProvider(ICEConfig{TURNURLs: []string{"turn:x"}}).Servers("peer"); len(s) != 0 {
		t.Fatalf("TURN without secret: %+v", s)
	}
}

func TestReach(t *testing.T) {
	ctx := context.Background()
	repo := repositories.NewMemory(nil)
	svc := NewPairingService(repo, PairingConfig{})
	const dev = "7f3c2a10-1b2c-4d3e-8f90-a1b2c3d4e5f6"
	if _, err := svc.Reach(ctx, "browser", "app", dev); !errors.Is(err, ErrDeviceOffline) {
		t.Fatalf("offline device: %v", err)
	}
	if _, err := svc.Register(ctx, "owner", "app", dev, testSecret); err != nil {
		t.Fatal(err)
	}
	sess, err := svc.Reach(ctx, "browser", "app", dev)
	if err != nil || sess.OwnerPeerID != "owner" || !sess.Has("browser") {
		t.Fatalf("reach: %+v %v", sess, err)
	}
	if _, err := svc.Reach(ctx, "browser", "app", dev); !errors.Is(err, ErrAlreadyInSession) {
		t.Fatalf("second reach: %v", err)
	}
	if _, err := svc.Reach(ctx, "owner", "app", dev); !errors.Is(err, ErrAlreadyInSession) {
		t.Fatalf("owner reaching itself: %v", err)
	}
	// The owner leaves: its device_id is no longer reachable.
	if _, _, err := repo.DetachPeer(ctx, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Reach(ctx, "late", "app", dev); !errors.Is(err, ErrDeviceOffline) {
		t.Fatalf("after owner left: %v", err)
	}
}

func TestRoomInvites(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{})
	if _, err := f.pairing.Register(ctx, "device", testApp, testDevice, testSecret); err != nil {
		t.Fatal(err)
	}
	room, err := f.rooms.Open(ctx, "device", false, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.rooms.CreateInvite(ctx, "intruder", room.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("invite from a stranger: %v", err)
	}
	invite, code, err := f.rooms.CreateInvite(ctx, "device", room.ID)
	if err != nil || len(invite) != 22 || len(code) != 9 {
		t.Fatalf("invite %q code %q err %v", invite, code, err)
	}
	// An invite-only room refuses its room_id.
	if _, _, err := f.rooms.Join(ctx, "g1", testApp, room.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("join by id: %v", err)
	}
	if _, r, err := f.rooms.JoinInvite(ctx, "g1", testApp, invite, ""); err != nil || r.ID != room.ID {
		t.Fatalf("join by invite: %v", err)
	}
	spaced := code[0:3] + " " + code[3:6] + " " + code[6:9]
	if _, _, err := f.rooms.JoinInvite(ctx, "g2", testApp, "", spaced); err != nil {
		t.Fatalf("join by code: %v", err)
	}
	if _, _, err := f.rooms.JoinInvite(ctx, "g3", "other-app", invite, ""); !errors.Is(err, ErrInvalidInvite) && !errors.Is(err, ErrAppNotAllowed) {
		t.Fatalf("other app: %v", err)
	}
	// A new invitation replaces the old one.
	fresh, freshCode, err := f.rooms.CreateInvite(ctx, "device", room.ID)
	if err != nil || fresh == invite {
		t.Fatalf("rotate: %v", err)
	}
	if _, _, err := f.rooms.JoinInvite(ctx, "g4", testApp, invite, ""); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("old invite: %v", err)
	}
	if _, _, err := f.rooms.JoinInvite(ctx, "g4", testApp, "", code); !errors.Is(err, ErrInvalidInvite) && code != freshCode {
		t.Fatalf("old code: %v", err)
	}
	for _, bad := range []string{"short", "!!!!!!!!!!!!!!!!!!!!!!"} {
		if _, _, err := f.rooms.JoinInvite(ctx, "g5", testApp, bad, ""); !errors.Is(err, ErrInvalidInvite) {
			t.Fatalf("bad invite %q: %v", bad, err)
		}
	}
	// The invitation dies with the room.
	if err := f.rooms.Close(ctx, "device", room.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.rooms.JoinInvite(ctx, "g6", testApp, fresh, ""); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("invite of a closed room: %v", err)
	}
}

func TestPublicRoomsAcceptBothIDAndInvite(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{})
	id := openRoom(t, f, "device", true, `{}`)
	invite, _, err := f.rooms.CreateInvite(ctx, "device", id)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.rooms.Join(ctx, "g1", testApp, id); err != nil {
		t.Fatalf("join by id: %v", err)
	}
	if _, _, err := f.rooms.JoinInvite(ctx, "g2", testApp, invite, ""); err != nil {
		t.Fatalf("join by invite: %v", err)
	}
}

func TestADeviceIDStaysWithItsSecret(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{})
	secret := strings.Repeat("A", 43)
	other := strings.Repeat("B", 43)
	if _, err := f.pairing.Register(ctx, "device", testApp, testDevice, secret); err != nil {
		t.Fatal(err)
	}
	// Someone else with the device_id: another secret, or none, is refused.
	if _, err := f.pairing.Register(ctx, "thief", testApp, testDevice, other); !errors.Is(err, ErrDeviceTaken) {
		t.Fatalf("other secret: %v", err)
	}
	if _, err := f.pairing.Register(ctx, "thief2", testApp, testDevice, ""); !errors.Is(err, ErrInvalidDeviceID) {
		t.Fatalf("no secret: %v", err)
	}
	// Still bound after the device disconnects (it restarts, say).
	if _, err := f.sessions.Leave(ctx, "device"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pairing.Register(ctx, "thief3", testApp, testDevice, other); !errors.Is(err, ErrDeviceTaken) {
		t.Fatalf("after leaving: %v", err)
	}
	// The device itself comes back with its secret.
	if _, err := f.pairing.Register(ctx, "device-again", testApp, testDevice, secret); err != nil {
		t.Fatalf("the device came back: %v", err)
	}
	if owner, _ := f.repo.OwnerByDevice(ctx, testApp, testDevice); owner != "device-again" {
		t.Fatalf("owner %q", owner)
	}
	// A binding nobody renewed for its whole life is forgotten.
	f.clk.Advance(repositories.DeviceBindingTTL)
	f.repo.PurgeExpired()
	if _, err := f.sessions.Leave(ctx, "device-again"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pairing.Register(ctx, "new-owner", testApp, testDevice, other); err != nil {
		t.Fatalf("after the binding expired: %v", err)
	}
}

func TestDeviceSecretFormatAndOneDevicePerConnection(t *testing.T) {
	ctx := context.Background()
	f := newFixture(PairingConfig{})
	if _, err := f.pairing.Register(ctx, "d", testApp, testDevice, "too-short"); !errors.Is(err, ErrInvalidDeviceID) {
		t.Fatalf("bad secret: %v", err)
	}
	// A connection that registers another device_id drops the first one.
	second := "0b5c7c1e-9d7a-4b8e-8a31-2f6f3c9d1e24"
	if _, err := f.pairing.Register(ctx, "d", testApp, testDevice, testSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pairing.Register(ctx, "d", testApp, second, testSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.OwnerByDevice(ctx, testApp, testDevice); err == nil {
		t.Fatal("the first device_id still points to the connection")
	}
	if owner, _ := f.repo.OwnerByDevice(ctx, testApp, second); owner != "d" {
		t.Fatalf("second owner %q", owner)
	}
}

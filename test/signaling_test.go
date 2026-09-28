// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

// Package test runs end-to-end scenarios against a real HTTP server with
// real WebSocket clients.
package test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/lordbasex/signalhub/internal/controllers"
	"github.com/lordbasex/signalhub/internal/models"
	"github.com/lordbasex/signalhub/internal/repositories"
	"github.com/lordbasex/signalhub/internal/services"
)

const (
	app      = "go-link"
	deviceID = "7f3c2a10-1b2c-4d3e-8f90-a1b2c3d4e5f6"
	// deviceSecret is a well-formed device_secret.
	deviceSecret = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	webApp       = "https://go-link.org"
)

var codePattern = regexp.MustCompile(`^\d{3} \d{3} \d{3}$`)

type server struct {
	url string
}

type options struct {
	allowedApps       []string
	attemptsPerMinute int
	clientIPHeader    string
	turnSecret        string
	maxConnsPerIP     int
	messagesPerSecond int
}

func newServer(t *testing.T) server {
	return newServerWith(t, options{attemptsPerMinute: 100})
}

func newServerWith(t *testing.T, o options) server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo := repositories.NewMemory(time.Now)
	ice := services.ICEConfig{STUNURLs: []string{"stun:stun.example:3478"}}
	if o.turnSecret != "" {
		ice.TURNURLs = []string{"turn:turn.example:3478?transport=udp"}
		ice.TURNSecret = o.turnSecret
	}
	hub := controllers.NewHub(controllers.HubConfig{
		AllowedOrigins:    []string{webApp},
		Logger:            logger,
		AttemptsPerMinute: o.attemptsPerMinute,
		ClientIPHeader:    o.clientIPHeader,
		MaxConnsPerIP:     o.maxConnsPerIP,
		MessagesPerSecond: o.messagesPerSecond,
	}, controllers.Services{
		Pairing:  services.NewPairingService(repo, services.PairingConfig{AllowedApps: o.allowedApps}),
		Sessions: services.NewSessionService(repo),
		Rooms:    services.NewRoomService(repo, services.RoomConfig{AllowedApps: o.allowedApps}),
		ICE:      services.NewICEProvider(ice),
	})
	srv := httptest.NewServer(controllers.NewRouter(hub))
	t.Cleanup(func() {
		hub.Shutdown()
		srv.Close()
	})
	return server{url: srv.URL}
}

func (s server) wsURL(query string) string {
	return "ws" + strings.TrimPrefix(s.url, "http") + "/ws" + query
}

type wsClient struct {
	t      *testing.T
	conn   *websocket.Conn
	peerID string
}

// connect dials the server and consumes the hello message.
func (s server) connect(t *testing.T) *wsClient {
	t.Helper()
	c, _ := s.connectWith(t, nil)
	return c
}

// connectWith dials with extra headers and returns the client and its hello.
func (s server) connectWith(t *testing.T, header http.Header) (*wsClient, models.Envelope) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(s.wsURL("?v=1"), header)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := &wsClient{t: t, conn: conn}
	t.Cleanup(func() { _ = conn.Close() })
	hello := c.expect(models.TypeHello)
	if len(hello.PeerID) != 32 || len(hello.ICEServers) == 0 {
		t.Fatalf("bad hello: %+v", hello)
	}
	c.peerID = hello.PeerID
	return c, hello
}

func (c *wsClient) send(env models.Envelope) {
	c.t.Helper()
	if err := c.conn.WriteJSON(env); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *wsClient) recv() models.Envelope {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var env models.Envelope
	if err := c.conn.ReadJSON(&env); err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return env
}

func (c *wsClient) expect(typ string) models.Envelope {
	c.t.Helper()
	env := c.recv()
	if env.Type != typ {
		c.t.Fatalf("got %s (%+v), want %s", env.Type, env, typ)
	}
	return env
}

func (c *wsClient) expectError(msg string) {
	c.t.Helper()
	if env := c.expect(models.TypeError); env.Error != msg {
		c.t.Fatalf("error = %q, want %q", env.Error, msg)
	}
}

// register asks for a pairing code and returns it formatted.
func (c *wsClient) register() string {
	c.t.Helper()
	c.send(models.Envelope{Type: models.TypeRegister, App: app, DeviceID: deviceID, DeviceSecret: deviceSecret})
	code := c.expect(models.TypeCode).Code
	if !codePattern.MatchString(code) {
		c.t.Fatalf("bad code format %q", code)
	}
	return code
}

// pair runs claim from browser and checks both paired messages.
func pair(t *testing.T, device, browser *wsClient, code string) string {
	t.Helper()
	browser.send(models.Envelope{Type: models.TypeClaim, App: app, Code: code})
	dp := device.expect(models.TypePaired)
	bp := browser.expect(models.TypePaired)
	if dp.SessionID == "" || dp.SessionID != bp.SessionID {
		t.Fatalf("session mismatch: %q vs %q", dp.SessionID, bp.SessionID)
	}
	if dp.Remote != browser.peerID || bp.Remote != device.peerID {
		t.Fatalf("remote mismatch: device got %q, browser got %q", dp.Remote, bp.Remote)
	}
	return dp.SessionID
}

// TestFullPairingFlow walks register -> code -> claim -> paired -> signal -> peer_left.
func TestFullPairingFlow(t *testing.T) {
	s := newServer(t)
	device := s.connect(t)
	browser := s.connect(t)

	code := device.register()
	session := pair(t, device, browser, strings.ReplaceAll(code, " ", ""))

	// Device -> browser offer. The spoofed From must be replaced.
	offer := json.RawMessage(`{"sdp":"offer-sdp","kind":"offer"}`)
	device.send(models.Envelope{Type: models.TypeSignal, To: browser.peerID, From: "spoofed", Payload: offer})
	got := browser.expect(models.TypeSignal)
	if got.From != device.peerID || string(got.Payload) != string(offer) {
		t.Fatalf("bad relay to browser: %+v", got)
	}

	// Browser -> device answer.
	answer := json.RawMessage(`{"sdp":"answer-sdp","kind":"answer"}`)
	browser.send(models.Envelope{Type: models.TypeSignal, To: device.peerID, From: device.peerID, Payload: answer})
	got = device.expect(models.TypeSignal)
	if got.From != browser.peerID || string(got.Payload) != string(answer) {
		t.Fatalf("bad relay to device: %+v", got)
	}

	// Browser disconnects: the device is told.
	_ = browser.conn.Close()
	left := device.expect(models.TypePeerLeft)
	if left.From != browser.peerID || left.SessionID != session {
		t.Fatalf("bad peer_left: %+v", left)
	}
}

func TestCodeIsSingleUse(t *testing.T) {
	s := newServer(t)
	device := s.connect(t)
	code := device.register()
	pair(t, device, s.connect(t), code)

	late := s.connect(t)
	late.send(models.Envelope{Type: models.TypeClaim, App: app, Code: code})
	late.expectError(services.ErrInvalidCode.Error())
}

func TestWrongAppIsInvalidCode(t *testing.T) {
	s := newServer(t)
	device := s.connect(t)
	code := device.register()
	browser := s.connect(t)
	browser.send(models.Envelope{Type: models.TypeClaim, App: "dialer", Code: code})
	browser.expectError(services.ErrInvalidCode.Error())
}

func TestAllowedApps(t *testing.T) {
	s := newServerWith(t, options{allowedApps: []string{app}})
	device := s.connect(t)
	device.send(models.Envelope{Type: models.TypeRegister, App: "dialer", DeviceID: deviceID, DeviceSecret: deviceSecret})
	device.expectError(services.ErrAppNotAllowed.Error())
}

func TestOwnerLeaveNotifiesGuests(t *testing.T) {
	s := newServer(t)
	device := s.connect(t)
	browser := s.connect(t)
	session := pair(t, device, browser, device.register())

	_ = device.conn.Close()
	left := browser.expect(models.TypePeerLeft)
	if left.From != device.peerID || left.SessionID != session {
		t.Fatalf("bad peer_left: %+v", left)
	}
	// The session is gone: the former guest can no longer signal.
	browser.send(models.Envelope{Type: models.TypeSignal, To: device.peerID, Payload: json.RawMessage(`{}`)})
	browser.expectError(services.ErrNotInSession.Error())
}

func TestStarTopology(t *testing.T) {
	s := newServer(t)
	device := s.connect(t)
	b1 := s.connect(t)
	b2 := s.connect(t)
	s1 := pair(t, device, b1, device.register())
	s2 := pair(t, device, b2, device.register())
	if s1 != s2 {
		t.Fatalf("second browser got a different session")
	}

	// Guest to guest is forbidden.
	b1.send(models.Envelope{Type: models.TypeSignal, To: b2.peerID, Payload: json.RawMessage(`{}`)})
	b1.expectError(services.ErrTargetForbidden.Error())

	// Owner to any guest is fine.
	device.send(models.Envelope{Type: models.TypeSignal, To: b2.peerID, Payload: json.RawMessage(`{"n":2}`)})
	if got := b2.expect(models.TypeSignal); got.From != device.peerID {
		t.Fatalf("bad relay: %+v", got)
	}

	// A guest leaving only notifies the owner, not the other guest.
	_ = b1.conn.Close()
	if left := device.expect(models.TypePeerLeft); left.From != b1.peerID {
		t.Fatalf("bad peer_left: %+v", left)
	}
	device.send(models.Envelope{Type: models.TypeSignal, To: b2.peerID, Payload: json.RawMessage(`{"n":3}`)})
	if got := b2.expect(models.TypeSignal); string(got.Payload) != `{"n":3}` {
		t.Fatalf("b2 received something else first: %+v", got)
	}
}

func TestStrangerCannotSignal(t *testing.T) {
	s := newServer(t)
	device := s.connect(t)
	stranger := s.connect(t)
	stranger.send(models.Envelope{Type: models.TypeSignal, To: device.peerID, Payload: json.RawMessage(`{}`)})
	stranger.expectError(services.ErrNotInSession.Error())
}

func TestInvalidMessages(t *testing.T) {
	s := newServer(t)
	c := s.connect(t)
	if err := c.conn.WriteMessage(websocket.TextMessage, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	c.expectError("invalid message")
	c.send(models.Envelope{Type: models.TypePaired})
	c.expectError("unsupported message type")
}

func TestMessageSizeLimit(t *testing.T) {
	s := newServer(t)
	c := s.connect(t)
	big := `{"type":"signal","payload":"` + strings.Repeat("x", 70<<10) + `"}`
	_ = c.conn.WriteMessage(websocket.TextMessage, []byte(big))
	_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := c.conn.ReadMessage(); err == nil {
		t.Fatal("connection should be closed after an oversized message")
	}
}

func TestOriginCheck(t *testing.T) {
	s := newServer(t)
	evil := http.Header{"Origin": {"https://evil.example"}}
	if _, resp, err := websocket.DefaultDialer.Dial(s.wsURL("?v=1"), evil); err == nil {
		t.Fatal("foreign origin was accepted")
	} else if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %v", resp)
	}
	good := http.Header{"Origin": {webApp}}
	conn, _, err := websocket.DefaultDialer.Dial(s.wsURL("?v=1"), good)
	if err != nil {
		t.Fatalf("allowed origin rejected: %v", err)
	}
	_ = conn.Close()
}

func TestUnsupportedVersion(t *testing.T) {
	s := newServer(t)
	_, resp, err := websocket.DefaultDialer.Dial(s.wsURL("?v=2"), nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %v %v", resp, err)
	}
}

func TestHealthz(t *testing.T) {
	s := newServer(t)
	resp, err := http.Get(s.url + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("healthz: %d %q", resp.StatusCode, body)
	}
}

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// openRoom registers the device and opens a room, returning its ID.
func (c *wsClient) openRoom(public bool, meta string) string {
	c.t.Helper()
	c.register()
	env := models.Envelope{Type: models.TypeRoomOpen, Public: public}
	if meta != "" {
		env.Meta = json.RawMessage(meta)
	}
	c.send(env)
	id := c.expect(models.TypeRoomOpened).RoomID
	if !uuidV4.MatchString(id) {
		c.t.Fatalf("room_id is not a UUID v4: %q", id)
	}
	return id
}

// sync waits until the server has processed everything c sent before.
// Messages from one connection are handled in order, so a round trip is
// enough. It is needed after room_update and room_close, which have no reply.
func (c *wsClient) sync() {
	c.t.Helper()
	c.listRooms()
}

func (c *wsClient) listRooms() []models.RoomInfo {
	c.t.Helper()
	c.send(models.Envelope{Type: models.TypeRoomsList, App: app})
	return c.expect(models.TypeRooms).Rooms
}

// TestRoomFlow walks room_open -> rooms_list -> join -> joined/peer_joined -> signal.
func TestRoomFlow(t *testing.T) {
	s := newServer(t)
	device := s.connect(t)
	roomID := device.openRoom(true, `{"game":"sf2","free":4}`)

	lobby := s.connect(t)
	rooms := lobby.listRooms()
	if len(rooms) != 1 || rooms[0].RoomID != roomID || string(rooms[0].Meta) != `{"game":"sf2","free":4}` {
		t.Fatalf("directory: %+v", rooms)
	}

	// The device updates the directory; no reply on success.
	device.send(models.Envelope{Type: models.TypeRoomUpdate, RoomID: roomID, Meta: json.RawMessage(`{"game":"sf2","free":3}`)})
	device.sync()
	guest := s.connect(t)
	guest.send(models.Envelope{Type: models.TypeJoin, App: app, RoomID: roomID})
	pj := device.expect(models.TypePeerJoined)
	j := guest.expect(models.TypeJoined)
	if pj.Remote != guest.peerID || pj.RoomID != roomID || j.Remote != device.peerID || j.RoomID != roomID || pj.SessionID != j.SessionID {
		t.Fatalf("join mismatch: %+v / %+v", pj, j)
	}
	if rooms := lobby.listRooms(); string(rooms[0].Meta) != `{"game":"sf2","free":3}` {
		t.Fatalf("update not visible: %s", rooms[0].Meta)
	}

	device.send(models.Envelope{Type: models.TypeSignal, To: guest.peerID, Payload: json.RawMessage(`{"sdp":"x"}`)})
	if got := guest.expect(models.TypeSignal); got.From != device.peerID {
		t.Fatalf("relay: %+v", got)
	}

	// Closing the room removes it from the directory but keeps the guest.
	device.send(models.Envelope{Type: models.TypeRoomClose, RoomID: roomID})
	device.sync()
	if rooms := lobby.listRooms(); len(rooms) != 0 {
		t.Fatalf("closed room listed: %+v", rooms)
	}
	lobby.send(models.Envelope{Type: models.TypeJoin, App: app, RoomID: roomID})
	lobby.expectError(services.ErrRoomNotFound.Error())
	guest.send(models.Envelope{Type: models.TypeSignal, To: device.peerID, Payload: json.RawMessage(`{}`)})
	if got := device.expect(models.TypeSignal); got.From != guest.peerID {
		t.Fatalf("guest lost the session: %+v", got)
	}
}

func TestPrivateRoom(t *testing.T) {
	s := newServer(t)
	device := s.connect(t)
	roomID := device.openRoom(false, "")

	lobby := s.connect(t)
	if rooms := lobby.listRooms(); len(rooms) != 0 {
		t.Fatalf("private room listed: %+v", rooms)
	}
	// Knowing the link is enough to enter.
	lobby.send(models.Envelope{Type: models.TypeJoin, App: app, RoomID: roomID})
	device.expect(models.TypePeerJoined)
	lobby.expect(models.TypeJoined)
}

func TestRoomOwnership(t *testing.T) {
	s := newServer(t)
	device := s.connect(t)
	roomID := device.openRoom(true, "")

	stranger := s.connect(t)
	stranger.send(models.Envelope{Type: models.TypeRoomOpen, Public: true})
	stranger.expectError(services.ErrNotOwner.Error())
	stranger.send(models.Envelope{Type: models.TypeRoomClose, RoomID: roomID})
	stranger.expectError(services.ErrRoomNotFound.Error())

	// Stage 1 allows one room per session.
	device.send(models.Envelope{Type: models.TypeRoomOpen})
	device.expectError(services.ErrRoomLimit.Error())
	big := `{"x":"` + strings.Repeat("a", services.MaxMetaSize) + `"}`
	device.send(models.Envelope{Type: models.TypeRoomUpdate, RoomID: roomID, Meta: json.RawMessage(big)})
	device.expectError(services.ErrInvalidMeta.Error())
}

func TestOwnerLeaveClosesRooms(t *testing.T) {
	s := newServer(t)
	device := s.connect(t)
	roomID := device.openRoom(true, "")
	guest := s.connect(t)
	guest.send(models.Envelope{Type: models.TypeJoin, App: app, RoomID: roomID})
	device.expect(models.TypePeerJoined)
	guest.expect(models.TypeJoined)

	_ = device.conn.Close()
	guest.expect(models.TypePeerLeft)
	if rooms := guest.listRooms(); len(rooms) != 0 {
		t.Fatalf("room survived its owner: %+v", rooms)
	}
}

func TestClaimRateLimit(t *testing.T) {
	s := newServerWith(t, options{attemptsPerMinute: 5})
	c := s.connect(t)
	for i := 0; i < 5; i++ {
		c.send(models.Envelope{Type: models.TypeClaim, App: app, Code: "000000000"})
		c.expectError(services.ErrInvalidCode.Error())
	}
	c.send(models.Envelope{Type: models.TypeClaim, App: app, Code: "000000000"})
	c.expectError("rate limit exceeded")
	// join shares the same budget.
	c.send(models.Envelope{Type: models.TypeJoin, App: app, RoomID: "3f2b9c1e-7a4d-4e0b-9c1d-2e3f4a5b6c7d"})
	c.expectError("rate limit exceeded")
}

func TestRateLimitUsesProxyHeader(t *testing.T) {
	s := newServerWith(t, options{attemptsPerMinute: 1, clientIPHeader: "X-Forwarded-For"})
	// Both connections come from 127.0.0.1, but the proxy says they are
	// different clients. The left XFF entry is forged and must be ignored.
	a, _ := s.connectWith(t, http.Header{"X-Forwarded-For": {"9.9.9.9, 1.1.1.1"}})
	b, _ := s.connectWith(t, http.Header{"X-Forwarded-For": {"9.9.9.9, 2.2.2.2"}})
	for _, c := range []*wsClient{a, b} {
		c.send(models.Envelope{Type: models.TypeClaim, App: app, Code: "000000000"})
		c.expectError(services.ErrInvalidCode.Error())
	}
	a.send(models.Envelope{Type: models.TypeClaim, App: app, Code: "000000000"})
	a.expectError("rate limit exceeded")
}

func TestHelloIncludesTURNCredentials(t *testing.T) {
	s := newServerWith(t, options{turnSecret: "s3cret"})
	_, hello := s.connectWith(t, nil)
	if len(hello.ICEServers) != 2 {
		t.Fatalf("ice servers: %+v", hello.ICEServers)
	}
	turn := hello.ICEServers[1]
	if !strings.HasSuffix(turn.Username, ":"+hello.PeerID) || turn.Credential == "" {
		t.Fatalf("turn credentials not bound to peer_id: %+v", turn)
	}
}

// TestReachWithoutCode brings a browser back to a device it linked before:
// no code, the device learns the peer is unproven (reached), and a device
// that reconnects takes over its device_id.
func TestReachWithoutCode(t *testing.T) {
	s := newServer(t)
	browser := s.connect(t)
	browser.send(models.Envelope{Type: models.TypeReach, App: app, DeviceID: deviceID})
	browser.expectError(services.ErrDeviceOffline.Error())

	device := s.connect(t)
	device.register()
	browser.send(models.Envelope{Type: models.TypeReach, App: app, DeviceID: "not-a-uuid"})
	browser.expectError(services.ErrInvalidDeviceID.Error())
	browser.send(models.Envelope{Type: models.TypeReach, App: "other", DeviceID: deviceID})
	browser.expectError(services.ErrDeviceOffline.Error())

	browser.send(models.Envelope{Type: models.TypeReach, App: app, DeviceID: deviceID})
	dr := device.expect(models.TypeReached)
	bp := browser.expect(models.TypePaired)
	if dr.Remote != browser.peerID || bp.Remote != device.peerID || dr.SessionID != bp.SessionID {
		t.Fatalf("reached %+v, paired %+v", dr, bp)
	}
	// Once in the session, signals flow as after a claim.
	device.send(models.Envelope{Type: models.TypeSignal, To: browser.peerID, Payload: json.RawMessage(`{"kind":"offer"}`)})
	browser.expect(models.TypeSignal)

	// The device restarts: the old connection goes, a new one registers
	// the same device_id, and a new browser connection reaches it.
	_ = device.conn.Close()
	browser.expect(models.TypePeerLeft)
	again := s.connect(t)
	again.register()
	back := s.connect(t)
	back.send(models.Envelope{Type: models.TypeReach, App: app, DeviceID: deviceID})
	if dr := again.expect(models.TypeReached); dr.Remote != back.peerID {
		t.Fatalf("reached after restart: %+v", dr)
	}
	back.expect(models.TypePaired)
}

// TestInviteOnlyRoom walks room_open{invite_only} -> invite_create ->
// join by invite and by code -> a new invite that retires the old one.
func TestInviteOnlyRoom(t *testing.T) {
	s := newServer(t)
	device := s.connect(t)
	device.register()
	device.send(models.Envelope{Type: models.TypeRoomOpen, InviteOnly: true})
	roomID := device.expect(models.TypeRoomOpened).RoomID

	device.send(models.Envelope{Type: models.TypeInviteCreate, RoomID: roomID})
	inv := device.expect(models.TypeInviteCreated)
	if inv.RoomID != roomID || len(inv.Invite) != 22 || len(inv.Code) != 9 {
		t.Fatalf("invite_created: %+v", inv)
	}

	byID := s.connect(t)
	byID.send(models.Envelope{Type: models.TypeJoin, App: app, RoomID: roomID})
	byID.expectError(services.ErrRoomNotFound.Error())

	guest := s.connect(t)
	guest.send(models.Envelope{Type: models.TypeJoin, App: app, Invite: inv.Invite})
	device.expect(models.TypePeerJoined)
	if j := guest.expect(models.TypeJoined); j.RoomID != roomID {
		t.Fatalf("joined: %+v", j)
	}
	typed := s.connect(t)
	typed.send(models.Envelope{Type: models.TypeJoin, App: app, Code: inv.Code})
	device.expect(models.TypePeerJoined)
	typed.expect(models.TypeJoined)

	// "New link": the old invite stops working.
	device.send(models.Envelope{Type: models.TypeInviteCreate, RoomID: roomID})
	if fresh := device.expect(models.TypeInviteCreated); fresh.Invite == inv.Invite {
		t.Fatal("the invite did not change")
	}
	late := s.connect(t)
	late.send(models.Envelope{Type: models.TypeJoin, App: app, Invite: inv.Invite})
	late.expectError(services.ErrInvalidInvite.Error())

	// Only the owner creates invites.
	guest.send(models.Envelope{Type: models.TypeInviteCreate, RoomID: roomID})
	guest.expectError(services.ErrRoomNotFound.Error())
}

func TestOneAddressCannotOpenEndlessConnections(t *testing.T) {
	s := newServerWith(t, options{attemptsPerMinute: 100, maxConnsPerIP: 3})
	for range 3 {
		s.connect(t)
	}
	conn, _, err := websocket.DefaultDialer.Dial(s.wsURL("?v=1"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := conn.ReadMessage(); !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("the fourth connection got %v, want a close for too many connections", err)
	}
}

func TestAFloodingConnectionIsClosed(t *testing.T) {
	s := newServerWith(t, options{attemptsPerMinute: 1000, messagesPerSecond: 5})
	c := s.connect(t)
	for range 30 {
		_ = c.conn.WriteJSON(models.Envelope{Type: models.TypeRoomsList, App: app})
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var env models.Envelope
		if err := c.conn.ReadJSON(&env); err != nil {
			return // closed by the server
		}
		if env.Type == models.TypeError && env.Error == "too many messages" {
			continue
		}
	}
}

func TestRegisterWithAnotherDevicesSecretIsRefused(t *testing.T) {
	s := newServer(t)
	device := s.connect(t)
	secret := strings.Repeat("s", 43)
	device.send(models.Envelope{Type: models.TypeRegister, App: app, DeviceID: deviceID, DeviceSecret: secret})
	device.expect(models.TypeCode)
	thief := s.connect(t)
	thief.send(models.Envelope{Type: models.TypeRegister, App: app, DeviceID: deviceID, DeviceSecret: strings.Repeat("t", 43)})
	thief.expectError("device_id taken")
}

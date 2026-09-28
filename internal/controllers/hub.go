// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

// Package controllers adapts WebSocket connections to the services.
package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/time/rate"

	"github.com/lordbasex/signalhub/internal/models"
	"github.com/lordbasex/signalhub/internal/services"
	"github.com/lordbasex/signalhub/pkg/ids"
	"github.com/lordbasex/signalhub/pkg/ratelimit"
)

// errRateLimited is returned when an IP sends too many attempts.
var errRateLimited = errors.New("rate limit exceeded")

// errTooManyMessages closes a connection that floods the server.
var errTooManyMessages = errors.New("too many messages")

// Services groups the business services the Hub delegates to.
type Services struct {
	Pairing  *services.PairingService
	Sessions *services.SessionService
	Rooms    *services.RoomService
	ICE      *services.ICEProvider
}

// HubConfig configures the Hub. Zero values get defaults.
type HubConfig struct {
	AllowedOrigins []string // browser origins allowed to connect; "*" allows any
	Logger         *slog.Logger

	// AttemptsPerMinute limits claim and join per client IP (default 5).
	AttemptsPerMinute int
	// ListsPerMinute limits rooms_list per client IP (default 30).
	ListsPerMinute int
	// OwnerPerMinute limits register, room_open and invite_create per
	// client IP (default 30).
	OwnerPerMinute int
	// MaxConnsPerIP caps the open connections of one client IP (default
	// 20); MaxConns caps them all (default 5000).
	MaxConnsPerIP int
	MaxConns      int
	// MessagesPerSecond caps what one connection may send (default 30,
	// bursts of twice that); a flooding connection is closed.
	MessagesPerSecond int
	// ClientIPHeader names the header a trusted reverse proxy uses to
	// pass the real client IP (X-Forwarded-For or X-Real-IP). Empty
	// means the TCP peer address is used.
	ClientIPHeader string

	MaxMessageSize int64         // default 64 KB
	SendBuffer     int           // default 32 messages
	WriteWait      time.Duration // default 10s
	PongWait       time.Duration // default 60s
	PingPeriod     time.Duration // default 90% of PongWait
}

// Hub owns every live WebSocket connection and routes messages between
// them. Business rules live in the services; the Hub only translates.
type Hub struct {
	cfg      HubConfig
	log      *slog.Logger
	svc      Services
	upgrader websocket.Upgrader
	attempts *ratelimit.Keyed
	lists    *ratelimit.Keyed
	owners   *ratelimit.Keyed

	mu      sync.Mutex
	clients map[string]*client // peer_id -> client
	perIP   map[string]int     // client IP -> open connections
	closed  bool
}

// NewHub builds a Hub and fills config defaults.
func NewHub(cfg HubConfig, svc Services) *Hub {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaxMessageSize <= 0 {
		cfg.MaxMessageSize = 64 << 10
	}
	if cfg.SendBuffer <= 0 {
		cfg.SendBuffer = 32
	}
	if cfg.WriteWait <= 0 {
		cfg.WriteWait = 10 * time.Second
	}
	if cfg.PongWait <= 0 {
		cfg.PongWait = 60 * time.Second
	}
	if cfg.PingPeriod <= 0 || cfg.PingPeriod >= cfg.PongWait {
		cfg.PingPeriod = cfg.PongWait * 9 / 10
	}
	if cfg.AttemptsPerMinute <= 0 {
		cfg.AttemptsPerMinute = 5
	}
	if cfg.ListsPerMinute <= 0 {
		cfg.ListsPerMinute = 30
	}
	if cfg.OwnerPerMinute <= 0 {
		cfg.OwnerPerMinute = 30
	}
	if cfg.MaxConnsPerIP <= 0 {
		cfg.MaxConnsPerIP = 20
	}
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = 5000
	}
	if cfg.MessagesPerSecond <= 0 {
		cfg.MessagesPerSecond = 30
	}
	h := &Hub{
		cfg:      cfg,
		log:      cfg.Logger,
		svc:      svc,
		attempts: ratelimit.New(cfg.AttemptsPerMinute),
		lists:    ratelimit.New(cfg.ListsPerMinute),
		owners:   ratelimit.New(cfg.OwnerPerMinute),
		clients:  make(map[string]*client),
		perIP:    make(map[string]int),
	}
	h.upgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     h.checkOrigin,
	}
	return h
}

// checkOrigin blocks web pages from other sites. Native clients (the
// device) send no Origin header and are always accepted.
func (h *Hub) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	for _, allowed := range h.cfg.AllowedOrigins {
		if allowed == "*" || strings.EqualFold(allowed, origin) {
			return true
		}
	}
	h.log.Warn("origin rejected", "origin", origin)
	return false
}

// clientIP returns the address used as the rate-limit key. Behind a proxy
// every connection comes from the proxy, so the configured header is used
// instead. For X-Forwarded-For the right-most entry is taken: it is the
// one appended by our own proxy, while the left entries can be forged by
// the client.
func (h *Hub) clientIP(r *http.Request) string {
	if h.cfg.ClientIPHeader != "" {
		if v := r.Header.Get(h.cfg.ClientIPHeader); v != "" {
			parts := strings.Split(v, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return limitKey(ip)
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return limitKey(r.RemoteAddr)
	}
	return limitKey(host)
}

// limitKey is the key limits count by: the IPv4 address, or the /64 of an
// IPv6 address (one home or server gets a whole /64, so counting single
// IPv6 addresses would give an attacker 2^64 fresh keys).
func limitKey(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil || ip.To4() != nil {
		return addr
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// ServeHTTP upgrades the request to a WebSocket and starts the client loops.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if v := r.URL.Query().Get("v"); v != "" && v != models.ProtocolVersion {
		http.Error(w, "unsupported protocol version", http.StatusBadRequest)
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // the upgrader already wrote the HTTP error
	}
	c := &client{
		hub:    h,
		peerID: ids.New(),
		ip:     h.clientIP(r),
		conn:   conn,
		send:   make(chan models.Envelope, h.cfg.SendBuffer),
		done:   make(chan struct{}),
	}
	if !h.add(c) {
		msg := websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "too many connections")
		_ = conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(h.cfg.WriteWait))
		_ = conn.Close()
		return
	}
	h.log.Debug("peer connected", "peer_id", c.peerID)
	c.deliver(models.Envelope{Type: models.TypeHello, PeerID: c.peerID, ICEServers: h.svc.ICE.Servers(c.peerID)})
	go c.writeLoop()
	go c.readLoop()
}

// Shutdown closes every connection. New connections are refused.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	h.closed = true
	all := make([]*client, 0, len(h.clients))
	for _, c := range h.clients {
		all = append(all, c)
	}
	h.mu.Unlock()
	for _, c := range all {
		c.close()
	}
}

func (h *Hub) add(c *client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || len(h.clients) >= h.cfg.MaxConns || h.perIP[c.ip] >= h.cfg.MaxConnsPerIP {
		return false
	}
	h.clients[c.peerID] = c
	h.perIP[c.ip]++
	return true
}

func (h *Hub) lookup(peerID string) *client {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clients[peerID]
}

// remove forgets a disconnected client and notifies its session. The
// notifications are sent after releasing h.mu.
func (h *Hub) remove(c *client) {
	h.mu.Lock()
	if h.clients[c.peerID] == c {
		delete(h.clients, c.peerID)
		if h.perIP[c.ip]--; h.perIP[c.ip] <= 0 {
			delete(h.perIP, c.ip)
		}
	}
	h.mu.Unlock()

	res, err := h.svc.Sessions.Leave(context.Background(), c.peerID)
	if err != nil {
		h.log.Error("leave failed", "peer_id", c.peerID, "err", err)
		return
	}
	for _, peer := range res.Notify {
		h.sendTo(peer, models.Envelope{Type: models.TypePeerLeft, SessionID: res.SessionID, From: c.peerID})
	}
	h.log.Debug("peer disconnected", "peer_id", c.peerID, "was_owner", res.WasOwner)
}

func (h *Hub) sendTo(peerID string, env models.Envelope) {
	if target := h.lookup(peerID); target != nil {
		target.deliver(env)
	}
}

// handle routes one message received from c.
func (h *Hub) handle(c *client, env models.Envelope) {
	switch env.Type {
	case models.TypeRegister:
		h.handleRegister(c, env)
	case models.TypeReach:
		h.handleReach(c, env)
	case models.TypeClaim:
		h.handleClaim(c, env)
	case models.TypeSignal:
		h.handleSignal(c, env)
	case models.TypeRoomOpen:
		h.handleRoomOpen(c, env)
	case models.TypeRoomUpdate:
		h.handleRoomUpdate(c, env)
	case models.TypeRoomClose:
		h.handleRoomClose(c, env)
	case models.TypeRoomsList:
		h.handleRoomsList(c, env)
	case models.TypeInviteCreate:
		h.handleInviteCreate(c, env)
	case models.TypeJoin:
		h.handleJoin(c, env)
	default:
		c.sendError("unsupported message type")
	}
}

func (h *Hub) handleRegister(c *client, env models.Envelope) {
	if !h.owners.Allow(c.ip) {
		c.sendError(errRateLimited.Error())
		return
	}
	code, err := h.svc.Pairing.Register(context.Background(), c.peerID, env.App, env.DeviceID, env.DeviceSecret)
	if err != nil {
		c.sendError(h.publicError(err))
		return
	}
	c.deliver(models.Envelope{Type: models.TypeCode, Code: code})
}

func (h *Hub) handleClaim(c *client, env models.Envelope) {
	if !h.attempts.Allow(c.ip) {
		c.sendError(errRateLimited.Error())
		return
	}
	sess, err := h.svc.Pairing.Claim(context.Background(), c.peerID, env.App, env.Code)
	if err != nil {
		c.sendError(h.publicError(err))
		return
	}
	owner := h.ownerOrRollback(c, sess.OwnerPeerID)
	if owner == nil {
		c.sendError(services.ErrInvalidCode.Error())
		return
	}
	owner.deliver(models.Envelope{Type: models.TypePaired, SessionID: sess.ID, Remote: c.peerID})
	c.deliver(models.Envelope{Type: models.TypePaired, SessionID: sess.ID, Remote: owner.peerID})
}

// handleReach brings a browser back to a device it linked before. The
// device gets reached, not paired, so it knows the peer is unproven.
func (h *Hub) handleReach(c *client, env models.Envelope) {
	if !h.attempts.Allow(c.ip) {
		c.sendError(errRateLimited.Error())
		return
	}
	sess, err := h.svc.Pairing.Reach(context.Background(), c.peerID, env.App, env.DeviceID)
	if err != nil {
		c.sendError(h.publicError(err))
		return
	}
	owner := h.ownerOrRollback(c, sess.OwnerPeerID)
	if owner == nil {
		c.sendError(services.ErrDeviceOffline.Error())
		return
	}
	owner.deliver(models.Envelope{Type: models.TypeReached, SessionID: sess.ID, Remote: c.peerID})
	c.deliver(models.Envelope{Type: models.TypePaired, SessionID: sess.ID, Remote: owner.peerID})
}

// ownerOrRollback returns the owner's live connection. If the owner has
// just disconnected (its Leave has not run yet), c is detached again so
// it is not left in a dying session, and nil is returned.
func (h *Hub) ownerOrRollback(c *client, ownerPeerID string) *client {
	if owner := h.lookup(ownerPeerID); owner != nil {
		return owner
	}
	if _, err := h.svc.Sessions.Leave(context.Background(), c.peerID); err != nil {
		h.log.Error("rollback failed", "peer_id", c.peerID, "err", err)
	}
	return nil
}

func (h *Hub) handleRoomOpen(c *client, env models.Envelope) {
	if !h.owners.Allow(c.ip) {
		c.sendError(errRateLimited.Error())
		return
	}
	room, err := h.svc.Rooms.Open(context.Background(), c.peerID, env.Public, env.InviteOnly, env.Meta)
	if err != nil {
		c.sendError(h.publicError(err))
		return
	}
	c.deliver(models.Envelope{Type: models.TypeRoomOpened, RoomID: room.ID})
}

func (h *Hub) handleRoomUpdate(c *client, env models.Envelope) {
	if err := h.svc.Rooms.Update(context.Background(), c.peerID, env.RoomID, env.Meta); err != nil {
		c.sendError(h.publicError(err))
	}
}

func (h *Hub) handleRoomClose(c *client, env models.Envelope) {
	if err := h.svc.Rooms.Close(context.Background(), c.peerID, env.RoomID); err != nil {
		c.sendError(h.publicError(err))
	}
}

func (h *Hub) handleInviteCreate(c *client, env models.Envelope) {
	if !h.owners.Allow(c.ip) {
		c.sendError(errRateLimited.Error())
		return
	}
	invite, code, err := h.svc.Rooms.CreateInvite(context.Background(), c.peerID, env.RoomID)
	if err != nil {
		c.sendError(h.publicError(err))
		return
	}
	c.deliver(models.Envelope{Type: models.TypeInviteCreated, RoomID: env.RoomID, Invite: invite, Code: code})
}

func (h *Hub) handleRoomsList(c *client, env models.Envelope) {
	if !h.lists.Allow(c.ip) {
		c.sendError(errRateLimited.Error())
		return
	}
	rooms, err := h.svc.Rooms.List(context.Background(), env.App)
	if err != nil {
		c.sendError(h.publicError(err))
		return
	}
	c.deliver(models.Envelope{Type: models.TypeRooms, Rooms: rooms})
}

func (h *Hub) handleJoin(c *client, env models.Envelope) {
	if !h.attempts.Allow(c.ip) {
		c.sendError(errRateLimited.Error())
		return
	}
	var (
		sess models.Session
		room models.Room
		err  error
	)
	if env.Invite != "" || env.Code != "" {
		sess, room, err = h.svc.Rooms.JoinInvite(context.Background(), c.peerID, env.App, env.Invite, env.Code)
	} else {
		sess, room, err = h.svc.Rooms.Join(context.Background(), c.peerID, env.App, env.RoomID)
	}
	if err != nil {
		c.sendError(h.publicError(err))
		return
	}
	owner := h.ownerOrRollback(c, sess.OwnerPeerID)
	if owner == nil {
		c.sendError(services.ErrRoomNotFound.Error())
		return
	}
	owner.deliver(models.Envelope{Type: models.TypePeerJoined, SessionID: sess.ID, Remote: c.peerID, RoomID: room.ID})
	c.deliver(models.Envelope{Type: models.TypeJoined, SessionID: sess.ID, Remote: owner.peerID, RoomID: room.ID})
}

func (h *Hub) handleSignal(c *client, env models.Envelope) {
	if _, err := h.svc.Sessions.AuthorizeSignal(context.Background(), c.peerID, env.To); err != nil {
		c.sendError(h.publicError(err))
		return
	}
	target := h.lookup(env.To)
	if target == nil {
		c.sendError("peer not connected")
		return
	}
	// Build a fresh envelope: From is set by the server and nothing else
	// from the sender is copied.
	target.deliver(models.Envelope{Type: models.TypeSignal, From: c.peerID, Payload: env.Payload})
}

// publicErrors are safe to show to clients verbatim.
var publicErrors = []error{
	services.ErrInvalidCode,
	services.ErrAppNotAllowed,
	services.ErrInvalidDeviceID,
	services.ErrDeviceOffline,
	services.ErrAlreadyInSession,
	services.ErrCodeUnavailable,
	services.ErrNotInSession,
	services.ErrTargetForbidden,
	services.ErrNotOwner,
	services.ErrRoomLimit,
	services.ErrRoomNotFound,
	services.ErrInvalidMeta,
	services.ErrInvalidInvite,
	services.ErrDeviceTaken,
	errRateLimited,
}

func (h *Hub) publicError(err error) string {
	for _, e := range publicErrors {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	h.log.Error("internal error", "err", err)
	return "internal error"
}

// client is one WebSocket connection.
type client struct {
	hub    *Hub
	peerID string
	ip     string // rate-limit key
	conn   *websocket.Conn

	// send is never closed: closing it while deliver() writes would
	// panic. Shutdown is signalled by closing done instead.
	send      chan models.Envelope
	done      chan struct{}
	closeOnce sync.Once
}

func (c *client) close() {
	c.closeOnce.Do(func() { close(c.done) })
}

// deliver queues env without ever blocking. A slow client whose buffer is
// full loses the message instead of stalling the sender.
func (c *client) deliver(env models.Envelope) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.send <- env:
		return true
	default:
		c.hub.log.Warn("send buffer full, message dropped", "peer_id", c.peerID, "type", env.Type)
		return false
	}
}

func (c *client) sendError(msg string) {
	c.deliver(models.Envelope{Type: models.TypeError, Error: msg})
}

// readLoop is the only reader of the socket.
func (c *client) readLoop() {
	defer func() {
		c.hub.remove(c)
		c.close()
		_ = c.conn.Close()
	}()
	cfg := c.hub.cfg
	c.conn.SetReadLimit(cfg.MaxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(cfg.PongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(cfg.PongWait))
	})
	flood := rate.NewLimiter(rate.Limit(cfg.MessagesPerSecond), cfg.MessagesPerSecond*2)
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		if !flood.Allow() {
			// Best effort: the socket closes right after.
			c.sendError(errTooManyMessages.Error())
			c.hub.log.Warn("connection flooding, closed", "peer_id", c.peerID)
			return
		}
		var env models.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			c.sendError("invalid message")
			continue
		}
		c.hub.handle(c, env)
	}
}

// writeLoop is the only writer of the socket (gorilla/websocket does not
// support concurrent writers). It also sends the keep-alive pings.
func (c *client) writeLoop() {
	cfg := c.hub.cfg
	ticker := time.NewTicker(cfg.PingPeriod)
	defer func() {
		ticker.Stop()
		c.close()
		_ = c.conn.Close()
	}()
	for {
		select {
		case env := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(cfg.WriteWait))
			if err := c.conn.WriteJSON(env); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(cfg.WriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
			_ = c.conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(cfg.WriteWait))
			return
		}
	}
}

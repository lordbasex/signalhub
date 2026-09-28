// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lordbasex/signalhub/internal/models"
	"github.com/lordbasex/signalhub/internal/repositories"
	"github.com/lordbasex/signalhub/pkg/ids"
	"github.com/lordbasex/signalhub/pkg/pairingcode"
)

// Errors returned to clients by room operations.
var (
	ErrNotOwner     = errors.New("only the session owner can manage rooms")
	ErrRoomLimit    = errors.New("room limit reached")
	ErrRoomNotFound = errors.New("room not found")
	ErrInvalidMeta  = errors.New("invalid meta")
	// ErrInvalidInvite is the only answer to a bad invitation, like the
	// pairing code, so it gives no hints.
	ErrInvalidInvite = errors.New("invalid or expired invite")
)

// MaxMetaSize bounds the opaque room metadata kept in the directory.
const MaxMetaSize = 4 << 10

// RoomConfig configures RoomService. Zero values get defaults.
type RoomConfig struct {
	AllowedApps        []string               // empty allows every app
	MaxRoomsPerSession int                    // default 1
	Now                func() time.Time       // default time.Now
	NewRoomID          func() string          // default ids.NewUUIDv4
	NewInvite          func() string          // default ids.NewInvite
	NewCode            func() (string, error) // default pairingcode.New
}

// RoomService manages rooms (entry points into a session) and the
// directory of public rooms. It never reads the meta it stores.
type RoomService struct {
	repo repositories.Repository
	cfg  RoomConfig
	apps appPolicy
}

// NewRoomService builds the service and fills config defaults.
func NewRoomService(repo repositories.Repository, cfg RoomConfig) *RoomService {
	if cfg.MaxRoomsPerSession <= 0 {
		cfg.MaxRoomsPerSession = 1
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewRoomID == nil {
		cfg.NewRoomID = ids.NewUUIDv4
	}
	if cfg.NewInvite == nil {
		cfg.NewInvite = ids.NewInvite
	}
	if cfg.NewCode == nil {
		cfg.NewCode = pairingcode.New
	}
	return &RoomService{repo: repo, cfg: cfg, apps: newAppPolicy(cfg.AllowedApps)}
}

func validMeta(meta []byte) bool {
	if len(meta) == 0 {
		return true
	}
	return len(meta) <= MaxMetaSize && json.Valid(meta)
}

// Open creates a room in the session owned by ownerPeerID. The room ID
// is a server-generated UUID v4, so no client can pick or claim an ID.
// With inviteOnly, guests can join only through the room's invitation.
func (s *RoomService) Open(ctx context.Context, ownerPeerID string, public, inviteOnly bool, meta []byte) (models.Room, error) {
	sess, err := s.repo.SessionByPeer(ctx, ownerPeerID)
	if errors.Is(err, repositories.ErrNotFound) || (err == nil && sess.OwnerPeerID != ownerPeerID) {
		return models.Room{}, ErrNotOwner
	}
	if err != nil {
		return models.Room{}, fmt.Errorf("session lookup: %w", err)
	}
	if !validMeta(meta) {
		return models.Room{}, ErrInvalidMeta
	}
	room := models.Room{
		ID:          s.cfg.NewRoomID(),
		App:         sess.App,
		SessionID:   sess.ID,
		OwnerPeerID: ownerPeerID,
		Public:      public,
		InviteOnly:  inviteOnly,
		Meta:        meta,
		CreatedAt:   s.cfg.Now(),
	}
	err = s.repo.SaveRoom(ctx, room, s.cfg.MaxRoomsPerSession)
	if errors.Is(err, repositories.ErrConflict) {
		return models.Room{}, ErrRoomLimit
	}
	if errors.Is(err, repositories.ErrNotFound) {
		return models.Room{}, ErrNotOwner // the session ended meanwhile
	}
	if err != nil {
		return models.Room{}, fmt.Errorf("save room: %w", err)
	}
	return room, nil
}

// Update replaces the directory meta of a room owned by ownerPeerID.
func (s *RoomService) Update(ctx context.Context, ownerPeerID, roomID string, meta []byte) error {
	if !ids.ValidUUID(roomID) {
		return ErrRoomNotFound
	}
	if !validMeta(meta) {
		return ErrInvalidMeta
	}
	err := s.repo.UpdateRoomMeta(ctx, roomID, ownerPeerID, meta)
	if errors.Is(err, repositories.ErrNotFound) {
		return ErrRoomNotFound
	}
	return err
}

// Close removes a room owned by ownerPeerID from the directory. Peers
// already in the session stay connected: the room was only the door.
func (s *RoomService) Close(ctx context.Context, ownerPeerID, roomID string) error {
	if !ids.ValidUUID(roomID) {
		return ErrRoomNotFound
	}
	err := s.repo.DeleteRoom(ctx, roomID, ownerPeerID)
	if errors.Is(err, repositories.ErrNotFound) {
		return ErrRoomNotFound
	}
	return err
}

// CreateInvite gives a room owned by ownerPeerID a new invitation, which
// replaces the old one: a long random invite for links and a 9 digit code
// to type. Both are made here, so no client can pick one.
func (s *RoomService) CreateInvite(ctx context.Context, ownerPeerID, roomID string) (invite, code string, err error) {
	if !ids.ValidUUID(roomID) {
		return "", "", ErrRoomNotFound
	}
	for range 10 {
		invite = s.cfg.NewInvite()
		code, err = s.cfg.NewCode()
		if err != nil {
			return "", "", err
		}
		err = s.repo.SetRoomInvite(ctx, roomID, ownerPeerID, invite, code)
		if errors.Is(err, repositories.ErrConflict) {
			continue // a taken code: draw again
		}
		if errors.Is(err, repositories.ErrNotFound) {
			return "", "", ErrRoomNotFound
		}
		if err != nil {
			return "", "", fmt.Errorf("set invite: %w", err)
		}
		return invite, code, nil
	}
	return "", "", ErrCodeUnavailable
}

// List returns the public directory of an app. Private rooms never appear.
func (s *RoomService) List(ctx context.Context, app string) ([]models.RoomInfo, error) {
	if !s.apps.allowed(app) {
		return nil, ErrAppNotAllowed
	}
	rooms, err := s.repo.PublicRooms(ctx, app)
	if err != nil {
		return nil, fmt.Errorf("list rooms: %w", err)
	}
	out := make([]models.RoomInfo, 0, len(rooms))
	for _, r := range rooms {
		out = append(out, models.RoomInfo{RoomID: r.ID, Meta: r.Meta})
	}
	return out, nil
}

// Join adds peerID to the session that owns roomID. Public and private
// rooms behave the same here: knowing the ID is what grants entry.
func (s *RoomService) Join(ctx context.Context, peerID, app, roomID string) (models.Session, models.Room, error) {
	if !s.apps.allowed(app) {
		return models.Session{}, models.Room{}, ErrAppNotAllowed
	}
	if !ids.ValidUUID(roomID) {
		return models.Session{}, models.Room{}, ErrRoomNotFound
	}
	if _, err := s.repo.SessionByPeer(ctx, peerID); err == nil {
		return models.Session{}, models.Room{}, ErrAlreadyInSession
	}
	room, err := s.repo.GetRoom(ctx, roomID)
	if errors.Is(err, repositories.ErrNotFound) || (err == nil && (room.App != app || room.InviteOnly)) {
		return models.Session{}, models.Room{}, ErrRoomNotFound
	}
	if err != nil {
		return models.Session{}, models.Room{}, fmt.Errorf("get room: %w", err)
	}
	return s.attach(ctx, peerID, room)
}

// JoinInvite adds peerID to the session of the room whose invitation is
// invite, or whose code is code (spaces and dashes allowed).
func (s *RoomService) JoinInvite(ctx context.Context, peerID, app, invite, code string) (models.Session, models.Room, error) {
	if !s.apps.allowed(app) {
		return models.Session{}, models.Room{}, ErrAppNotAllowed
	}
	key, byCode := invite, false
	if invite == "" {
		c, ok := pairingcode.Normalize(code)
		if !ok {
			return models.Session{}, models.Room{}, ErrInvalidInvite
		}
		key, byCode = c, true
	} else if !ids.ValidInvite(invite) {
		return models.Session{}, models.Room{}, ErrInvalidInvite
	}
	if _, err := s.repo.SessionByPeer(ctx, peerID); err == nil {
		return models.Session{}, models.Room{}, ErrAlreadyInSession
	}
	room, err := s.repo.RoomByInvite(ctx, key, byCode)
	if errors.Is(err, repositories.ErrNotFound) || (err == nil && room.App != app) {
		return models.Session{}, models.Room{}, ErrInvalidInvite
	}
	if err != nil {
		return models.Session{}, models.Room{}, fmt.Errorf("room by invite: %w", err)
	}
	return s.attach(ctx, peerID, room)
}

// attach adds peerID to the session that owns room.
func (s *RoomService) attach(ctx context.Context, peerID string, room models.Room) (models.Session, models.Room, error) {
	sess, err := s.repo.AttachPeer(ctx, room.OwnerPeerID, peerID)
	if errors.Is(err, repositories.ErrNotFound) {
		return models.Session{}, models.Room{}, ErrRoomNotFound
	}
	if errors.Is(err, repositories.ErrConflict) {
		return models.Session{}, models.Room{}, ErrAlreadyInSession
	}
	if err != nil {
		return models.Session{}, models.Room{}, fmt.Errorf("attach peer: %w", err)
	}
	return sess, room, nil
}

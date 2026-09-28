// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

// Package repositories stores the ephemeral state of signalhub (pairing
// codes, sessions and rooms). Live WebSocket connections are NOT stored
// here: they live in the controllers.Hub.
package repositories

import (
	"context"
	"errors"

	"github.com/lordbasex/signalhub/internal/models"
)

var (
	// ErrNotFound means the requested item does not exist or has expired.
	ErrNotFound = errors.New("not found")
	// ErrConflict means the write would break a uniqueness rule.
	ErrConflict = errors.New("conflict")
)

// Repository is the storage contract. Stage 1 uses Memory; stage 2 will
// add a Redis implementation. Every method must be atomic on its own.
type Repository interface {
	// SavePairing stores a new pairing. It returns ErrConflict when the
	// code is already held by a non-expired pairing.
	SavePairing(ctx context.Context, p models.Pairing) error

	// TakePairing returns and deletes the pairing in one atomic step, so
	// a code can be redeemed only once. Missing or expired codes return
	// ErrNotFound.
	TakePairing(ctx context.Context, code string) (models.Pairing, error)

	// DeletePairingsByOwner removes every pending code of the owner.
	DeletePairingsByOwner(ctx context.Context, ownerPeerID string) error

	// EnsureOwnerSession returns the session owned by ownerPeerID,
	// creating it with newSessionID if it does not exist. It returns
	// ErrConflict when ownerPeerID is a member of another session or
	// already owns a session for a different app.
	EnsureOwnerSession(ctx context.Context, app, ownerPeerID, newSessionID string) (models.Session, error)

	// AttachPeer adds peerID to the session owned by ownerPeerID. It
	// returns ErrNotFound when the owner has no session, and ErrConflict
	// when peerID already belongs to a session or equals ownerPeerID.
	AttachPeer(ctx context.Context, ownerPeerID, peerID string) (models.Session, error)

	// SessionByPeer returns the session peerID belongs to, as owner or
	// member. It returns ErrNotFound otherwise.
	SessionByPeer(ctx context.Context, peerID string) (models.Session, error)

	// DetachPeer removes peerID from its session. If peerID is the owner,
	// the whole session and all its rooms are deleted. It returns the
	// session as it was before the removal and whether peerID was the
	// owner, or ErrNotFound when peerID is not in a session.
	DetachPeer(ctx context.Context, peerID string) (models.Session, bool, error)

	// SaveRoom stores a new room. It returns ErrConflict when the ID is
	// taken or when the owner already has maxPerOwner rooms, and
	// ErrNotFound when the room's session no longer exists.
	SaveRoom(ctx context.Context, r models.Room, maxPerOwner int) error

	// GetRoom returns a room by ID, or ErrNotFound.
	GetRoom(ctx context.Context, roomID string) (models.Room, error)

	// UpdateRoomMeta replaces the meta of a room owned by ownerPeerID.
	// It returns ErrNotFound when the room does not exist or belongs to
	// someone else.
	UpdateRoomMeta(ctx context.Context, roomID, ownerPeerID string, meta []byte) error

	// DeleteRoom removes a room owned by ownerPeerID. It returns
	// ErrNotFound when the room does not exist or belongs to someone else.
	DeleteRoom(ctx context.Context, roomID, ownerPeerID string) error

	// SetDeviceOwner records that ownerPeerID is the live connection of
	// deviceID in app, replacing an older one (a device that reconnected
	// before its old socket timed out). An owner connection holds one
	// device_id: a new one replaces the one it had. secretHash (SHA-256 of
	// the device secret) binds the device_id: while a binding lives,
	// another hash gets ErrConflict.
	SetDeviceOwner(ctx context.Context, app, deviceID, ownerPeerID string, secretHash []byte) error

	// OwnerByDevice returns the live owner connection of deviceID in app,
	// or ErrNotFound. The entry goes away when that owner disconnects.
	OwnerByDevice(ctx context.Context, app, deviceID string) (string, error)

	// SetRoomInvite replaces the invitation of a room owned by
	// ownerPeerID. It returns ErrNotFound when the room does not exist or
	// belongs to someone else, and ErrConflict when the invite or the code
	// is held by another room.
	SetRoomInvite(ctx context.Context, roomID, ownerPeerID, invite, code string) error

	// RoomByInvite returns the room whose current invitation is invite
	// (or, with byCode, whose code is that), or ErrNotFound.
	RoomByInvite(ctx context.Context, invite string, byCode bool) (models.Room, error)

	// PublicRooms lists the public rooms of an app, oldest first.
	PublicRooms(ctx context.Context, app string) ([]models.Room, error)
}

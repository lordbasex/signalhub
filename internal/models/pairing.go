// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

package models

import (
	"slices"
	"time"
)

// Pairing is a pending 9-digit code issued to a device (the session owner).
type Pairing struct {
	Code        string // 9 digits without separators
	App         string
	DeviceID    string
	OwnerPeerID string
	ExpiresAt   time.Time
}

// Expired reports whether the code can no longer be redeemed at now.
func (p Pairing) Expired(now time.Time) bool {
	return !now.Before(p.ExpiresAt)
}

// Session groups the owner (the device) and the peers linked to it.
type Session struct {
	ID          string
	App         string
	OwnerPeerID string
	Peers       []string // non-owner members in join order
}

// Has reports whether peerID is the owner or a member of the session.
func (s Session) Has(peerID string) bool {
	return peerID == s.OwnerPeerID || slices.Contains(s.Peers, peerID)
}

// Clone returns a deep copy so callers never share the Peers slice.
func (s Session) Clone() Session {
	s.Peers = slices.Clone(s.Peers)
	return s
}

// Room is an entry point into a session. Its ID is the link guests use
// (/r/<room_id>). Meta is opaque JSON owned by the application.
type Room struct {
	ID          string
	App         string
	SessionID   string
	OwnerPeerID string
	Public      bool
	// InviteOnly rooms admit guests only through their invitation.
	InviteOnly bool
	// Invite and InviteCode are the current invitation ("" = none).
	Invite     string
	InviteCode string
	Meta       []byte
	CreatedAt  time.Time
}

// Clone returns a deep copy so callers never share the Meta bytes.
func (r Room) Clone() Room {
	r.Meta = slices.Clone(r.Meta)
	return r
}

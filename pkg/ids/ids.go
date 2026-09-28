// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

// Package ids generates and validates identifiers used by signalhub.
package ids

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"regexp"
)

// New returns 128 random bits from crypto/rand encoded as 32 hex characters.
// It is used for session_id and peer_id, which must not be guessable.
func New() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand only fails if the OS entropy source is broken;
		// continuing with predictable IDs would be a security bug.
		panic("ids: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// NewUUIDv4 returns a random RFC 9562 version 4 UUID, for example
// "3f2b9c1e-7a4d-4e0b-9c1d-2e3f4a5b6c7d". It is used for room_id: 122
// random bits make room links impossible to guess or enumerate.
func NewUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ids: crypto/rand failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 9562 variant
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidUUID reports whether s has the canonical textual UUID format
// (8-4-4-4-12 hex digits). The version is not checked.
func ValidUUID(s string) bool {
	return uuidPattern.MatchString(s)
}

// NewInvite returns 128 random bits in base64url without padding (22
// characters), for room invitation links: short enough for a QR code and
// impossible to guess.
func NewInvite() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ids: crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

var invitePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

// ValidInvite reports whether s looks like an invitation from NewInvite.
func ValidInvite(s string) bool {
	return invitePattern.MatchString(s)
}

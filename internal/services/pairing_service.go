// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

// Package services holds the business rules of signalhub. Services know
// nothing about WebSockets or about the storage technology, so they can
// be tested with an in-memory repository.
package services

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/lordbasex/signalhub/internal/models"
	"github.com/lordbasex/signalhub/internal/repositories"
	"github.com/lordbasex/signalhub/pkg/ids"
	"github.com/lordbasex/signalhub/pkg/pairingcode"
)

// Errors returned to clients. Their text is part of the protocol.
var (
	// ErrInvalidCode is deliberately the same for every redeem failure
	// (unknown, expired, wrong app, own code) so attackers learn nothing.
	ErrInvalidCode      = errors.New("invalid or expired code")
	ErrAppNotAllowed    = errors.New("app not allowed")
	ErrInvalidDeviceID  = errors.New("invalid device_id")
	ErrAlreadyInSession = errors.New("peer already belongs to a session")
	ErrCodeUnavailable  = errors.New("could not allocate a pairing code")
	ErrDeviceOffline    = errors.New("device not connected")
	ErrDeviceTaken      = errors.New("device_id taken")
)

// validSecret reports whether s is 32 bytes in unpadded base64url (43
// characters), the device_secret format.
func validSecret(s string) bool {
	if len(s) != 43 {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(b) == 32
}

// maxCodeAttempts bounds retries when a random code collides with a live one.
const maxCodeAttempts = 5

// PairingConfig configures PairingService. Zero values get defaults.
type PairingConfig struct {
	TTL          time.Duration          // default 10 minutes
	AllowedApps  []string               // empty allows every app
	Now          func() time.Time       // default time.Now
	NewCode      func() (string, error) // default pairingcode.New
	NewSessionID func() string          // default ids.New
}

// PairingService implements register (device asks for a code) and claim
// (browser redeems the code), following the RFC 8628 device flow.
type PairingService struct {
	repo repositories.Repository
	cfg  PairingConfig
	apps appPolicy
}

// NewPairingService builds the service and fills config defaults.
func NewPairingService(repo repositories.Repository, cfg PairingConfig) *PairingService {
	if cfg.TTL <= 0 {
		cfg.TTL = 10 * time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewCode == nil {
		cfg.NewCode = pairingcode.New
	}
	if cfg.NewSessionID == nil {
		cfg.NewSessionID = ids.New
	}
	return &PairingService{repo: repo, cfg: cfg, apps: newAppPolicy(cfg.AllowedApps)}
}

// Register issues a new pairing code for the device connected as
// ownerPeerID and returns it formatted for display ("113 134 323").
// Any previous pending code of the same owner is invalidated. secret (the
// device's device_secret, required) binds deviceID: someone else
// registering it with another secret gets ErrDeviceTaken.
func (s *PairingService) Register(ctx context.Context, ownerPeerID, app, deviceID, secret string) (string, error) {
	if !s.apps.allowed(app) {
		return "", ErrAppNotAllowed
	}
	if !ids.ValidUUID(deviceID) {
		return "", ErrInvalidDeviceID
	}
	// Every device proves the device_id is its own with its secret.
	if !validSecret(secret) {
		return "", ErrInvalidDeviceID
	}
	sum := sha256.Sum256([]byte(secret))
	secretHash := sum[:]
	// The owner session is created on the first register, so the device
	// can open rooms before anyone links. A browser that already joined
	// someone else's session cannot become an owner at the same time.
	_, err := s.repo.EnsureOwnerSession(ctx, app, ownerPeerID, s.cfg.NewSessionID())
	if errors.Is(err, repositories.ErrConflict) {
		return "", ErrAlreadyInSession
	}
	if err != nil {
		return "", fmt.Errorf("ensure session: %w", err)
	}
	// Browsers that linked before can come back with reach.
	if err := s.repo.SetDeviceOwner(ctx, app, deviceID, ownerPeerID, secretHash); err != nil {
		if errors.Is(err, repositories.ErrConflict) {
			return "", ErrDeviceTaken
		}
		return "", fmt.Errorf("index device: %w", err)
	}
	if err := s.repo.DeletePairingsByOwner(ctx, ownerPeerID); err != nil {
		return "", fmt.Errorf("delete old codes: %w", err)
	}
	for i := 0; i < maxCodeAttempts; i++ {
		code, err := s.cfg.NewCode()
		if err != nil {
			return "", fmt.Errorf("new code: %w", err)
		}
		err = s.repo.SavePairing(ctx, models.Pairing{
			Code:        code,
			App:         app,
			DeviceID:    deviceID,
			OwnerPeerID: ownerPeerID,
			ExpiresAt:   s.cfg.Now().Add(s.cfg.TTL),
		})
		if errors.Is(err, repositories.ErrConflict) {
			continue // collision with a live code: try another one
		}
		if err != nil {
			return "", fmt.Errorf("save code: %w", err)
		}
		return pairingcode.Format(code), nil
	}
	return "", ErrCodeUnavailable
}

// Claim redeems a code typed by the user. On success the claimer joins
// the owner's session (created by Register) and the session is
// returned. The code is consumed even when the claim fails after lookup.
func (s *PairingService) Claim(ctx context.Context, claimerPeerID, app, rawCode string) (models.Session, error) {
	if !s.apps.allowed(app) {
		return models.Session{}, ErrAppNotAllowed
	}
	code, ok := pairingcode.Normalize(rawCode)
	if !ok {
		return models.Session{}, ErrInvalidCode
	}
	// Check before consuming the code, so a confused client does not
	// burn a valid code.
	if _, err := s.repo.SessionByPeer(ctx, claimerPeerID); err == nil {
		return models.Session{}, ErrAlreadyInSession
	}
	p, err := s.repo.TakePairing(ctx, code)
	if errors.Is(err, repositories.ErrNotFound) {
		return models.Session{}, ErrInvalidCode
	}
	if err != nil {
		return models.Session{}, fmt.Errorf("take code: %w", err)
	}
	if p.App != app || p.OwnerPeerID == claimerPeerID {
		return models.Session{}, ErrInvalidCode
	}
	sess, err := s.repo.AttachPeer(ctx, p.OwnerPeerID, claimerPeerID)
	if errors.Is(err, repositories.ErrNotFound) {
		return models.Session{}, ErrInvalidCode // the owner left meanwhile
	}
	if errors.Is(err, repositories.ErrConflict) {
		return models.Session{}, ErrAlreadyInSession
	}
	if err != nil {
		return models.Session{}, fmt.Errorf("attach peer: %w", err)
	}
	return sess, nil
}

// Reach attaches peerID, without a code, to the session of the device
// that registered deviceID. It proves nothing about the peer: the owner is
// told with reached and must authenticate it on its own channel.
func (s *PairingService) Reach(ctx context.Context, peerID, app, deviceID string) (models.Session, error) {
	if !s.apps.allowed(app) {
		return models.Session{}, ErrAppNotAllowed
	}
	if !ids.ValidUUID(deviceID) {
		return models.Session{}, ErrInvalidDeviceID
	}
	if _, err := s.repo.SessionByPeer(ctx, peerID); err == nil {
		return models.Session{}, ErrAlreadyInSession
	}
	owner, err := s.repo.OwnerByDevice(ctx, app, deviceID)
	if errors.Is(err, repositories.ErrNotFound) {
		return models.Session{}, ErrDeviceOffline
	}
	if err != nil {
		return models.Session{}, fmt.Errorf("device lookup: %w", err)
	}
	if owner == peerID {
		return models.Session{}, ErrAlreadyInSession
	}
	sess, err := s.repo.AttachPeer(ctx, owner, peerID)
	if errors.Is(err, repositories.ErrNotFound) {
		return models.Session{}, ErrDeviceOffline // the owner left meanwhile
	}
	if errors.Is(err, repositories.ErrConflict) {
		return models.Session{}, ErrAlreadyInSession
	}
	if err != nil {
		return models.Session{}, fmt.Errorf("attach peer: %w", err)
	}
	return sess, nil
}

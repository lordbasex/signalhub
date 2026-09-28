// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/lordbasex/signalhub/internal/models"
	"github.com/lordbasex/signalhub/internal/repositories"
)

// Errors returned to clients when a signal cannot be relayed.
var (
	ErrNotInSession    = errors.New("not in a session")
	ErrTargetForbidden = errors.New("target not allowed")
)

// SessionService enforces who may talk to whom and cleans up when a peer
// disconnects.
type SessionService struct {
	repo repositories.Repository
}

// NewSessionService builds the service.
func NewSessionService(repo repositories.Repository) *SessionService {
	return &SessionService{repo: repo}
}

// AuthorizeSignal checks that from may send a signal to to. Both must be
// in the same session, and the topology is a star: members may only
// address the owner, while the owner may address any member.
func (s *SessionService) AuthorizeSignal(ctx context.Context, from, to string) (models.Session, error) {
	if to == "" || to == from {
		return models.Session{}, ErrTargetForbidden
	}
	sess, err := s.repo.SessionByPeer(ctx, from)
	if errors.Is(err, repositories.ErrNotFound) {
		return models.Session{}, ErrNotInSession
	}
	if err != nil {
		return models.Session{}, fmt.Errorf("session lookup: %w", err)
	}
	if !sess.Has(to) {
		return models.Session{}, ErrTargetForbidden
	}
	if from != sess.OwnerPeerID && to != sess.OwnerPeerID {
		return models.Session{}, ErrTargetForbidden
	}
	return sess, nil
}

// LeaveResult tells the caller whom to notify with peer_left.
type LeaveResult struct {
	SessionID string
	WasOwner  bool
	Notify    []string // peer IDs that must receive peer_left
}

// Leave removes every trace of peerID: its pending codes and its session
// membership. When the owner leaves, the session ends and every member
// must be notified; when a member leaves, only the owner is notified
// (members never talk to each other). Leave is idempotent.
func (s *SessionService) Leave(ctx context.Context, peerID string) (LeaveResult, error) {
	if err := s.repo.DeletePairingsByOwner(ctx, peerID); err != nil {
		return LeaveResult{}, fmt.Errorf("delete codes: %w", err)
	}
	sess, wasOwner, err := s.repo.DetachPeer(ctx, peerID)
	if errors.Is(err, repositories.ErrNotFound) {
		return LeaveResult{}, nil
	}
	if err != nil {
		return LeaveResult{}, fmt.Errorf("detach peer: %w", err)
	}
	res := LeaveResult{SessionID: sess.ID, WasOwner: wasOwner}
	if wasOwner {
		res.Notify = sess.Peers
	} else {
		res.Notify = []string{sess.OwnerPeerID}
	}
	return res, nil
}

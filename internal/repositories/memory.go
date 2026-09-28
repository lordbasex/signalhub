// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

package repositories

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"slices"
	"sync"
	"time"

	"github.com/lordbasex/signalhub/internal/models"
)

// Memory is the stage 1 Repository: plain maps guarded by one mutex,
// plus a janitor goroutine that purges expired codes.
type Memory struct {
	now func() time.Time

	mu        sync.Mutex
	pairings  map[string]models.Pairing  // code -> pairing
	sessions  map[string]*models.Session // session_id -> session
	peerIndex map[string]string          // peer_id -> session_id (owner and members)
	rooms     map[string]*models.Room    // room_id -> room
	devices   map[string]string          // app + "\x00" + device_id -> owner peer_id
	ownerDev  map[string]string          // owner peer_id -> its device key
	bindings  map[string]deviceBinding   // device key -> hash of its secret
	invites   map[string]string          // invite -> room_id
	codes     map[string]string          // invite code -> room_id
}

var _ Repository = (*Memory)(nil)

// NewMemory creates an empty store. now is injectable for tests; nil
// means time.Now.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{
		now:       now,
		pairings:  make(map[string]models.Pairing),
		sessions:  make(map[string]*models.Session),
		peerIndex: make(map[string]string),
		rooms:     make(map[string]*models.Room),
		devices:   make(map[string]string),
		ownerDev:  make(map[string]string),
		bindings:  make(map[string]deviceBinding),
		invites:   make(map[string]string),
		codes:     make(map[string]string),
	}
}

func (m *Memory) SavePairing(_ context.Context, p models.Pairing) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.pairings[p.Code]; ok && !old.Expired(m.now()) {
		return ErrConflict
	}
	m.pairings[p.Code] = p
	return nil
}

func (m *Memory) TakePairing(_ context.Context, code string) (models.Pairing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pairings[code]
	if !ok {
		return models.Pairing{}, ErrNotFound
	}
	delete(m.pairings, code)
	if p.Expired(m.now()) {
		return models.Pairing{}, ErrNotFound
	}
	return p, nil
}

func (m *Memory) DeletePairingsByOwner(_ context.Context, ownerPeerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for code, p := range m.pairings {
		if p.OwnerPeerID == ownerPeerID {
			delete(m.pairings, code)
		}
	}
	return nil
}

func (m *Memory) EnsureOwnerSession(_ context.Context, app, ownerPeerID, newSessionID string) (models.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sid, ok := m.peerIndex[ownerPeerID]; ok {
		s := m.sessions[sid]
		if s.OwnerPeerID != ownerPeerID || s.App != app {
			return models.Session{}, ErrConflict
		}
		return s.Clone(), nil
	}
	if _, taken := m.sessions[newSessionID]; taken {
		return models.Session{}, ErrConflict
	}
	s := &models.Session{ID: newSessionID, App: app, OwnerPeerID: ownerPeerID}
	m.sessions[s.ID] = s
	m.peerIndex[ownerPeerID] = s.ID
	return s.Clone(), nil
}

func (m *Memory) AttachPeer(_ context.Context, ownerPeerID, peerID string) (models.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if peerID == ownerPeerID {
		return models.Session{}, ErrConflict
	}
	if _, busy := m.peerIndex[peerID]; busy {
		return models.Session{}, ErrConflict
	}
	sid, ok := m.peerIndex[ownerPeerID]
	if !ok || m.sessions[sid].OwnerPeerID != ownerPeerID {
		return models.Session{}, ErrNotFound
	}
	s := m.sessions[sid]
	s.Peers = append(s.Peers, peerID)
	m.peerIndex[peerID] = s.ID
	return s.Clone(), nil
}

func (m *Memory) SessionByPeer(_ context.Context, peerID string) (models.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sid, ok := m.peerIndex[peerID]
	if !ok {
		return models.Session{}, ErrNotFound
	}
	return m.sessions[sid].Clone(), nil
}

func (m *Memory) DetachPeer(_ context.Context, peerID string) (models.Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sid, ok := m.peerIndex[peerID]
	if !ok {
		return models.Session{}, false, ErrNotFound
	}
	s := m.sessions[sid]
	before := s.Clone()
	if s.OwnerPeerID == peerID {
		for _, p := range s.Peers {
			delete(m.peerIndex, p)
		}
		delete(m.peerIndex, peerID)
		delete(m.sessions, sid)
		for id, r := range m.rooms {
			if r.SessionID == sid {
				m.forgetInvite(r)
				delete(m.rooms, id)
			}
		}
		if key, ok := m.ownerDev[peerID]; ok {
			if m.devices[key] == peerID {
				delete(m.devices, key)
			}
			delete(m.ownerDev, peerID)
		}
		return before, true, nil
	}
	s.Peers = slices.DeleteFunc(s.Peers, func(p string) bool { return p == peerID })
	delete(m.peerIndex, peerID)
	return before, false, nil
}

func deviceKey(app, deviceID string) string { return app + "\x00" + deviceID }

// DeviceBindingTTL is how long a device_id stays bound to its secret
// after the device's last register, connected or not.
const DeviceBindingTTL = 30 * 24 * time.Hour

type deviceBinding struct {
	hash [sha256.Size]byte
	seen time.Time
}

func (m *Memory) SetDeviceOwner(_ context.Context, app, deviceID, ownerPeerID string, secretHash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := deviceKey(app, deviceID)
	now := m.now()
	b, bound := m.bindings[key]
	if bound && now.Sub(b.seen) >= DeviceBindingTTL {
		bound = false
	}
	switch {
	case bound && (len(secretHash) != sha256.Size || subtle.ConstantTimeCompare(secretHash, b.hash[:]) != 1):
		return ErrConflict
	case len(secretHash) == sha256.Size:
		var h [sha256.Size]byte
		copy(h[:], secretHash)
		m.bindings[key] = deviceBinding{hash: h, seen: now}
	}
	// One device_id per owner connection.
	if old, ok := m.ownerDev[ownerPeerID]; ok && old != key && m.devices[old] == ownerPeerID {
		delete(m.devices, old)
	}
	m.devices[key] = ownerPeerID
	m.ownerDev[ownerPeerID] = key
	return nil
}

func (m *Memory) OwnerByDevice(_ context.Context, app, deviceID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	owner, ok := m.devices[deviceKey(app, deviceID)]
	if !ok {
		return "", ErrNotFound
	}
	return owner, nil
}

func (m *Memory) SaveRoom(_ context.Context, r models.Room, maxPerOwner int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.rooms[r.ID]; taken {
		return ErrConflict
	}
	s, ok := m.sessions[r.SessionID]
	if !ok || s.OwnerPeerID != r.OwnerPeerID {
		return ErrNotFound
	}
	owned := 0
	for _, existing := range m.rooms {
		if existing.OwnerPeerID == r.OwnerPeerID {
			owned++
		}
	}
	if owned >= maxPerOwner {
		return ErrConflict
	}
	stored := r.Clone()
	m.rooms[r.ID] = &stored
	return nil
}

func (m *Memory) GetRoom(_ context.Context, roomID string) (models.Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[roomID]
	if !ok {
		return models.Room{}, ErrNotFound
	}
	return r.Clone(), nil
}

func (m *Memory) UpdateRoomMeta(_ context.Context, roomID, ownerPeerID string, meta []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[roomID]
	if !ok || r.OwnerPeerID != ownerPeerID {
		return ErrNotFound
	}
	r.Meta = slices.Clone(meta)
	return nil
}

func (m *Memory) DeleteRoom(_ context.Context, roomID, ownerPeerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[roomID]
	if !ok || r.OwnerPeerID != ownerPeerID {
		return ErrNotFound
	}
	m.forgetInvite(r)
	delete(m.rooms, roomID)
	return nil
}

// forgetInvite drops the indexes of a room's invitation. Callers hold mu.
func (m *Memory) forgetInvite(r *models.Room) {
	if r.Invite != "" {
		delete(m.invites, r.Invite)
	}
	if r.InviteCode != "" {
		delete(m.codes, r.InviteCode)
	}
}

func (m *Memory) SetRoomInvite(_ context.Context, roomID, ownerPeerID, invite, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[roomID]
	if !ok || r.OwnerPeerID != ownerPeerID {
		return ErrNotFound
	}
	if other, taken := m.invites[invite]; taken && other != roomID {
		return ErrConflict
	}
	if other, taken := m.codes[code]; taken && other != roomID {
		return ErrConflict
	}
	m.forgetInvite(r)
	r.Invite, r.InviteCode = invite, code
	m.invites[invite] = roomID
	m.codes[code] = roomID
	return nil
}

func (m *Memory) RoomByInvite(_ context.Context, invite string, byCode bool) (models.Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.invites
	if byCode {
		index = m.codes
	}
	id, ok := index[invite]
	if !ok {
		return models.Room{}, ErrNotFound
	}
	r, ok := m.rooms[id]
	if !ok {
		return models.Room{}, ErrNotFound
	}
	return r.Clone(), nil
}

func (m *Memory) PublicRooms(_ context.Context, app string) ([]models.Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.Room
	for _, r := range m.rooms {
		if r.Public && r.App == app {
			out = append(out, r.Clone())
		}
	}
	slices.SortFunc(out, func(a, b models.Room) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out, nil
}

// PurgeExpired deletes expired pairing codes (and device bindings nobody
// renewed in DeviceBindingTTL) and returns how many codes it deleted.
func (m *Memory) PurgeExpired() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	n := 0
	for code, p := range m.pairings {
		if p.Expired(now) {
			delete(m.pairings, code)
			n++
		}
	}
	for key, b := range m.bindings {
		if now.Sub(b.seen) >= DeviceBindingTTL {
			delete(m.bindings, key)
		}
	}
	return n
}

// RunJanitor calls PurgeExpired every interval until ctx is cancelled.
// Expired codes are already rejected by TakePairing; the janitor only
// frees memory.
func (m *Memory) RunJanitor(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.PurgeExpired()
		}
	}
}

// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

package services

import (
	"time"

	"github.com/lordbasex/signalhub/internal/models"
	"github.com/lordbasex/signalhub/pkg/turncred"
)

// ICEConfig configures ICEProvider. TURN is enabled only when both
// TURNURLs and TURNSecret are set.
type ICEConfig struct {
	STUNURLs   []string
	TURNURLs   []string
	TURNSecret string
	TURNTTL    time.Duration    // default 12h
	Now        func() time.Time // default time.Now
}

// ICEProvider builds the ICE server list sent in hello. TURN credentials
// are unique per peer and expire, so a leaked credential is short-lived
// and cannot be reused by someone else's peer_id.
type ICEProvider struct {
	cfg ICEConfig
}

// NewICEProvider builds the provider and fills config defaults.
func NewICEProvider(cfg ICEConfig) *ICEProvider {
	if cfg.TURNTTL <= 0 {
		cfg.TURNTTL = 12 * time.Hour
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &ICEProvider{cfg: cfg}
}

// Servers returns the STUN and TURN servers for peerID.
func (p *ICEProvider) Servers(peerID string) []models.ICEServer {
	var out []models.ICEServer
	if len(p.cfg.STUNURLs) > 0 {
		out = append(out, models.ICEServer{URLs: p.cfg.STUNURLs})
	}
	if len(p.cfg.TURNURLs) > 0 && p.cfg.TURNSecret != "" {
		expires := p.cfg.Now().Add(p.cfg.TURNTTL).Unix()
		user, cred := turncred.Credentials(p.cfg.TURNSecret, peerID, expires)
		out = append(out, models.ICEServer{URLs: p.cfg.TURNURLs, Username: user, Credential: cred})
	}
	return out
}

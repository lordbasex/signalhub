// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

// Package models holds the wire format of the signaling protocol and the
// domain types shared by services and repositories.
package models

import "encoding/json"

// ProtocolVersion is the only protocol version this server speaks.
// Clients send it in the URL: /ws?v=1.
const ProtocolVersion = "1"

// Message types of protocol v1.
const (
	TypeHello         = "hello"
	TypeRegister      = "register"
	TypeCode          = "code"
	TypeClaim         = "claim"
	TypePaired        = "paired"
	TypeReach         = "reach"
	TypeReached       = "reached"
	TypeRoomOpen      = "room_open"
	TypeRoomOpened    = "room_opened"
	TypeRoomUpdate    = "room_update"
	TypeRoomClose     = "room_close"
	TypeInviteCreate  = "invite_create"
	TypeInviteCreated = "invite_created"
	TypeRoomsList     = "rooms_list"
	TypeRooms         = "rooms"
	TypeJoin          = "join"
	TypeJoined        = "joined"
	TypePeerJoined    = "peer_joined"
	TypeSignal        = "signal"
	TypePeerLeft      = "peer_left"
	TypeError         = "error"
)

// ICEServer follows the shape of RTCIceServer in the browser WebRTC API.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// RoomInfo is one entry of the public room directory.
type RoomInfo struct {
	RoomID string          `json:"room_id"`
	Meta   json.RawMessage `json:"meta,omitempty"`
}

// Envelope is the single JSON shape of every WebSocket message.
// Empty fields are omitted. Meta and Payload are opaque: the server
// forwards them without reading them.
type Envelope struct {
	Type     string `json:"type"`
	App      string `json:"app,omitempty"`
	DeviceID string `json:"device_id,omitempty"`
	// DeviceSecret binds a device_id to whoever knows it (register only;
	// the server keeps its SHA-256, never the secret).
	DeviceSecret string          `json:"device_secret,omitempty"`
	Code         string          `json:"code,omitempty"`
	RoomID       string          `json:"room_id,omitempty"`
	Public       bool            `json:"public,omitempty"`
	InviteOnly   bool            `json:"invite_only,omitempty"`
	Invite       string          `json:"invite,omitempty"`
	Meta         json.RawMessage `json:"meta,omitempty"`
	Rooms        []RoomInfo      `json:"rooms,omitempty"`
	SessionID    string          `json:"session_id,omitempty"`
	PeerID       string          `json:"peer_id,omitempty"`
	Remote       string          `json:"remote,omitempty"`
	To           string          `json:"to,omitempty"`
	From         string          `json:"from,omitempty"`
	ICEServers   []ICEServer     `json:"ice_servers,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	Error        string          `json:"error,omitempty"`
}

// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

// Package turncred builds short-lived TURN credentials with the "TURN REST
// API" scheme supported by coturn (use-auth-secret / static-auth-secret).
// The server and coturn share a secret; no password is ever published.
package turncred

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"strconv"
)

// Credentials returns the TURN username and password for peerID, valid
// until expiresUnix (seconds since the epoch):
//
//	username   = "<expiresUnix>:<peerID>"
//	credential = base64(HMAC-SHA1(secret, username))
//
// coturn recomputes the HMAC with the same secret and rejects the
// username once the timestamp is in the past.
func Credentials(secret, peerID string, expiresUnix int64) (username, credential string) {
	username = strconv.FormatInt(expiresUnix, 10) + ":" + peerID
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(username))
	return username, base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

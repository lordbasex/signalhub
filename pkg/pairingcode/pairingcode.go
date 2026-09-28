// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

// Package pairingcode creates and parses the 9-digit codes a user types
// to link a browser with a device (RFC 8628 style "user code").
package pairingcode

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

// Digits is the length of a pairing code.
const Digits = 9

var space = big.NewInt(1_000_000_000) // 10^9 possible codes

// New returns a uniformly random 9-digit code, zero padded ("004213987").
func New() (string, error) {
	n, err := rand.Int(rand.Reader, space)
	if err != nil {
		return "", fmt.Errorf("pairingcode: %w", err)
	}
	return fmt.Sprintf("%09d", n.Int64()), nil
}

// Format groups a 9-digit code for display: "113134323" -> "113 134 323".
// Any other input is returned unchanged.
func Format(code string) string {
	if len(code) != Digits {
		return code
	}
	return code[0:3] + " " + code[3:6] + " " + code[6:9]
}

// Normalize removes the separators a human may type (spaces, dashes, dots)
// and reports whether the result is exactly 9 ASCII digits.
func Normalize(input string) (string, bool) {
	var b strings.Builder
	for _, r := range input {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.' || r == '\t':
			// ignore separators
		default:
			return "", false
		}
	}
	code := b.String()
	if len(code) != Digits {
		return "", false
	}
	return code, true
}

// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

package ids

import (
	"regexp"
	"testing"
)

func TestNewIsHexAndUnique(t *testing.T) {
	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := New()
		if !hex32.MatchString(id) {
			t.Fatalf("unexpected format: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id: %q", id)
		}
		seen[id] = true
	}
}

func TestValidUUID(t *testing.T) {
	cases := map[string]bool{
		"7f3c2a10-1b2c-4d3e-8f90-a1b2c3d4e5f6": true,
		"7F3C2A10-1B2C-4D3E-8F90-A1B2C3D4E5F6": true,
		"":                                     false,
		"not-a-uuid":                           false,
		"7f3c2a101b2c4d3e8f90a1b2c3d4e5f6":     false,
		"7f3c2a10-1b2c-4d3e-8f90-a1b2c3d4e5f":  false,
		"7f3c2a10-1b2c-4d3e-8f90-a1b2c3d4e5fg": false,
	}
	for in, want := range cases {
		if got := ValidUUID(in); got != want {
			t.Errorf("ValidUUID(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestNewUUIDv4(t *testing.T) {
	v4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := NewUUIDv4()
		if !v4.MatchString(id) || !ValidUUID(id) {
			t.Fatalf("not a v4 UUID: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate: %q", id)
		}
		seen[id] = true
	}
}

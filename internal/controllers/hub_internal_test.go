// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

package controllers

import "testing"

func TestLimitsCountIPv6ByTheirSlash64(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":          "203.0.113.7",
		"2001:db8:1:2:aaaa::1": "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::9": "2001:db8:1:2::/64",
		"::ffff:198.51.100.4":  "::ffff:198.51.100.4",
		"not-an-ip":            "not-an-ip",
	} {
		if got := limitKey(in); got != want {
			t.Errorf("limitKey(%q) = %q, want %q", in, got, want)
		}
	}
}

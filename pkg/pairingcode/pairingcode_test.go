// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

package pairingcode

import "testing"

func TestNew(t *testing.T) {
	for i := 0; i < 1000; i++ {
		code, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := Normalize(code); !ok {
			t.Fatalf("New returned an invalid code: %q", code)
		}
	}
}

func TestFormat(t *testing.T) {
	if got := Format("113134323"); got != "113 134 323" {
		t.Fatalf("got %q", got)
	}
	if got := Format("123"); got != "123" {
		t.Fatalf("short input should be unchanged, got %q", got)
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"113134323", "113134323", true},
		{"113 134 323", "113134323", true},
		{" 113-134.323 ", "113134323", true},
		{"004213987", "004213987", true},
		{"11313432", "", false},
		{"1131343231", "", false},
		{"11313432a", "", false},
		{"", "", false},
		{"١١٣١٣٤٣٢٣", "", false}, // non-ASCII digits are rejected
	}
	for _, c := range cases {
		got, ok := Normalize(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("Normalize(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

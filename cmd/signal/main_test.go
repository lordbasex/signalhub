// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

package main

import (
	"log/slog"
	"slices"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" || cfg.PairingTTL != 10*time.Minute || cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if !slices.Equal(cfg.STUNURLs, []string{"stun:stun.l.google.com:19302"}) {
		t.Fatalf("stun: %v", cfg.STUNURLs)
	}
	if cfg.TURNTTL != 12*time.Hour || cfg.RateLimitPerMin != 5 || cfg.MaxRoomsPerSession != 1 || cfg.ClientIPHeader != "" {
		t.Fatalf("unexpected v0.2 defaults: %+v", cfg)
	}
	if cfg.AllowedOrigins != nil || cfg.AllowedApps != nil || cfg.TURNURLs != nil {
		t.Fatalf("lists should be empty: %+v", cfg)
	}
}

func TestLoadConfigValues(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"ADDR":                  ":9000",
		"ALLOWED_ORIGINS":       "https://a.example, https://b.example ,",
		"ALLOWED_APPS":          "go-link",
		"PAIRING_TTL":           "5m",
		"LOG_LEVEL":             "debug",
		"TURN_URLS":             "turn:turn.example:3478?transport=udp,turns:turn.example:5349",
		"TURN_SECRET":           "0123456789abcdef0123456789abcdef",
		"OWNER_RATE_PER_MIN":    "12",
		"MAX_CONNS_PER_IP":      "4",
		"MAX_CONNS":             "100",
		"MSG_RATE_PER_SEC":      "7",
		"TURN_TTL":              "1h",
		"RATE_LIMIT_PER_MIN":    "10",
		"MAX_ROOMS_PER_SESSION": "3",
		"CLIENT_IP_HEADER":      "X-Forwarded-For",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.AllowedOrigins, []string{"https://a.example", "https://b.example"}) {
		t.Fatalf("origins: %v", cfg.AllowedOrigins)
	}
	if len(cfg.TURNURLs) != 2 || cfg.TURNSecret != "0123456789abcdef0123456789abcdef" || cfg.TURNTTL != time.Hour {
		t.Fatalf("turn: %+v", cfg)
	}
	if cfg.RateLimitPerMin != 10 || cfg.MaxRoomsPerSession != 3 || cfg.ClientIPHeader != "X-Forwarded-For" ||
		cfg.OwnerRatePerMin != 12 || cfg.MaxConnsPerIP != 4 || cfg.MaxConns != 100 || cfg.MsgRatePerSec != 7 {
		t.Fatalf("limits: %+v", cfg)
	}
	if cfg.Addr != ":9000" || cfg.PairingTTL != 5*time.Minute || cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("unexpected: %+v", cfg)
	}
}

func TestLoadConfigInvalid(t *testing.T) {
	for _, m := range []map[string]string{
		{"PAIRING_TTL": "soon"},
		// The example secret, or one too short to resist guessing.
		{"TURN_URLS": "turn:t.example:3478", "TURN_SECRET": "change-me"},
		{"TURN_URLS": "turn:t.example:3478", "TURN_SECRET": "short"},
		{"MAX_CONNS_PER_IP": "0"},
		{"PAIRING_TTL": "-1m"},
		{"LOG_LEVEL": "loud"},
		{"TURN_TTL": "0s"},
		{"RATE_LIMIT_PER_MIN": "0"},
		{"MAX_ROOMS_PER_SESSION": "many"},
		{"TURN_URLS": "turn:turn.example:3478"},
	} {
		if _, err := loadConfig(env(m)); err == nil {
			t.Errorf("expected error for %v", m)
		}
	}
}

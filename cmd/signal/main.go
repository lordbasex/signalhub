// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

// Command signal runs the signalhub WebSocket server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lordbasex/signalhub/internal/controllers"
	"github.com/lordbasex/signalhub/internal/repositories"
	"github.com/lordbasex/signalhub/internal/services"
)

type config struct {
	Addr               string
	AllowedOrigins     []string
	AllowedApps        []string
	PairingTTL         time.Duration
	STUNURLs           []string
	TURNURLs           []string
	TURNSecret         string
	TURNTTL            time.Duration
	RateLimitPerMin    int
	OwnerRatePerMin    int
	MaxConnsPerIP      int
	MaxConns           int
	MsgRatePerSec      int
	MaxRoomsPerSession int
	ClientIPHeader     string
	LogLevel           slog.Level
}

// loadConfig reads the environment. getenv is injectable for tests.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		Addr:           envOr(getenv, "ADDR", ":8080"),
		AllowedOrigins: splitList(getenv("ALLOWED_ORIGINS")),
		AllowedApps:    splitList(getenv("ALLOWED_APPS")),
		STUNURLs:       splitList(envOr(getenv, "STUN_URLS", "stun:stun.l.google.com:19302")),
		TURNURLs:       splitList(getenv("TURN_URLS")),
		TURNSecret:     strings.TrimSpace(getenv("TURN_SECRET")),
		ClientIPHeader: strings.TrimSpace(getenv("CLIENT_IP_HEADER")),
	}
	var err error
	if cfg.PairingTTL, err = positiveDuration(getenv, "PAIRING_TTL", "10m"); err != nil {
		return config{}, err
	}
	if cfg.TURNTTL, err = positiveDuration(getenv, "TURN_TTL", "12h"); err != nil {
		return config{}, err
	}
	if cfg.RateLimitPerMin, err = positiveInt(getenv, "RATE_LIMIT_PER_MIN", "5"); err != nil {
		return config{}, err
	}
	if cfg.MaxRoomsPerSession, err = positiveInt(getenv, "MAX_ROOMS_PER_SESSION", "1"); err != nil {
		return config{}, err
	}
	if cfg.OwnerRatePerMin, err = positiveInt(getenv, "OWNER_RATE_PER_MIN", "30"); err != nil {
		return config{}, err
	}
	if cfg.MaxConnsPerIP, err = positiveInt(getenv, "MAX_CONNS_PER_IP", "20"); err != nil {
		return config{}, err
	}
	if cfg.MaxConns, err = positiveInt(getenv, "MAX_CONNS", "5000"); err != nil {
		return config{}, err
	}
	if cfg.MsgRatePerSec, err = positiveInt(getenv, "MSG_RATE_PER_SEC", "30"); err != nil {
		return config{}, err
	}
	if len(cfg.TURNURLs) > 0 && cfg.TURNSecret == "" {
		return config{}, errors.New("TURN_URLS requires TURN_SECRET")
	}
	// The secret signs every TURN credential: the example value or a short
	// one would let anyone make their own.
	if cfg.TURNSecret != "" && (cfg.TURNSecret == "change-me" || len(cfg.TURNSecret) < 32) {
		return config{}, errors.New("TURN_SECRET must be a random value of at least 32 characters (openssl rand -hex 32)")
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(envOr(getenv, "LOG_LEVEL", "info"))); err != nil {
		return config{}, fmt.Errorf("invalid LOG_LEVEL: %w", err)
	}
	return cfg, nil
}

func positiveDuration(getenv func(string) string, key, def string) (time.Duration, error) {
	d, err := time.ParseDuration(envOr(getenv, key, def))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid %s %q", key, getenv(key))
	}
	return d, nil
}

func positiveInt(getenv func(string) string, key, def string) (int, error) {
	n, err := strconv.Atoi(envOr(getenv, key, def))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid %s %q", key, getenv(key))
	}
	return n, nil
}

func envOr(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "signal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	if len(cfg.AllowedOrigins) == 0 {
		logger.Warn("ALLOWED_ORIGINS is empty: browsers will be rejected, only native clients can connect")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	repo := repositories.NewMemory(time.Now)
	go repo.RunJanitor(ctx, time.Minute)

	if len(cfg.TURNURLs) == 0 {
		logger.Warn("TURN_URLS is empty: peers behind strict NATs will not connect")
	}
	hub := controllers.NewHub(controllers.HubConfig{
		AllowedOrigins:    cfg.AllowedOrigins,
		Logger:            logger,
		AttemptsPerMinute: cfg.RateLimitPerMin,
		OwnerPerMinute:    cfg.OwnerRatePerMin,
		MaxConnsPerIP:     cfg.MaxConnsPerIP,
		MaxConns:          cfg.MaxConns,
		MessagesPerSecond: cfg.MsgRatePerSec,
		ClientIPHeader:    cfg.ClientIPHeader,
	}, controllers.Services{
		Pairing:  services.NewPairingService(repo, services.PairingConfig{TTL: cfg.PairingTTL, AllowedApps: cfg.AllowedApps}),
		Sessions: services.NewSessionService(repo),
		Rooms:    services.NewRoomService(repo, services.RoomConfig{AllowedApps: cfg.AllowedApps, MaxRoomsPerSession: cfg.MaxRoomsPerSession}),
		ICE: services.NewICEProvider(services.ICEConfig{
			STUNURLs:   cfg.STUNURLs,
			TURNURLs:   cfg.TURNURLs,
			TURNSecret: cfg.TURNSecret,
			TURNTTL:    cfg.TURNTTL,
		}),
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           controllers.NewRouter(hub),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("signalhub listening", "addr", cfg.Addr)

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Shutdown stops accepting new requests; hijacked WebSocket
	// connections are not tracked by net/http, so the hub closes them.
	err = srv.Shutdown(shutdownCtx)
	hub.Shutdown()
	return err
}

// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

package controllers

import "net/http"

// NewRouter exposes the WebSocket endpoint and the health check.
func NewRouter(hub *Hub) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /ws", hub)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

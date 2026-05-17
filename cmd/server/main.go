package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sendbyai/sendbyai/internal/filter"
	"github.com/sendbyai/sendbyai/internal/hub"
)

func main() {
	addr := envOr("ADDR", ":8080")

	unsmile, err := filter.NewUnsmile()
	if err != nil {
		slog.Error("unsmile filter init failed", "err", err)
		os.Exit(1)
	}
	defer unsmile.Close()

	ollama := filter.NewOllama()

	// Two-stage pipeline: unsmile blocks synchronously; ollama re-judges
	// quarantined messages async after optimistic broadcast.
	h := hub.New(unsmile, ollama)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go h.Run(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.ServeWS)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("SendByAI listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down gracefully…")

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		slog.Error("shutdown error", "err", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

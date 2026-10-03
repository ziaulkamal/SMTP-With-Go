// Command mailcore runs the mail API server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ziaulkamal/mailcore/internal/api"
	"github.com/ziaulkamal/mailcore/internal/config"
	"github.com/ziaulkamal/mailcore/internal/job"
	"github.com/ziaulkamal/mailcore/internal/mailer"
)

var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)
	api.Version = version

	if len(cfg.APIKeys) == 0 {
		log.Warn("MAILCORE_API_KEYS is empty: the API is open to anyone who can reach it. Only do this in local development.")
	}

	dialer := mailer.SMTPDialer{HeloName: cfg.HeloName, Timeout: cfg.SMTPTimeout}
	jobs := job.NewManager(dialer, job.Options{
		Workers:        cfg.JobWorkers,
		QueueSize:      cfg.JobQueueSize,
		TTL:            cfg.JobTTL,
		CallbackSecret: cfg.CallbackSecret,
		Logger:         log,
	})
	jobs.Start()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(cfg, dialer, jobs, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Info("mailcore listening", "addr", cfg.Addr, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server failed", "error", err)
			stop()
		}
	}()
	<-ctx.Done()

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown", "error", err)
	}
	if err := jobs.Shutdown(shutdownCtx); err != nil {
		log.Error("job manager shutdown", "error", err)
	}
}

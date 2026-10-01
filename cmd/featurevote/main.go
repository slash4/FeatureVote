// Command featurevote runs the FeatureVote HTTP service.
//
// Configuration is read from FV_* environment variables (see
// docs/INTEGRATION.md). Migrations are applied at boot.
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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slash4/featurevote/internal/api"
	"github.com/slash4/featurevote/internal/config"
	"github.com/slash4/featurevote/internal/migrate"
	"github.com/slash4/featurevote/internal/store"
	"github.com/slash4/featurevote/internal/web"
	"github.com/slash4/featurevote/migrations"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return errors.New("connect to database: invalid FV_DATABASE_URL")
	}
	defer pool.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = pool.Ping(pingCtx)
	cancel()
	if err != nil {
		return err
	}
	if _, err := migrate.Up(ctx, pool, migrations.FS, log); err != nil {
		return err
	}

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: api.NewServer(api.Options{
			Store:    store.New(pool),
			Config:   cfg,
			WidgetJS: web.WidgetJS,
			Logger:   log,
		}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("featurevote listening", "addr", cfg.ListenAddr, "issuer", cfg.HostIssuer,
			"audience", cfg.Audience,
			"allowed_origins", cfg.AllowedOrigins)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

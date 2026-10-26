// Command chatchat-server serves the chatchat site and its API from a
// single process.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"chatchat"
	"chatchat/internal/auth"
	"chatchat/internal/config"
	"chatchat/internal/server"
	"chatchat/internal/store"
)

// Set at build time with -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "unknown"
)

// sweepInterval is how often idle and expired chats are destroyed and
// lapsed leases announced.
const sweepInterval = 5 * time.Second

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("chatchat-server %s (%s)\n", version, commit)
		return
	}
	if err := run(*configPath); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	if cfg.DevUser != "" {
		slog.Warn("dev_user is set: OIDC is disabled and every browser request is signed in as this user", "user", cfg.DevUser)
	}
	authn := auth.New(st, auth.Config{
		PublicURL:    cfg.PublicURL,
		Issuer:       cfg.OIDC.Issuer,
		ClientID:     cfg.OIDC.ClientID,
		ClientSecret: cfg.OIDC.ClientSecret,
		DevUser:      cfg.DevUser,
	})
	srv, err := server.New(st, authn, chatchat.Frontend(), server.Options{
		Version: version, PublicURL: cfg.PublicURL, Limits: cfg.Limits,
	})
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.RunSweeper(ctx, sweepInterval)

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Listen, "data_dir", cfg.DataDir, "version", version)
		errc <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}
	// Long polls hold connections for up to wait_max; cut them off rather
	// than wait for them.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return httpSrv.Close()
	}
	return nil
}

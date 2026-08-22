// Command ao-cloud runs the hosted AO control-plane API.
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

	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/auth"
	cloudconfig "github.com/aoagents/agent-orchestrator/backend/internal/cloud/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/httpapi"
	cloudpostgres "github.com/aoagents/agent-orchestrator/backend/internal/cloud/postgres"
	cloudruntime "github.com/aoagents/agent-orchestrator/backend/internal/cloud/runtime/daytona"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("Cloud control plane stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := cloudconfig.Load()
	if err != nil {
		return err
	}
	store, err := cloudpostgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	google, err := auth.NewGoogleVerifier(ctx, cfg.GoogleIssuer, cfg.GoogleJWKSURL, cfg.GoogleClientIDs)
	if err != nil {
		return err
	}
	accessTokens, err := auth.NewAccessTokenManager(
		cfg.AccessTokenKey,
		cfg.AccessTokenIssuer,
		cfg.AccessTokenAudience,
		cfg.AccessTokenTTL,
	)
	if err != nil {
		return err
	}
	options := httpapi.Options{
		Store:           store,
		Google:          google,
		AllowedEmails:   cfg.AllowedEmails,
		AccessTokens:    accessTokens,
		RefreshTokenTTL: cfg.RefreshTokenTTL,
		Logger:          logger,
		WorkspaceStore:  store,
		SessionStore:    store,
		PublicURL:       cfg.PublicURL,
	}
	var workspaceProvider *cloudruntime.Provider
	if cfg.DaytonaAPIKey != "" {
		workspaceProvider, err = cloudruntime.New(cloudruntime.Config{
			APIKey:       cfg.DaytonaAPIKey,
			APIURL:       cfg.DaytonaAPIURL,
			Target:       cfg.DaytonaTarget,
			AOBinaryPath: cfg.SandboxAOBinaryPath,
			GitHubToken:  cfg.GitHubToken,
		})
		if err != nil {
			return err
		}
		options.Workspaces = workspaceProvider
		options.SessionRuntimes = workspaceProvider
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = workspaceProvider.Close(closeCtx)
		}()
	}
	api, err := httpapi.New(options)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("Cloud control plane listening", "address", cfg.Address)
		errCh <- server.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	databaseURL := strings.TrimSpace(os.Getenv("AO_CLOUD_MIGRATION_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("AO_CLOUD_DATABASE_URL"))
	}
	if databaseURL == "" {
		logger.Error("migration database URL is required")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer cancel()
	timeout := 15 * time.Minute
	if value := strings.TrimSpace(os.Getenv("AO_CLOUD_MIGRATION_TIMEOUT")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			logger.Error("invalid migration timeout", "value", value)
			os.Exit(1)
		}
		timeout = parsed
	}
	migrationContext, cancelMigration := context.WithTimeout(ctx, timeout)
	defer cancelMigration()
	started := time.Now()
	release := strings.TrimSpace(os.Getenv("AO_CLOUD_RELEASE"))
	logger.Info("AO Cloud database migrations started", "release", release, "timeout", timeout)
	if err := postgres.Migrate(migrationContext, databaseURL); err != nil {
		if !errors.Is(err, context.Canceled) {
			logger.Error(
				"migrate AO Cloud database",
				"error",
				err,
				"release",
				release,
				"duration",
				time.Since(started),
			)
		}
		os.Exit(1)
	}
	runtimeUser := strings.TrimSpace(os.Getenv("AO_CLOUD_RUNTIME_DATABASE_USER"))
	if runtimeUser != "" {
		if err := postgres.GrantRuntimeRole(migrationContext, databaseURL, runtimeUser); err != nil {
			logger.Error("grant runtime database privileges", "error", err)
			os.Exit(1)
		}
	}
	logger.Info(
		"AO Cloud database migrations complete",
		"release",
		release,
		"duration",
		time.Since(started),
	)
}

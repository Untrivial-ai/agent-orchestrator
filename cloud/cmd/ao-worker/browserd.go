package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/vmbrowser"
)

const (
	defaultBrowserIdleTimeout = 30 * time.Minute
	chromiumPathEnv           = "AO_CLOUD_CHROMIUM_PATH"
	agentBrowserPathEnv       = "AO_CLOUD_AGENT_BROWSER_PATH"
)

// BrowserdOptions configures the in-VM browser service.
type BrowserdOptions struct {
	DataDir     string
	SessionID   string
	Logger      *slog.Logger
	NewEngine   func(root, sessionID string) vmbrowser.EngineLike
	IdleTimeout time.Duration
}

// startBrowserd binds the loopback listener, mints the session capability,
// and serves the desktop browser contract until the returned stop function is
// called. The returned env map carries AO_BROWSER_CAPABILITY and
// AO_BROWSER_API_URL for the agent process.
//
// Binding synchronously (before the agent terminal opens) removes the env
// race a goroutine-based start would have.
func startBrowserd(ctx context.Context, opts BrowserdOptions) (map[string]string, func(context.Context) error, error) {
	if opts.DataDir == "" || opts.SessionID == "" {
		return nil, nil, errors.New("browserd requires data dir and session id")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = defaultBrowserIdleTimeout
	}
	authority := browsercontract.NewAuthority()
	token, verifier, err := authority.Issue(opts.SessionID)
	if err != nil {
		return nil, nil, fmt.Errorf("mint browser capability: %w", err)
	}
	root := filepath.Join(opts.DataDir, "browser", opts.SessionID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create browser root: %w", err)
	}
	newEngine := opts.NewEngine
	if newEngine == nil {
		newEngine = defaultBrowserEngine
	}
	engine := newEngine(root, opts.SessionID)
	service := vmbrowser.NewService(vmbrowser.ServiceOptions{
		SessionID:          opts.SessionID,
		CapabilityVerifier: verifier,
		Engine:             engine,
		Logger:             opts.Logger,
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("listen browserd loopback: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	env := map[string]string{
		"AO_BROWSER_CAPABILITY": token,
		"AO_BROWSER_API_URL":    "http://127.0.0.1:" + strconv.Itoa(port),
	}

	server := &http.Server{Handler: service.Handler()}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	watchCtx, stopWatch := context.WithCancel(context.WithoutCancel(ctx))
	go watchBrowserIdle(watchCtx, opts.Logger, engine, service, opts.IdleTimeout)

	stop := func(shutdownCtx context.Context) error {
		stopWatch()
		shutdownCtx, cancel := context.WithTimeout(shutdownCtx, 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		err := <-serveErr
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		closeErr := engine.Close(context.Background())
		return errors.Join(err, closeErr)
	}
	return env, stop, nil
}

// watchBrowserIdle stops the supervised browser after IdleTimeout without
// served commands (spec D7). The next command lazily restarts it.
func watchBrowserIdle(
	ctx context.Context,
	logger *slog.Logger,
	engine vmbrowser.EngineLike,
	service *vmbrowser.Service,
	idleTimeout time.Duration,
) {
	ticker := time.NewTicker(idleTimeout)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			last := service.LastActivity()
			// Zero means no command was ever served: idle since start.
			if !last.IsZero() && time.Since(last) < idleTimeout {
				continue
			}
			logger.Info("browser idle; stopping chromium until next command", "idle", idleTimeout.String())
			stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := engine.Close(stopCtx); err != nil {
				logger.Warn("idle browser stop failed", "error", err)
			}
			cancel()
		}
	}
}

// defaultBrowserEngine builds the production engine: supervised Chromium
// under the browser root plus the pinned agent-browser binary.
func defaultBrowserEngine(root, sessionID string) vmbrowser.EngineLike {
	chromiumPath := os.Getenv(chromiumPathEnv)
	if chromiumPath == "" {
		chromiumPath = "/usr/bin/chromium"
	}
	binaryPath := os.Getenv(agentBrowserPathEnv)
	if binaryPath == "" {
		binaryPath = "/usr/local/lib/ao/agent-browser"
	}
	chromium := vmbrowser.NewChromium(vmbrowser.ChromiumOptions{
		BinaryPath:  chromiumPath,
		UserDataDir: filepath.Join(root, "profile"),
		Logger:      slog.Default(),
	})
	return vmbrowser.NewEngine(chromium, nil, vmbrowser.EngineOptions{
		BinaryPath: binaryPath,
		Root:       root,
		SessionID:  sessionID,
		Logger:     slog.Default(),
	})
}

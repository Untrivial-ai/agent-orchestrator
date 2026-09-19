// ao-browserd-standalone runs the production in-VM browser service (the
// same vmbrowser service, engine, and Chromium supervisor ao-worker embeds)
// without a control plane, for manual verification on VMs (CreateOS) and
// dev boxes. It prints the agent env as one JSON line on stdout, then serves
// until killed. Developer tool only; not shipped in worker images.
//
// Env: AO_SESSION_ID, AO_DATA_DIR, AO_CLOUD_CHROMIUM_PATH,
// AO_CLOUD_AGENT_BROWSER_PATH, AO_BROWSERD_LISTEN (default 127.0.0.1:0).
package main

import (
	"context"
	"encoding/json"
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

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	sessionID := envOr("AO_SESSION_ID", "sbx-session-1")
	dataDir := envOr("AO_DATA_DIR", "/tmp/browserd")
	chromiumPath := envOr("AO_CLOUD_CHROMIUM_PATH", "/usr/bin/chromium")
	agentBrowserPath := envOr("AO_CLOUD_AGENT_BROWSER_PATH", "/usr/local/lib/ao/agent-browser")
	listen := envOr("AO_BROWSERD_LISTEN", "127.0.0.1:0")

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	authority := browsercontract.NewAuthority()
	token, verifier, err := authority.Issue(sessionID)
	if err != nil {
		logger.Error("mint capability", "error", err)
		os.Exit(1)
	}
	root := filepath.Join(dataDir, "browser", sessionID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		logger.Error("create root", "error", err)
		os.Exit(1)
	}
	chromium := vmbrowser.NewChromium(vmbrowser.ChromiumOptions{
		BinaryPath:  chromiumPath,
		UserDataDir: filepath.Join(root, "profile"),
		Logger:      logger,
	})
	engine := vmbrowser.NewEngine(chromium, nil, vmbrowser.EngineOptions{
		BinaryPath: agentBrowserPath,
		Root:       root,
		SessionID:  sessionID,
		Logger:     logger,
	})
	service := vmbrowser.NewService(vmbrowser.ServiceOptions{
		SessionID:          sessionID,
		CapabilityVerifier: verifier,
		Engine:             engine,
		Logger:             logger,
	})
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		logger.Error("listen", "error", err)
		os.Exit(1)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	envOut, _ := json.Marshal(map[string]string{
		"sessionId":             sessionID,
		"AO_BROWSER_CAPABILITY": token,
		"AO_BROWSER_API_URL":    "http://127.0.0.1:" + strconv.Itoa(port),
	})
	fmt.Println(string(envOut))

	server := &http.Server{Handler: service.Handler()}
	ctx, stop := context.WithTimeout(context.Background(), 24*time.Hour)
	defer stop()
	if err := server.Serve(listener); err != nil && ctx.Err() == nil {
		logger.Error("serve", "error", err)
		os.Exit(1)
	}
}

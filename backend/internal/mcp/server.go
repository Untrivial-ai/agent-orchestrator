package mcp

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options configures how the MCP server is exposed.
type Options struct {
	// Version is reported in the MCP implementation metadata.
	Version string
	// ListenAddr, when non-empty, serves streamable HTTP on that address
	// instead of stdio. Must resolve to a loopback address.
	ListenAddr string
	// Logger receives operational messages. Callers should pass a stderr
	// logger for stdio so protocol bytes stay on stdout.
	Logger *log.Logger
}

// NewServer builds an AO MCP server wired to api.
func NewServer(api DaemonAPI, opts Options) *mcpsdk.Server {
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = "dev"
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "ao",
		Title:   "Agent Orchestrator",
		Version: version,
	}, nil)
	registerTools(server, api)
	return server
}

// Run starts the MCP server until ctx is cancelled or the transport closes.
func Run(ctx context.Context, api DaemonAPI, opts Options) error {
	server := NewServer(api, opts)
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}

	addr := strings.TrimSpace(opts.ListenAddr)
	if addr == "" {
		return server.Run(ctx, &mcpsdk.StdioTransport{})
	}

	if err := requireLoopbackAddr(addr); err != nil {
		return err
	}
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return server
	}, &mcpsdk.StreamableHTTPOptions{Stateless: true})

	httpServer := &http.Server{
		Addr:    addr,
		Handler: handler,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	logger.Printf("AO MCP server listening on http://%s (streamable HTTP, loopback only)", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpServer.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		err := <-errCh
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func requireLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Allow bare ports like ":8787" by treating missing host as localhost.
		if strings.HasPrefix(addr, ":") {
			host = "127.0.0.1"
		} else {
			return fmt.Errorf("invalid listen address %q: %w", addr, err)
		}
	}
	host = strings.TrimSpace(host)
	if host == "" || host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("MCP HTTP listen address must be loopback (got %q); use 127.0.0.1 or localhost", host)
	}
	return nil
}

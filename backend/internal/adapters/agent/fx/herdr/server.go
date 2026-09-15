package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	maxMessageBytes = 16 * 1024
	maxConnections  = 32
	clientDuration  = 2 * time.Second
)

// Server owns a private Unix socket and submits native reports exclusively to
// the lifecycle write boundary. Stop closes active clients and waits for all
// callbacks before removing the socket that this server created.
type Server struct {
	sink       activitySink
	logger     *slog.Logger
	listener   *net.UnixListener
	path       string
	socketInfo os.FileInfo
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	stopOnce   sync.Once
	mu         sync.Mutex
	clients    map[net.Conn]struct{}
	workers    sync.WaitGroup
}

// Start prepares the endpoint synchronously so a startup failure can be logged
// by the daemon. Cancellation and Stop both gracefully stop the server.
func Start(ctx context.Context, dataDir string, sink activitySink, logger *slog.Logger) (*Server, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if dataDir == "" || sink == nil {
		return nil, errors.New("fx Herdr requires data directory and lifecycle sink")
	}
	if logger == nil {
		logger = slog.Default()
	}
	path := SocketPath(dataDir)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create fx Herdr directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("fx Herdr run path is not a directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // A private directory needs owner traversal; this is not a regular file.
		return nil, err
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	info, err = os.Lstat(path)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		removeOwnedSocket(path, info, logger)
		return nil, err
	}
	serverCtx, cancel := context.WithCancel(ctx)
	s := &Server{sink: sink, logger: logger, listener: listener, path: path, socketInfo: info, ctx: serverCtx, cancel: cancel, done: make(chan struct{}), clients: make(map[net.Conn]struct{})}
	go s.serve()
	go func() {
		select {
		case <-serverCtx.Done():
			s.shutdown()
		case <-s.done:
		}
	}()
	return s, nil
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("fx Herdr path is not a Unix socket: %s", path)
	}
	conn, dialErr := net.DialTimeout("unix", path, 150*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		return errors.New("fx Herdr socket is already listening")
	}
	// Only connection refused proves a dead listener. Permission failures and
	// timeouts must never authorize unlinking another process's live endpoint.
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return fmt.Errorf("probe fx Herdr socket: %w", dialErr)
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if current.Mode()&os.ModeSocket == 0 || !os.SameFile(info, current) {
		return errors.New("fx Herdr socket changed during stale check")
	}
	return os.Remove(path)
}

func removeOwnedSocket(path string, original os.FileInfo, logger *slog.Logger) {
	current, err := os.Lstat(path)
	if err == nil && current.Mode()&os.ModeSocket != 0 && os.SameFile(original, current) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			logger.Warn("remove fx Herdr socket", "error", err)
		}
	}
}

func (s *Server) serve() {
	defer close(s.done)
	defer removeOwnedSocket(s.path, s.socketInfo, s.logger)
	defer s.workers.Wait()
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			if s.ctx.Err() == nil {
				s.logger.Warn("accept fx Herdr client", "error", err)
				s.shutdown()
			}
			return
		}
		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		if len(s.clients) >= maxConnections {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		s.clients[conn] = struct{}{}
		s.workers.Add(1)
		s.mu.Unlock()
		go s.serveClient(conn)
	}
}

func (s *Server) serveClient(conn net.Conn) {
	defer s.workers.Done()
	defer func() { _ = conn.Close(); s.mu.Lock(); delete(s.clients, conn); s.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(s.ctx, clientDuration)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return
	}
	reader := bufio.NewReaderSize(conn, maxMessageBytes+1)
	writer := json.NewEncoder(conn)
	for {
		line, err := reader.ReadSlice('\n')
		if err != nil || len(line) > maxMessageBytes {
			return
		}
		if err := writer.Encode(s.handle(ctx, line)); err != nil {
			return
		}
	}
}

func (s *Server) shutdown() {
	s.stopOnce.Do(func() {
		s.cancel()
		_ = s.listener.Close()
		s.mu.Lock()
		for conn := range s.clients {
			_ = conn.Close()
		}
		s.mu.Unlock()
	})
}

// Stop is safe to call repeatedly, with or without prior context cancellation.
func (s *Server) Stop() { s.shutdown(); <-s.done }

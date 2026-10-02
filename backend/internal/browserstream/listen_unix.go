//go:build !windows

package browserstream

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
)

// Listen creates a private, short-lived Unix socket for the Electron media
// host. A random name prevents address squatting; the token still authenticates
// the peer.
func Listen(_ string) (net.Listener, string, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return nil, "", err
	}
	root := os.TempDir()
	if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
		root = "/tmp"
	}
	path := filepath.Join(root, fmt.Sprintf("ao-bstream-%d-%s.sock", os.Getpid(), hex.EncodeToString(random)))
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, "", err
	}
	wrapper := &cleanupListener{Listener: ln, path: path}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = wrapper.Close()
		return nil, "", err
	}
	return wrapper, path, nil
}

type cleanupListener struct {
	net.Listener
	path string
	once sync.Once
}

func (l *cleanupListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { _ = os.Remove(l.path) })
	return err
}

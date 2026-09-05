//go:build !windows

package tmux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Darwin's sockaddr_un.sun_path is 104 bytes including the trailing NUL.
// Linux permits a little more, but using Darwin's ceiling reproduces the
// portable address contract used by AO's historical private tmux runtime.
const maxUnixSocketPathBytes = 103

const socketAliasIdentityBytes = 16

// socketAddress preserves ordinary explicit-path behavior while reproducing
// the alias used by the historical private-socket release for paths which
// cannot fit in sockaddr_un. Qualified handles continue to persist the raw,
// canonical namespace path; this function only translates it at the tmux argv
// boundary.
func socketAddress(ctx context.Context, socketPath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len([]byte(socketPath)) <= maxUnixSocketPathBytes {
		return socketPath, nil
	}
	return privateSocketAddress(ctx, socketPath)
}

// privateSocketAddress is the exact long-path mapping shipped by the private
// tmux runtime. A deterministic short directory symlink under /tmp points at
// the real AO runtime directory; binding through that alias keeps the socket
// inode beside running.json while satisfying AF_UNIX's textual address limit.
func privateSocketAddress(ctx context.Context, socketPath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	targetDir, err := validatePrivateSocketDirectory(filepath.Dir(socketPath))
	if err != nil {
		return "", err
	}
	socketPath = filepath.Join(targetDir, filepath.Base(socketPath))
	if len([]byte(socketPath)) <= maxUnixSocketPathBytes {
		return socketPath, nil
	}

	digest := sha256.Sum256([]byte(socketPath))
	identity := hex.EncodeToString(digest[:socketAliasIdentityBytes])
	// The deterministic aliases live inside an owner-only root. Creating them
	// directly in the shared /tmp namespace would let another local user
	// pre-create an alias between AO's checks and tmux's bind.
	aliasRoot := fmt.Sprintf("/tmp/ao-tmux-%d", os.Getuid())
	if err := ensureSocketAliasRoot(aliasRoot); err != nil {
		return "", err
	}
	aliasDir := filepath.Join(aliasRoot, identity)
	address := filepath.Join(aliasDir, filepath.Base(socketPath))
	if len([]byte(address)) > maxUnixSocketPathBytes {
		return "", fmt.Errorf("tmux runtime: private socket alias is %d bytes; maximum is %d", len([]byte(address)), maxUnixSocketPathBytes)
	}

	if err := os.Symlink(targetDir, aliasDir); err != nil {
		if !os.IsExist(err) {
			return "", fmt.Errorf("tmux runtime: create private socket directory alias: %w", err)
		}
		if err := validateSocketAlias(aliasDir, targetDir); err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return address, nil
}

// validatePrivateSocketDirectory returns a canonical directory which cannot be
// redirected or modified by another local user. The leaf must belong to AO's
// user. Every ancestor must belong to that user or root and must not be
// group/other-writable, except for a root-owned sticky directory such as /tmp.
func validatePrivateSocketDirectory(targetDir string) (string, error) {
	resolved, err := filepath.EvalSymlinks(targetDir)
	if err != nil {
		return "", fmt.Errorf("tmux runtime: resolve private socket directory: %w", err)
	}
	currentUID := int64(os.Getuid())
	for current := resolved; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return "", fmt.Errorf("tmux runtime: inspect private socket directory: %w", err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("tmux runtime: private socket path component is not a directory: %s", current)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return "", fmt.Errorf("tmux runtime: cannot inspect private socket directory owner: %s", current)
		}
		ownerUID := int64(stat.Uid)
		if current == resolved && ownerUID != currentUID {
			return "", fmt.Errorf("tmux runtime: private socket directory is not owned by the current user: %s", current)
		}
		if ownerUID != currentUID && ownerUID != 0 {
			return "", fmt.Errorf("tmux runtime: private socket path component has an untrusted owner: %s", current)
		}
		if info.Mode().Perm()&0o022 != 0 && (ownerUID != 0 || info.Mode()&os.ModeSticky == 0) {
			return "", fmt.Errorf("tmux runtime: private socket directory is writable by other users: %s", current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return resolved, nil
}

func ensureSocketAliasRoot(aliasRoot string) error {
	err := os.Mkdir(aliasRoot, 0o700)
	if err != nil && !os.IsExist(err) {
		return fmt.Errorf("tmux runtime: create private socket alias root: %w", err)
	}

	info, err := os.Lstat(aliasRoot)
	if err != nil {
		return fmt.Errorf("tmux runtime: inspect private socket alias root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("tmux runtime: private socket alias root is not a directory: %s", aliasRoot)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Getuid()) {
		return fmt.Errorf("tmux runtime: private socket alias root is not owned by the current user: %s", aliasRoot)
	}
	if info.Mode().Perm() != 0o700 {
		//nolint:gosec // This owner-only directory needs execute permission to reach its aliases.
		if err := os.Chmod(aliasRoot, 0o700); err != nil {
			return fmt.Errorf("tmux runtime: secure private socket alias root: %w", err)
		}
	}
	return nil
}

func validateSocketAlias(aliasDir, wantTarget string) error {
	info, err := os.Lstat(aliasDir)
	if err != nil {
		return fmt.Errorf("tmux runtime: inspect private socket directory alias: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("tmux runtime: private socket directory alias is not a symlink: %s", aliasDir)
	}
	gotTarget, err := os.Readlink(aliasDir)
	if err != nil {
		return fmt.Errorf("tmux runtime: read private socket directory alias: %w", err)
	}
	if !filepath.IsAbs(gotTarget) {
		gotTarget = filepath.Join(filepath.Dir(aliasDir), gotTarget)
	}
	gotTarget, err = filepath.Abs(gotTarget)
	if err != nil {
		return fmt.Errorf("tmux runtime: resolve private socket directory alias: %w", err)
	}
	wantTarget, err = filepath.Abs(wantTarget)
	if err != nil {
		return fmt.Errorf("tmux runtime: resolve private socket directory: %w", err)
	}
	if filepath.Clean(gotTarget) != filepath.Clean(wantTarget) {
		return fmt.Errorf("tmux runtime: private socket directory alias points to %s, want %s", gotTarget, wantTarget)
	}
	return nil
}

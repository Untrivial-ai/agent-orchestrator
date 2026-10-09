package fsbrowser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

func symlink(t *testing.T, target, path string) {
	t.Helper()
	err := os.Symlink(target, path)
	if errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EOPNOTSUPP) || (runtime.GOOS == "windows" && errors.Is(err, os.ErrPermission)) {
		t.Skipf("symlink creation unsupported: %v", err)
	}
	require.NoError(t, err)
}

func TestListDirectorySymlinks(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "normal", "child"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(external, ".git"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".worktree"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".worktree", ".git"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "file"), nil, 0o600))
	symlink(t, "normal", filepath.Join(root, "alias"))
	symlink(t, "alias", filepath.Join(root, "chain"))
	symlink(t, external, filepath.Join(root, "external"))
	symlink(t, ".worktree", filepath.Join(root, "worktree"))
	symlink(t, "normal", filepath.Join(root, ".hidden"))
	symlink(t, "file", filepath.Join(root, "file-alias"))
	symlink(t, "missing", filepath.Join(root, "broken"))
	symlink(t, "loop", filepath.Join(root, "loop"))

	listing, err := New().List(context.Background(), root)
	require.NoError(t, err)
	require.Equal(t, Listing{
		Path: root, Parent: filepath.Dir(root),
		Entries: []Entry{
			{Name: "alias", Path: filepath.Join(root, "alias")},
			{Name: "chain", Path: filepath.Join(root, "chain")},
			{Name: "external", Path: filepath.Join(root, "external"), GitRepo: true},
			{Name: "normal", Path: filepath.Join(root, "normal")},
			{Name: "worktree", Path: filepath.Join(root, "worktree"), GitRepo: true},
		},
	}, listing)

	for _, name := range []string{"alias", "chain"} {
		t.Run(name, func(t *testing.T) {
			alias := filepath.Join(root, name)
			got, err := New().List(context.Background(), alias)
			require.NoError(t, err)
			require.Equal(t, Listing{
				Path: alias, Parent: root,
				Entries: []Entry{{Name: "child", Path: filepath.Join(alias, "child")}},
			}, got)
		})
	}
}

func TestListSymlinkEntryLimit(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	for i := range maxEntries {
		symlink(t, target, filepath.Join(root, fmt.Sprintf("dir-%04d", i)))
	}
	symlink(t, target, filepath.Join(root, ".hidden"))
	symlink(t, "missing", filepath.Join(root, "z-broken"))
	require.NoError(t, os.WriteFile(filepath.Join(root, "z-file"), nil, 0o600))
	symlink(t, "z-file", filepath.Join(root, "z-file-alias"))
	listing, err := New().List(context.Background(), root)
	require.NoError(t, err)
	require.Len(t, listing.Entries, maxEntries)
	require.False(t, listing.Truncated)
	for i, entry := range listing.Entries {
		name := fmt.Sprintf("dir-%04d", i)
		require.Equal(t, Entry{Name: name, Path: filepath.Join(root, name)}, entry)
	}
	symlink(t, target, filepath.Join(root, "z-overflow"))
	truncated, err := New().List(context.Background(), root)
	require.NoError(t, err)
	require.True(t, truncated.Truncated)
	require.Equal(t, listing.Entries, truncated.Entries)
}

func TestListSymlinkPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce Unix directory mode bits")
	}
	root := t.TempDir()
	blocked := t.TempDir()
	target := filepath.Join(blocked, "target")
	require.NoError(t, os.Mkdir(target, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "visible"), 0o700))
	alias := filepath.Join(root, "alias")
	symlink(t, target, alias)
	require.NoError(t, os.Chmod(blocked, 0))
	t.Cleanup(func() { require.NoError(t, os.Chmod(blocked, 0o700)) })
	_, err := os.Stat(target)
	if err == nil {
		t.Skip("process can traverse directories without permission bits")
	}
	require.ErrorIs(t, err, os.ErrPermission)
	listing, err := New().List(context.Background(), root)
	require.NoError(t, err)
	require.Equal(t, []Entry{{Name: "visible", Path: filepath.Join(root, "visible")}}, listing.Entries)
	_, err = New().List(context.Background(), alias)
	var apiErr *apierr.Error
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, apierr.KindForbidden, apiErr.Kind)
	require.Equal(t, "FS_FORBIDDEN", apiErr.Code)
}

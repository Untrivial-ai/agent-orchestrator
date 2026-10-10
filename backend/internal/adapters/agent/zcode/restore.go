package zcode

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite" // register the read-only native session metadata driver
)

// Native resume replaces its working directory from persisted session metadata.
// Read only the exact row before spawning so a moved or foreign session cannot
// silently change the AO worktree. Do not copy, migrate or create the provider DB.
func validateNativeRestore(ctx context.Context, workspace, id string, env map[string]string) error {
	if strings.TrimSpace(workspace) == "" {
		return fmt.Errorf("zcode: restore workspace is required")
	}
	path, err := nativeSessionDB(workspace, env)
	if err != nil {
		return err
	}
	uri := &url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro&_pragma=busy_timeout(1000)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return fmt.Errorf("zcode: open native session store: %w", err)
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var directory string
	if err := db.QueryRowContext(ctx, "SELECT directory FROM session WHERE id = ? AND time_archived IS NULL", id).Scan(&directory); err != nil {
		return fmt.Errorf("zcode: exact native session is unavailable: %w", err)
	}
	var hasHistory bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM message m JOIN part p ON p.message_id = m.id AND p.session_id = m.session_id
		WHERE m.session_id = ? AND json_valid(m.data) AND json_valid(p.data)
		AND json_extract(m.data, '$.role') = 'user'
	)`, id).Scan(&hasHistory); err != nil || !hasHistory {
		return fmt.Errorf("zcode: native session history is missing or unreadable")
	}
	wanted, err := os.Stat(workspace)
	if err != nil {
		return fmt.Errorf("zcode: restore workspace unavailable: %w", err)
	}
	stored, err := os.Stat(directory)
	if err != nil || !stored.IsDir() || !wanted.IsDir() || !os.SameFile(wanted, stored) {
		return fmt.Errorf("zcode: native session workspace does not match the AO worktree")
	}
	return nil
}

// Match v3.14.3 config-factory/project-config discovery: user config, project
// files from nearest Git root to cwd, then native environment overrides.
func nativeSessionDB(workspace string, env map[string]string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	homeKey := "HOME"
	if runtime.GOOS == "windows" {
		homeKey = "USERPROFILE"
	}
	if override, ok := env[homeKey]; ok && override != "" {
		home = override
	}
	value := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	dirs := []string{}
	for current := absolute; ; current = filepath.Dir(current) {
		dirs = append(dirs, current)
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			slices.Reverse(dirs)
			break
		}
		if filepath.Dir(current) == current {
			dirs = []string{absolute}
			break
		}
	}
	paths := []string{filepath.Join(home, ".zcode", "cli", "config.json")}
	for _, dir := range dirs {
		paths = append(paths, filepath.Join(dir, "zcode.json"), filepath.Join(dir, ".zcode", "config.json"))
	}
	for _, path := range paths {
		data, err := os.ReadFile(path) //nolint:gosec // native, bounded config discovery paths
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("zcode: read native storage config: %w", err)
		}
		var config struct {
			Storage struct {
				SessionDBPath *string `json:"sessionDbPath"`
			} `json:"storage"`
		}
		if err := json.Unmarshal(data, &config); err != nil {
			return "", fmt.Errorf("zcode: invalid native storage config: %w", err)
		}
		if config.Storage.SessionDBPath != nil {
			value = *config.Storage.SessionDBPath
		}
	}
	lookup := func(key string) string {
		if value, ok := env[key]; ok {
			return value
		}
		return os.Getenv(key)
	}
	primary, alias := lookup("ZCODE_SESSION_DB_PATH"), lookup("ZCODE_SESSION_DB")
	if primary != "" && alias != "" && primary != alias {
		return "", fmt.Errorf("zcode: conflicting native session database overrides")
	}
	if primary != "" {
		value = primary
	} else if alias != "" {
		value = alias
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("zcode: native session database path is empty")
	}
	if strings.HasPrefix(value, "~/") {
		value = filepath.Join(home, value[2:])
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(absolute, value)
	}
	return value, nil
}

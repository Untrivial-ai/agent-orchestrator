package agentlaunch

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

var pinMu sync.Mutex

// pinDirectory uses the install directory only when it contains no sibling
// executables. Shared installs get a shim so pinning ao cannot promote node,
// git, or other tools ahead of the agent's selected runtime.
func pinDirectory(exe, dataDir string) (string, error) {
	dir := pinnedDirForExecutable(exe)
	if dir == "" {
		return "", nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return dir, nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect AO install directory: %w", err)
	}
	shared := false
	for _, entry := range entries {
		if entry.Name() == filepath.Base(exe) || entry.IsDir() {
			continue
		}
		info, statErr := os.Stat(filepath.Join(dir, entry.Name()))
		if statErr != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if runtime.GOOS == "windows" {
			switch strings.ToLower(filepath.Ext(entry.Name())) {
			case ".exe", ".com", ".cmd", ".bat":
				shared = true
			}
		} else if info.Mode()&0o111 != 0 {
			shared = true
		}
	}
	if !shared {
		return dir, nil
	}
	if !filepath.IsAbs(dataDir) {
		return "", fmt.Errorf("AO shim data directory must be absolute, got %q", dataDir)
	}
	absolute, err := filepath.Abs(exe)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(absolute))
	shimDir := filepath.Join(dataDir, "runtime", "ao-path", fmt.Sprintf("%x", sum[:16]))
	name := "ao"
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(absolute, "'", `'"'"'`) + "' \"$@\"\n"
	if runtime.GOOS == "windows" {
		name = "ao.cmd"
		script = "@echo off\r\n\"" + strings.ReplaceAll(absolute, "%", "%%") + "\" %*\r\n"
	}
	pinMu.Lock()
	defer pinMu.Unlock()
	if err := os.MkdirAll(shimDir, 0o700); err != nil {
		return "", err
	}
	target := filepath.Join(shimDir, name)
	if content, err := os.ReadFile(target); err == nil && string(content) == script {
		if runtime.GOOS == "windows" {
			if err := ensureWindowsAOExecutable(shimDir, absolute); err != nil {
				return "", err
			}
		}
		return shimDir, nil
	}
	temp, err := os.CreateTemp(shimDir, ".ao-*")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, err := temp.WriteString(script); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tempPath, 0o700); err != nil { //nolint:gosec // executable AO shim
		return "", err
	}
	if err := os.Rename(tempPath, target); err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		if err := ensureWindowsAOExecutable(shimDir, absolute); err != nil {
			return "", err
		}
	}
	return shimDir, nil
}

func ensureWindowsAOExecutable(shimDir, executable string) error {
	return ensureWindowsAOExecutableWithLink(shimDir, executable, os.Link)
}

func ensureWindowsAOExecutableWithLink(shimDir, executable string, link func(string, string) error) error {
	target := filepath.Join(shimDir, "ao.exe")
	sourceInfo, err := os.Stat(executable)
	if err != nil {
		return fmt.Errorf("stat AO executable: %w", err)
	}
	if targetInfo, targetErr := os.Stat(target); targetErr == nil && os.SameFile(sourceInfo, targetInfo) {
		return nil
	} else if targetErr == nil && targetInfo.Size() == sourceInfo.Size() && targetInfo.ModTime().Equal(sourceInfo.ModTime()) {
		return nil
	}
	temp, err := os.CreateTemp(shimDir, ".ao-exe-*")
	if err != nil {
		return fmt.Errorf("create AO executable shim: %w", err)
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tempPath) }()
	if err := os.Remove(tempPath); err != nil {
		return err
	}
	if err := link(executable, tempPath); err != nil {
		if err := copyExecutable(executable, tempPath, sourceInfo.ModTime()); err != nil {
			return fmt.Errorf("copy AO executable after link failed: %w", err)
		}
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replace AO executable shim: %w", err)
	}
	if err := os.Rename(tempPath, target); err != nil {
		return fmt.Errorf("install AO executable shim: %w", err)
	}
	return nil
}

func copyExecutable(source, target string, modTime time.Time) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700) //nolint:gosec // executable AO shim
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(target, modTime, modTime)
}

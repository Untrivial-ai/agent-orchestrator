// Package systemexec adapts host PATH lookup and child-process execution to
// the narrow ports consumed by the system requirement/install services.
package systemexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Adapter implements the host executable and command-runner ports.
type Adapter struct {
	installerRoot string
	httpClient    *http.Client
}

var (
	_ ports.ExecutableFinder       = Adapter{}
	_ ports.CommandRunner          = Adapter{}
	_ ports.InstallCommandRunner   = Adapter{}
	_ ports.InstallScriptRunner    = Adapter{}
	_ ports.InstallCapabilityProbe = Adapter{}
)

// New creates a host adapter whose installer scratch space stays inside AO's
// configured data directory.
func New(dataDir string) Adapter {
	return newAdapter(dataDir, http.DefaultClient)
}

func newAdapter(dataDir string, client *http.Client) Adapter {
	return Adapter{
		installerRoot: filepath.Join(dataDir, "installers", "tmp"),
		httpClient:    client,
	}
}

// LookPath resolves file against the daemon process PATH.
func (Adapter) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

// RunInstall executes a server-owned installer command without interactive
// stdin. The small Env list augments the daemon environment rather than
// replacing PATH and the user's package-manager configuration.
func (Adapter) RunInstall(ctx context.Context, command ports.InstallCommand, stdout, stderr io.Writer) error {
	cmd, err := commandContext(ctx, command.Argv[0], command.Argv[1:]...)
	if err != nil {
		return err
	}
	configureProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessTree(cmd) }
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = strings.NewReader("")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), command.Env...)
	if err := cmd.Run(); err != nil {
		return err
	}
	// Windows installers persist PATH changes outside the already-running
	// daemon's process environment. Refresh it before adapter-backed
	// verification tries to resolve the newly installed executable.
	refreshExecutablePath()
	return nil
}

// Probe takes one cancellation-aware snapshot of the package-manager facts
// needed by every fixed recipe in a Settings response.
func (Adapter) Probe(ctx context.Context) (ports.InstallCapabilities, error) {
	var snapshot ports.InstallCapabilities

	nodeVersion, nodeErr := capabilityOutput(ctx, "node", "--version")
	npmVersion, npmVersionErr := capabilityOutput(ctx, "npm", "--version")
	npmPrefix, npmPrefixErr := capabilityOutput(ctx, "npm", "prefix", "-g")
	if err := ctx.Err(); err != nil {
		return ports.InstallCapabilities{}, err
	}
	snapshot.NPM = ports.NPMInstallCapabilities{
		NodeVersion: nodeVersion, NPMVersion: npmVersion, GlobalPrefix: npmPrefix,
	}
	snapshot.NPM.Err = errors.Join(nodeErr, npmVersionErr, npmPrefixErr)
	if snapshot.NPM.Err == nil {
		writable, err := pathWritable(ctx, npmPrefix)
		if err != nil {
			return ports.InstallCapabilities{}, err
		}
		snapshot.NPM.PrefixWritable = writable
	}

	brewPrefix, brewPrefixErr := capabilityOutput(ctx, "brew", "--prefix")
	formulae, formulaErr := capabilityLines(ctx, "brew", "list", "--formula", "-1")
	casks, caskErr := capabilityLines(ctx, "brew", "list", "--cask", "-1")
	if err := ctx.Err(); err != nil {
		return ports.InstallCapabilities{}, err
	}
	snapshot.Homebrew = ports.HomebrewInstallCapabilities{
		Prefix: brewPrefix, Formulae: formulae, Casks: casks,
		Err: errors.Join(brewPrefixErr, formulaErr, caskErr),
	}
	if snapshot.Homebrew.Err == nil {
		writable, err := pathWritable(ctx, brewPrefix)
		if err != nil {
			return ports.InstallCapabilities{}, err
		}
		snapshot.Homebrew.PrefixWritable = writable
	}
	return snapshot, nil
}

func capabilityOutput(parent context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	cmd, err := commandContext(ctx, name, args...)
	if err != nil {
		return "", err
	}
	configureProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessTree(cmd) }
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func capabilityLines(ctx context.Context, name string, args ...string) (map[string]bool, error) {
	out, err := capabilityOutput(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	items := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		if item := strings.TrimSpace(line); item != "" {
			items[item] = true
		}
	}
	return items, nil
}

// pathWritable checks the nearest existing ancestor by creating and removing
// a private temporary file. This honors ACLs and ownership more accurately
// than permission-bit inspection and aborts promptly with its request.
func pathWritable(ctx context.Context, path string) (bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if _, err := os.Stat(path); err == nil {
			file, createErr := os.CreateTemp(path, ".ao-write-check-*")
			if createErr != nil {
				return false, nil //nolint:nilerr // inability to create the probe file means not writable.
			}
			name := file.Name()
			closeErr := file.Close()
			removeErr := os.Remove(name)
			return closeErr == nil && removeErr == nil, nil
		} else if !os.IsNotExist(err) {
			return false, nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false, nil
		}
		path = parent
	}
}

// Run executes argv with ctx and connects its output to the supplied writers.
func (Adapter) Run(ctx context.Context, argv []string, stdout, stderr io.Writer) error {
	cmd, err := commandContext(ctx, argv[0], argv[1:]...)
	if err != nil {
		return err
	}
	configureProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessTree(cmd) }
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

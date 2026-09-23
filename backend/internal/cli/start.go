package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

// releaseRepo is the GitHub "owner/repo" that `ao start` fetches the desktop app
// from. It defaults to the production target and is overridable at build time so
// a test binary fetches from the fork without a source edit:
//
//	go build -ldflags "-X github.com/aoagents/agent-orchestrator/backend/internal/cli.releaseRepo=harshitsinghbhandari/agent-orchestrator" ./cmd/ao
//
// Mirrors how version.go's Version var is stamped by release tooling.
//
// Untrivial-ai is the org the repo was transferred to in July 2026. The old
// AgentWrapper URLs still resolve only through GitHub's rename redirect, which
// is not a contract: the same staleness that stranded the baked update feed
// (#3523) would strand every `ao start` download the day that redirect stops.
var releaseRepo = "Untrivial-ai/agent-orchestrator"

// appBundleName is the macOS bundle directory name produced by electron-forge
// (spaced, per frontend/forge.config.ts).
const appBundleName = "Agent Orchestrator.app"

// appStateFileName is the marker the desktop app writes under ~/.ao on every
// launch (spec §5). `ao start` is a read-only consumer of it.
const appStateFileName = "app-state.json"

// appState mirrors the app-written ~/.ao/app-state.json marker (spec §5). Only
// the desktop app writes it; `ao start` reads it as a fast-path hint and never
// trusts appPath without stat-ing it (invariant 2).
type appState struct {
	SchemaVersion    int    `json:"schemaVersion"`
	AppPath          string `json:"appPath"`
	Version          string `json:"version"`
	InstalledAt      string `json:"installedAt"`
	LastReconciledAt string `json:"lastReconciledAt"`
	InstallSource    string `json:"installSource"`
}

type startOptions struct {
	json bool
}

// startResult is the JSON shape emitted with --json: what `ao start` resolved,
// whether it fetched, whether it opened, and the resulting bundle path.
type startResult struct {
	Resolved bool   `json:"resolved"`
	Fetched  bool   `json:"fetched"`
	Opened   bool   `json:"opened"`
	AppPath  string `json:"appPath"`
}

func newStartCommand(ctx *commandContext) *cobra.Command {
	opts := startOptions{}
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Fetch (if needed) and open the Agent Orchestrator desktop app",
		Long: "Fetch (if needed) and open the Agent Orchestrator desktop app.\n\n" +
			"The desktop app now owns the daemon, state, and updates. `ao start` no\n" +
			"longer runs a daemon: it resolves the installed app (or downloads the\n" +
			"latest release), opens it, and exits.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runStart(cmd.Context(), cmd, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output start result as JSON")
	return cmd
}

// runStart implements the spec §6.1 algorithm: resolve the installed app, fetch
// it if absent, open it, then print the deprecation notice. It never blocks or
// supervises the launched app.
func (c *commandContext) runStart(ctx context.Context, cmd *cobra.Command, opts startOptions) error {
	out := cmd.OutOrStdout()
	res := startResult{}

	appPath := c.resolveApp()
	res.Resolved = appPath != ""

	var err error
	if appPath == "" {
		// Progress for the fetch path goes to stderr so stdout stays pure JSON
		// under --json. The resolve-and-launch fast path above stays quiet.
		appPath, err = c.fetchApp(ctx, cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		res.Fetched = true
	}
	res.AppPath = appPath

	if runtime.GOOS == "linux" {
		if err := c.registerLinuxProtocolHandler(ctx, appPath); err != nil {
			return err
		}
	}

	opened, err := c.openApp(ctx, appPath)
	if err != nil {
		return err
	}
	res.Opened = opened

	if opts.json {
		return writeJSON(out, res)
	}

	c.printDeprecationNotice(out)
	if !opened {
		c.printManualOpen(out, appPath)
	}
	return nil
}

// resolveApp returns the path to a usable desktop bundle, or "" when none is
// found (spec §6.2). Resolution order is marker path -> stat -> known location
// scan, with one exception: on macOS a real bundle in /Applications outranks
// the marker. It still never compares versions (invariant 5).
//
// The exception exists because the marker is not authoritative about which copy
// the user wants. The app rewrites it on every launch, so a stale copy (the
// original download, one on a dmg) that got launched leaves the marker pointing
// at itself, and `ao start` would then keep reopening that copy instead of the
// install. /Applications is the one location the updater maintains, so prefer
// it and let the marker cover the rest (~/Applications, unusual layouts).
func (c *commandContext) resolveApp() string {
	if p := preferredAppPath(); p != "" && isUsableBundle(p) {
		return p
	}
	if p := c.markerAppPath(); p != "" && isUsableBundle(p) {
		return p
	}
	for _, p := range appScanLocations() {
		if isUsableBundle(p) {
			return p
		}
	}
	return ""
}

// preferredAppPath is the location that outranks the marker, or "" when the
// platform has none. A package var for the same reason as appScanLocations:
// tests point it at a temp bundle instead of the real system path.
var preferredAppPath = darwinApplicationsBundle

// darwinApplicationsBundle is /Applications/<bundle> on macOS, "" elsewhere.
// That is where Squirrel installs updates, so it is the copy actually kept
// current. Windows and Linux have no equivalent single maintained location, so
// they keep the original marker-first order.
func darwinApplicationsBundle() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	// Concatenated, not filepath.Join'd, to match knownAppLocations (and to keep
	// gocritic's filepathJoin check happy about the leading separator).
	return "/Applications/" + appBundleName
}

// appScanLocations is the known-location scan source. It is a package var so
// tests can point the scan at a temp bundle instead of real system paths.
var appScanLocations = knownAppLocations

// markerAppPath reads ~/.ao/app-state.json and returns its recorded appPath, or
// "" if the marker is missing/unreadable. It does not stat the path; callers do.
func (c *commandContext) markerAppPath() string {
	dir, err := aoStateDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, appStateFileName))
	if err != nil {
		return ""
	}
	var st appState
	if err := json.Unmarshal(data, &st); err != nil {
		return ""
	}
	return st.AppPath
}

// aoStateDir resolves the canonical ~/.ao home, honoring AO_DATA_DIR exactly as
// the daemon's config does (the marker lives beside running.json under ~/.ao).
func aoStateDir() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	// running.json lives directly under ~/.ao; the marker sits beside it.
	return filepath.Dir(cfg.RunFilePath), nil
}

// knownAppLocations lists the platform's standard install paths to scan when the
// marker misses (covers website installs and stale markers, spec §6.2).
func knownAppLocations() []string {
	switch runtime.GOOS {
	case "darwin":
		paths := []string{"/Applications/" + appBundleName}
		if home, err := os.UserHomeDir(); err == nil {
			paths = append(paths, filepath.Join(home, "Applications", appBundleName))
		}
		return paths
	case "windows":
		var paths []string
		// Default electron-builder NSIS per-user install (perMachine:false).
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			paths = append(paths, windowsInstalledExe(local))
		}
		// Per-machine fallback (if a user chose an all-users install).
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			paths = append(paths, filepath.Join(pf, "Agent Orchestrator", "agent-orchestrator.exe"))
		}
		return paths
	case "linux":
		paths := []string{linuxAppImagePath()}
		if home, err := os.UserHomeDir(); err == nil {
			paths = append(paths, filepath.Join(home, "Applications", "agent-orchestrator.AppImage"))
		}
		return paths
	default:
		return nil
	}
}

// windowsInstalledExe is the default per-user electron-builder NSIS install
// target for the app exe under %LOCALAPPDATA%.
func windowsInstalledExe(localAppData string) string {
	return filepath.Join(localAppData, "Programs", "Agent Orchestrator", "agent-orchestrator.exe")
}

// linuxAppImagePath is the stable location `ao start` downloads the AppImage to
// and scans for. Keeping it out of the cleared staging dir lets re-runs resolve
// the existing download instead of re-fetching (spec §6.2/§6.3).
func linuxAppImagePath() string {
	dir, err := aoStateDir()
	if err != nil {
		// Fall back to a bare filename so a misconfigured state dir surfaces as a
		// clear "not found" rather than a panic; fetch will re-error on download.
		return "agent-orchestrator.AppImage"
	}
	return filepath.Join(dir, "agent-orchestrator.AppImage")
}

const linuxDesktopEntryName = "agent-orchestrator-ao-app.desktop"

func linuxApplicationsDir() (string, error) {
	// A URL-scheme handler is OS integration metadata, not AO runtime state.
	// freedesktop.org requires desktop entries under XDG_DATA_HOME; keeping the
	// executable and all mutable AO data under ~/.ao is unchanged.
	if dataHome := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(dataHome) {
		return filepath.Join(dataHome, "applications"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home for Linux protocol registration: %w", err)
	}
	return filepath.Join(home, ".local", "share", "applications"), nil
}

func desktopExecPath(appPath string) string {
	escaped := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"`", "\\`",
		"$", `\$`,
		"%", "%%",
	).Replace(appPath)
	return `"` + escaped + `"`
}

func (c *commandContext) registerLinuxProtocolHandler(
	ctx context.Context,
	appPath string,
) error {
	applicationsDir, err := linuxApplicationsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(applicationsDir, 0o750); err != nil {
		return fmt.Errorf("create Linux applications directory: %w", err)
	}
	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Agent Orchestrator
Exec=%s %%u
Terminal=false
NoDisplay=true
MimeType=x-scheme-handler/ao-app;
`, desktopExecPath(appPath))
	target := filepath.Join(applicationsDir, linuxDesktopEntryName)
	temporary := target + ".tmp"
	if err := os.WriteFile(temporary, []byte(entry), 0o600); err != nil {
		return fmt.Errorf("write Linux desktop entry: %w", err)
	}
	if err := os.Chmod(temporary, 0o644); err != nil { //nolint:gosec // G302: desktop entries must be world-readable
		_ = os.Remove(temporary)
		return fmt.Errorf("chmod Linux desktop entry: %w", err)
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("install Linux desktop entry: %w", err)
	}
	if out, err := c.deps.CommandOutput(
		ctx,
		"xdg-mime",
		"default",
		linuxDesktopEntryName,
		"x-scheme-handler/ao-app",
	); err != nil {
		return fmt.Errorf(
			"register ao-app URL handler with xdg-mime (install xdg-utils or use the .deb/.rpm package): %w: %s",
			err,
			out,
		)
	}
	out, err := c.deps.CommandOutput(
		ctx,
		"xdg-mime",
		"query",
		"default",
		"x-scheme-handler/ao-app",
	)
	if err != nil {
		return fmt.Errorf("verify ao-app URL handler: %w: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != linuxDesktopEntryName {
		return fmt.Errorf(
			"verify ao-app URL handler: xdg-mime selected %q instead of %q",
			strings.TrimSpace(string(out)),
			linuxDesktopEntryName,
		)
	}
	return nil
}

// isUsableBundle reports whether p stats as a usable app bundle. On macOS a
// bundle is a directory; on Windows/Linux it is a regular file (the installed
// exe / the AppImage). The filesystem is the source of truth (invariant 2).
func isUsableBundle(p string) bool {
	if p == "" {
		return false
	}
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	if runtime.GOOS == "darwin" {
		return info.IsDir()
	}
	// Windows exe / Linux AppImage: must be a regular file, not a directory.
	return info.Mode().IsRegular()
}

// fetchApp downloads the latest desktop release for this platform and returns
// the resolved bundle path (spec §6.3). macOS unpacks a signed zip into staging,
// Windows runs the NSIS installer silently, and Linux drops a chmod'd AppImage at
// a stable path.
func (c *commandContext) fetchApp(ctx context.Context, w io.Writer) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return c.fetchAppDarwin(ctx, w)
	case "windows":
		return c.fetchAppWindows(ctx, w)
	case "linux":
		return c.fetchAppLinux(ctx, w)
	default:
		return "", fmt.Errorf("ao start: fetch not supported on %s", runtime.GOOS)
	}
}

// fetchAppDarwin downloads the latest macOS release zip and unpacks it into a
// staging dir under ~/.ao/staging, returning the .app bundle path (spec §6.3).
func (c *commandContext) fetchAppDarwin(ctx context.Context, w io.Writer) (string, error) {
	asset, err := assetName()
	if err != nil {
		return "", err
	}
	url := downloadURL(asset)

	stateDir, err := aoStateDir()
	if err != nil {
		return "", err
	}
	staging := filepath.Join(stateDir, "staging")
	// Clear any stale or partial prior unpack so ditto extracts into a clean dir
	// (a leftover bundle could otherwise merge with the new one).
	if err := os.RemoveAll(staging); err != nil {
		return "", fmt.Errorf("clear staging dir: %w", err)
	}
	if err := os.MkdirAll(staging, 0o750); err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}

	zipPath := filepath.Join(staging, asset)
	if err := c.download(ctx, w, url, asset, zipPath); err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}

	// The unpack step is silent and can take seconds on a large bundle; announce
	// it so a quiet `ao start` doesn't look hung after the download finishes.
	_, _ = fmt.Fprintln(w, "Unpacking...")
	// ditto preserves the .app code signature; plain unzip corrupts it (spec §6.3).
	if out, err := c.deps.CommandOutput(ctx, "ditto", "-x", "-k", zipPath, staging); err != nil {
		return "", fmt.Errorf("ditto unpack: %w: %s", err, out)
	}

	appPath := filepath.Join(staging, appBundleName)
	if !isUsableBundle(appPath) {
		return "", fmt.Errorf("ao start: %s not found in unpacked release at %s", appBundleName, staging)
	}
	return appPath, nil
}

// fetchAppWindows downloads the NSIS installer into staging, runs it silently,
// and returns the default per-user install path (spec §6.3).
//
// ponytail: the silent-install flow (NSIS `/S`, default per-user dir) is
// untested on real Windows hardware (this build host is macOS). If the installed
// exe isn't where electron-builder's defaults put it, this surfaces as a clear
// "not found" error rather than silently launching the wrong thing.
func (c *commandContext) fetchAppWindows(ctx context.Context, w io.Writer) (string, error) {
	asset, err := assetName()
	if err != nil {
		return "", err
	}
	url := downloadURL(asset)

	stateDir, err := aoStateDir()
	if err != nil {
		return "", err
	}
	staging := filepath.Join(stateDir, "staging")
	if err := os.RemoveAll(staging); err != nil {
		return "", fmt.Errorf("clear staging dir: %w", err)
	}
	if err := os.MkdirAll(staging, 0o750); err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}

	installerPath := filepath.Join(staging, asset)
	if err := c.download(ctx, w, url, asset, installerPath); err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}

	// The `/S` installer runs with no UI and no output; announce it so the wait
	// after the download reads as progress, not a hang.
	_, _ = fmt.Fprintln(w, "Installing...")
	// NSIS silent install (`/S`) to the default per-user location. The installer
	// is configured oneClick:false/perMachine:false, so `/S` installs without UI
	// under %LOCALAPPDATA% for the current user.
	if out, err := c.deps.CommandOutput(ctx, installerPath, "/S"); err != nil {
		return "", fmt.Errorf("silent install: %w: %s", err, out)
	}

	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return "", fmt.Errorf("ao start: LOCALAPPDATA not set; cannot locate installed app")
	}
	appPath := windowsInstalledExe(local)
	if !isUsableBundle(appPath) {
		return "", fmt.Errorf("ao start: installed app not found at %s", appPath)
	}
	return appPath, nil
}

// fetchAppLinux downloads the self-contained AppImage to a stable path under
// ~/.ao, makes it executable, and returns it. runStart separately installs the
// freedesktop URL handler on every launch, including for an existing AppImage.
func (c *commandContext) fetchAppLinux(ctx context.Context, w io.Writer) (string, error) {
	asset, err := assetName()
	if err != nil {
		return "", err
	}
	url := downloadURL(asset)

	appPath := linuxAppImagePath()
	if err := os.MkdirAll(filepath.Dir(appPath), 0o750); err != nil {
		return "", fmt.Errorf("create state dir: %w", err)
	}
	// Download to a temp name in the same dir, then rename for atomicity so a
	// killed download never leaves a half-written executable at the stable path.
	tmpPath := appPath + ".part"
	if err := c.download(ctx, w, url, asset, tmpPath); err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	// chmod+rename is near-instant, but a one-line marker keeps the post-download
	// step from looking silent and matches the other platforms.
	_, _ = fmt.Fprintln(w, "Installing...")
	// An AppImage is a self-contained executable; it must be 0755 to launch.
	if err := os.Chmod(tmpPath, 0o755); err != nil { //nolint:gosec // G302: AppImage must be executable
		return "", fmt.Errorf("chmod AppImage: %w", err)
	}
	if err := os.Rename(tmpPath, appPath); err != nil {
		return "", fmt.Errorf("install AppImage: %w", err)
	}
	if !isUsableBundle(appPath) {
		return "", fmt.Errorf("ao start: AppImage not found at %s", appPath)
	}
	return appPath, nil
}

// download streams url to dst using the injected HTTP client, reporting progress
// to w. asset is the human-facing filename used in the announce line. A silent
// hundreds-of-MB fetch reads as a hang, so we print a start line (with the size
// from Content-Length when present) and then progress: a live carriage-return
// percentage when w is an interactive terminal, or a plain start+done pair when
// it is not (CI, pipes, redirected output).
func (c *commandContext) download(ctx context.Context, w io.Writer, url, asset, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	// deps.HTTPClient carries a short (2s) timeout sized for loopback daemon
	// probes; a release asset is hundreds of MB. Copy the client and drop the
	// timeout, relying on ctx for cancellation. The Transport is preserved so
	// tests still reach their httptest server.
	client := *c.deps.HTTPClient
	client.Timeout = 0
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}

	// Content-Length is -1 when the server omits the header; total<=0 means the
	// percentage is unknown and we report transferred bytes instead.
	total := resp.ContentLength
	if total > 0 {
		_, _ = fmt.Fprintf(w, "Downloading Agent Orchestrator (%s, ~%s) from %s...\n", asset, humanBytes(total), releaseRepo)
	} else {
		_, _ = fmt.Fprintf(w, "Downloading Agent Orchestrator (%s) from %s...\n", asset, releaseRepo)
	}

	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	pw := &progressWriter{w: w, total: total, tty: writerIsInteractive(w)}
	if _, err := io.Copy(f, io.TeeReader(resp.Body, pw)); err != nil {
		return err
	}
	pw.done()
	return f.Close()
}

// progressWriter counts bytes copied through it and renders download progress to
// w. On an interactive terminal it overwrites a single line with a carriage
// return; otherwise it stays quiet during the copy and prints one "Done" line at
// the end, so logs and pipes never fill with \r spam or per-chunk noise.
type progressWriter struct {
	w       io.Writer
	total   int64 // bytes; <=0 when Content-Length was absent
	tty     bool
	written int64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n := len(b)
	p.written += int64(n)
	if p.tty {
		if p.total > 0 {
			pct := p.written * 100 / p.total
			_, _ = fmt.Fprintf(p.w, "\r  %d%% (%s / %s)", pct, humanBytes(p.written), humanBytes(p.total))
		} else {
			_, _ = fmt.Fprintf(p.w, "\r  %s", humanBytes(p.written))
		}
	}
	return n, nil
}

// done emits the terminal progress line. On a TTY it closes the live line with a
// newline; off a TTY it prints the single completion line.
func (p *progressWriter) done() {
	if p.tty {
		_, _ = fmt.Fprintln(p.w)
		return
	}
	_, _ = fmt.Fprintf(p.w, "Downloaded %s.\n", humanBytes(p.written))
}

// writerIsInteractive reports whether w is an interactive terminal, mirroring
// stdinIsInteractive: only a real *os.File backed by a character device counts,
// so a bytes.Buffer or pipe is treated as non-interactive.
func writerIsInteractive(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// humanBytes formats a byte count with a binary-unit suffix (KiB, MiB, ...),
// e.g. 314572800 -> "300.0 MiB". A tiny hand-rolled formatter avoids promoting
// the already-indirect go-humanize dependency to a direct one for one string.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// assetName maps the current GOOS/GOARCH to the stable release asset name the
// release pipeline publishes (spec §6.3, §8). The pipeline uses "x64" for amd64.
//   - darwin: agent-orchestrator-darwin-{arm64,x64}.zip (signed bundle zip)
//   - windows: agent-orchestrator-win32-x64.exe (NSIS installer, amd64 only)
//   - linux: agent-orchestrator-linux-x64.AppImage (self-contained, amd64 only)
func assetName() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		arch, err := assetArch(runtime.GOARCH)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("agent-orchestrator-darwin-%s.zip", arch), nil
	case "windows":
		if _, err := requireAMD64(); err != nil {
			return "", err
		}
		return "agent-orchestrator-win32-x64.exe", nil
	case "linux":
		if _, err := requireAMD64(); err != nil {
			return "", err
		}
		return "agent-orchestrator-linux-x64.AppImage", nil
	default:
		return "", fmt.Errorf("ao start: no release asset for %s", runtime.GOOS)
	}
}

// requireAMD64 enforces the amd64/x64-only support window for Windows and Linux.
// arm64 Windows/Linux are not published yet; surface a clear unsupported error
// mirroring assetArch rather than fetching a 404.
func requireAMD64() (string, error) {
	if runtime.GOARCH != "amd64" {
		return "", fmt.Errorf("ao start: unsupported architecture %q on %s (only amd64 is published)", runtime.GOARCH, runtime.GOOS)
	}
	return "x64", nil
}

// assetArch maps a Go GOARCH to the release-asset arch token.
func assetArch(goarch string) (string, error) {
	switch goarch {
	case "arm64":
		return "arm64", nil
	case "amd64":
		return "x64", nil
	default:
		return "", fmt.Errorf("ao start: unsupported architecture %q", goarch)
	}
}

// downloadURL builds the constant releases/latest/download URL for asset.
func downloadURL(asset string) string {
	return fmt.Sprintf("https://github.com/%s/releases/latest/download/%s", releaseRepo, asset)
}

// openApp launches the resolved bundle detached and reports whether it launched
// (spec §6.5). It passes --installed-via=npm-bootstrap so the app can record the
// install source in its marker. It never waits on the app. A launch failure
// returns (false, nil) so the caller falls back to manual-open instructions.
func (c *commandContext) openApp(ctx context.Context, appPath string) (bool, error) {
	switch runtime.GOOS {
	case "darwin":
		// `open` returns immediately; --args forwards the rest to the app.
		if out, err := c.deps.CommandOutput(ctx, "open", appPath, "--args", "--installed-via=npm-bootstrap"); err != nil {
			return false, fmt.Errorf("open %s: %w: %s", appPath, err, out)
		}
		return true, nil
	case "windows", "linux":
		// No `open`-style launcher on these platforms; exec the bundle directly,
		// detached, so `ao start` does not block on the app. StartProcess uses
		// cmd.Start() + a detached SysProcAttr (see process.go).
		//
		// ponytail: on some Linux hosts the AppImage may need --no-sandbox; not
		// added here without evidence the bundled Electron requires it. If sandbox
		// launch failures appear, append "--no-sandbox" as the follow-up.
		err := c.deps.StartProcess(processStartConfig{
			Path: appPath,
			Args: []string{"--installed-via=npm-bootstrap"},
		})
		if err != nil {
			// Treat a launch failure as "not opened" so the caller prints the
			// manual-open path rather than aborting the whole command.
			return false, nil //nolint:nilerr // launch failure is reported via the bool, not as an error
		}
		return true, nil
	default:
		return false, nil
	}
}

// printDeprecationNotice explains the new role of the npm `ao` binary. Keep it
// honest: Track B (live auto-update) is not done, so it does not promise it.
func (c *commandContext) printDeprecationNotice(w io.Writer) {
	_, _ = fmt.Fprint(w, "Agent Orchestrator is now a desktop app, and the npm `ao` is just its launcher.\n"+
		"The app is distributed from the website and GitHub Releases; it owns the daemon and updates itself.\n"+
		"You can keep running `ao start` to fetch (if needed) and open it.\n")
}

// printManualOpen tells the user how to open the bundle when `ao start` could
// not launch it for them (non-darwin, or a failed launch handled upstream).
func (c *commandContext) printManualOpen(w io.Writer, appPath string) {
	_, _ = fmt.Fprintf(w, "Could not open the app automatically. Open it manually: %s\n", appPath)
}

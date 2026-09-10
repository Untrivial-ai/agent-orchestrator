// Package codexmaintenance proves ownership of the user's selected Codex. It
// never installs a runtime and never guesses a fallback package manager.
package codexmaintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/agentlaunch"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const packageName = "@openai/codex"
const maxRead = 256 * 1024

// Resolver uses the same binary selector as launches. Host seams are private so
// tests can exercise Windows layouts on Unix without touching resolved installers.
type Resolver struct {
	binary    func(context.Context) (string, error)
	runner    ports.InstallCommandRunner
	lookup    func(string) (string, error)
	realpath  func(string) (string, error)
	readFile  func(string) ([]byte, error)
	getenv    func(string) string
	client    *http.Client
	goos      string
	launchEnv func(context.Context, string) []string
}

// New binds ownership probes to the daemon's effective selector and host runner.
func New(binary func(context.Context) (string, error), runner ports.InstallCommandRunner, finder ports.ExecutableFinder) *Resolver {
	return &Resolver{binary: binary, runner: runner, lookup: finder.LookPath,
		realpath: filepath.EvalSymlinks, readFile: readBoundedFile, getenv: os.Getenv,
		client: &http.Client{Timeout: 4 * time.Second}, goos: runtime.GOOS,
		launchEnv: func(ctx context.Context, binary string) []string {
			env := map[string]string{"PATH": os.Getenv("PATH")}
			agentlaunch.AugmentRuntimePATHForLaunchBinary(ctx, env, []string{binary}, exec.LookPath, agentlaunch.PinnedDir(os.Executable, os.Getenv("AO_DATA_DIR")))
			return []string{"PATH=" + env["PATH"]}
		},
	}
}

func readBoundedFile(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxRead+1))
	if len(data) > maxRead {
		return nil, fmt.Errorf("installation metadata exceeds size limit")
	}
	return data, err
}

func slash(p string) string { return strings.ReplaceAll(p, `\`, "/") }
func (r *Resolver) equal(a, b string) bool {
	a, b = path.Clean(slash(a)), path.Clean(slash(b))
	if r.goos == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
func (r *Resolver) canonical(p string) string {
	if resolved, err := r.realpath(p); err == nil {
		return slash(resolved)
	}
	return ""
}

// Resolve performs bounded local reads only. Missing/unknown ownership stays
// manual-only, independently of authentication and protocol compatibility.
func (r *Resolver) Resolve(ctx context.Context) (ports.CodexInstallation, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	s := ports.CodexInstallation{Source: "unknown", VersionSource: "npm", Warning: "Update the selected Codex installation manually; AO could not verify its owning installer."}
	selected, err := r.binary(ctx)
	if err != nil {
		return s, err
	}
	s.Path = selected
	s.RealPath = r.canonical(selected)
	if s.RealPath == "" {
		return s, fmt.Errorf("selected Codex executable is missing or cannot be resolved")
	}
	version, err := r.probe(ctx, []string{selected, "--version"}, nil)
	if err != nil {
		return s, fmt.Errorf("read effective Codex version: %w", err)
	}
	s.Version = Version(version)
	if s.Version == "" {
		return s, fmt.Errorf("effective Codex version is unrecognized")
	}

	// Symlinks/junctions resolve natively. Only recognized package-manager shims
	// are unwrapped; arbitrary user scripts remain manual-only.
	resolved := s.RealPath
	if target := r.shimTarget(resolved); target != "" {
		s.RealPath = target
	}
	if i := strings.Index(s.RealPath, "/packages/standalone/releases/"); i > 0 {
		home := s.RealPath[:i]
		s.Source, s.Scope = "standalone", "standalone:"+home
		// Updating a pinned release path would update another visible command.
		// Require the selected entry to traverse the installation's current link.
		current := r.canonical(home + "/packages/standalone/current")
		if current != "" && strings.HasPrefix(s.RealPath, current+"/") && !strings.Contains(slash(selected), "/releases/") {
			env := []string{"CODEX_HOME=" + home, "CODEX_INSTALL_DIR=" + path.Dir(slash(selected)), "CODEX_NON_INTERACTIVE=1", "CODEX_RELEASE=latest"}
			help, helpErr := r.probe(ctx, []string{selected, "update", "--help"}, env)
			if helpErr == nil && strings.Contains(strings.ToLower(help), "codex update") {
				s.Command = ports.InstallCommand{Argv: []string{selected, "update"}, Env: env}
			}
		}
	} else if !r.resolveVite(ctx, &s) {
		if pkg := r.packageRoot(s.RealPath); pkg != "" {
			r.resolvePackage(ctx, &s, pkg)
		} else {
			r.resolveBrew(ctx, &s)
		}
	}
	if len(s.Command.Argv) > 0 {
		if r.launchEnv != nil {
			s.Command.Env = append(r.launchEnv(ctx, selected), s.Command.Env...)
		}
		for _, key := range []string{"PNPM_HOME", "BUN_INSTALL", "VP_HOME", "VP_BIN_DIR", "VP_DATA_DIR", "VP_CACHE_DIR"} {
			if value := r.getenv(key); value != "" {
				s.Command.Env = append(s.Command.Env, key+"="+value)
			}
		}
		// Batch files expand percent/delayed-expansion syntax even inside quotes.
		// Such unusual paths remain usable but require a manual update.
		for _, value := range append(append([]string{}, s.Command.Argv...), s.Command.Env...) {
			if strings.ContainsAny(value, "\r\n\x00") || (r.goos == "windows" && strings.ContainsAny(value, "%!\"")) {
				s.Command = ports.InstallCommand{}
				break
			}
		}
	}
	if len(s.Command.Argv) > 0 {
		s.Warning = ""
	}
	// A changed shim, payload, installer, prefix, environment, or installed
	// version invalidates the displayed approval and the queued command.
	stamp := []string{s.Path, s.RealPath, s.Version, s.Source, s.Scope}
	stamp = append(stamp, fileStamp(s.Path), fileStamp(s.RealPath), fileStamp(first(s.Command.Argv)))
	stamp = append(stamp, s.Command.Argv...)
	stamp = append(stamp, s.Command.Env...)
	for _, p := range []string{selected, resolved, s.RealPath, first(s.Command.Argv)} {
		stamp = append(stamp, r.canonical(p))
		if data, e := r.readFile(p); e == nil {
			sum := sha256.Sum256(data)
			stamp = append(stamp, fmt.Sprintf("%x", sum))
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(stamp, "\x00")))
	s.Fingerprint = fmt.Sprintf("%x", sum)
	return s, ctx.Err()
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

var versionPattern = regexp.MustCompile(`(?:^|\s)(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)(?:\s|$)`)

// Version accepts provider version output, preserving prerelease and build suffixes.
func Version(value string) string {
	m := versionPattern.FindStringSubmatch(strings.TrimSpace(strings.TrimPrefix(value, "v")))
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

func (r *Resolver) packageRoot(p string) string {
	for dir := path.Dir(p); dir != "." && dir != "/" && dir != path.Dir(dir); dir = path.Dir(dir) {
		if strings.HasSuffix(dir, "/node_modules/@openai/codex") && r.manifest(dir) {
			return dir
		}
	}
	return ""
}
func (r *Resolver) manifest(dir string) bool {
	data, err := r.readFile(dir + "/package.json")
	var manifest struct {
		Name string            `json:"name"`
		Bin  map[string]string `json:"bin"`
	}
	return err == nil && json.Unmarshal(data, &manifest) == nil && manifest.Name == packageName && manifest.Bin["codex"] == "bin/codex.js"
}

var shimReference = regexp.MustCompile(`(?:\$basedir|%dp0%|%~dp0|\$PSScriptRoot)[/\\]([^"'\r\n]*@openai[/\\]codex[/\\]bin[/\\]codex\.js)`)

func (r *Resolver) shimTarget(p string) string {
	data, err := r.readFile(p)
	if err != nil {
		return ""
	}
	text := string(data)
	// npm/cmd-shim and pnpm generate these signatures. Do not infer ownership
	// just because a nearby package happens to exist.
	if !strings.Contains(text, "basedir=") && !strings.Contains(text, "@ECHO off") && !strings.Contains(text, "$basedir=") {
		return ""
	}
	matches := shimReference.FindAllStringSubmatch(text, -1)
	var target string
	for _, m := range matches {
		resolved := r.canonical(path.Join(path.Dir(p), slash(m[1])))
		if resolved == "" || (target != "" && !r.equal(target, resolved)) {
			return ""
		}
		target = resolved
	}
	return target
}

func (r *Resolver) tool(name, adjacent string) string {
	if adjacent != "" {
		if p, err := r.lookup(adjacent); err == nil {
			return p
		}
	}
	p, _ := r.lookup(name)
	return p
}

var miseToolPrefix = regexp.MustCompile(`/mise/installs/([^/]+)/[^/]+$`)

func isMiseToolPrefix(prefix string) bool {
	parts := miseToolPrefix.FindStringSubmatch(prefix)
	return len(parts) > 1 && parts[1] != "node"
}

func (r *Resolver) resolvePackage(ctx context.Context, s *ports.CodexInstallation, pkg string) {
	// Probe each plausible owner against its actual global root. This also
	// supports custom PNPM_HOME/BUN_INSTALL paths without treating local packages
	// or version-manager shims as global installations.
	for _, manager := range []string{"pnpm", "bun"} {
		marker := "/pnpm/"
		if manager == "bun" {
			marker = "/.bun/"
		}
		custom := r.getenv("PNPM_HOME")
		if manager == "bun" {
			custom = r.getenv("BUN_INSTALL")
		}
		plausible := strings.Contains(strings.ToLower(slash(s.Path)+"/"+pkg), marker) || (custom != "" && strings.HasPrefix(slash(s.Path), slash(custom)+"/"))
		if !plausible {
			continue
		}
		tool := r.tool(manager, path.Join(path.Dir(slash(s.Path)), manager))
		if tool == "" {
			return
		}
		args := []string{tool, "root", "-g"}
		if manager == "bun" {
			args = []string{tool, "pm", "-g", "bin"}
		}
		root, err := r.probe(ctx, args, nil)
		if err != nil {
			return
		}
		if manager == "bun" {
			bin := r.canonical(root)
			if bin == "" || !r.equal(bin, r.canonical(path.Dir(slash(s.Path)))) {
				return
			}
			root = path.Dir(bin) + "/install/global/node_modules"
		}
		if !r.equal(r.canonical(root+"/@openai/codex"), pkg) {
			return
		}
		s.Source, s.Scope = manager, manager+":"+slash(root)
		args = []string{tool, "add", "-g", packageName + "@latest"}
		if manager == "bun" {
			args[1] = "i"
		}
		s.Command = ports.InstallCommand{Argv: args}
		return
	}
	segment := "/lib/node_modules/@openai/codex"
	idx := strings.LastIndex(pkg, segment)
	prefix := ""
	if idx > 0 && idx+len(segment) == len(pkg) && !strings.Contains(pkg[:idx], "/node_modules/") && !isMiseToolPrefix(pkg[:idx]) {
		prefix = pkg[:idx]
	} else if r.goos == "windows" && strings.HasSuffix(pkg, "/node_modules/@openai/codex") {
		prefix = strings.TrimSuffix(pkg, "/node_modules/@openai/codex")
	}
	if prefix == "" {
		return
	}
	adjacent := prefix + "/bin/npm"
	if r.goos == "windows" {
		adjacent = prefix + "/npm.cmd"
	}
	tool := r.tool("npm", adjacent)
	if tool == "" {
		return
	}
	probeArgs := []string{tool, "root", "-g", "--prefix", prefix}
	if r.goos == "windows" {
		probeArgs = []string{tool, "root", "-g"}
	}
	root, err := r.probe(ctx, probeArgs, nil)
	if err != nil || !r.equal(r.canonical(root+"/@openai/codex"), pkg) {
		return
	}
	s.Source, s.Scope = "npm", "npm:"+prefix
	s.Command = ports.InstallCommand{Argv: []string{tool, "install", "-g", "--prefix", prefix, "--allow-scripts=" + packageName, packageName + "@latest"}}
}

var viteInstallID = regexp.MustCompile(`^#?[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

func (r *Resolver) resolveVite(ctx context.Context, s *ports.CodexInstallation) bool {
	bin := path.Dir(slash(s.Path))
	toolName := "vp"
	if r.goos == "windows" {
		toolName = "vp.exe"
	}
	tool := r.tool(toolName, bin+"/"+toolName)
	if tool == "" {
		return false
	}
	toolReal := r.canonical(tool)
	sidecar, _ := r.readFile(strings.TrimSuffix(slash(s.Path), path.Ext(slash(s.Path))) + ".shim")
	trampoline := r.goos == "windows" && strings.HasPrefix(strings.TrimPrefix(string(sidecar), "\ufeff"), "vite-plus-shim-v1\n")
	if !r.equal(toolReal, s.RealPath) && !trampoline {
		return false
	}
	// VP_DUMP_DIRS is a read-only diagnostic in current Vite+. Older versions
	// use a single VP_HOME; require that exact legacy dispatcher path as proof.
	dirs := map[string]string{}
	out, err := r.probe(ctx, []string{tool, "--version"}, []string{"VP_DUMP_DIRS=1"})
	if err == nil {
		for _, line := range strings.Split(out, "\n") {
			k, v, ok := strings.Cut(line, "\t")
			if ok {
				dirs[k] = slash(v)
			}
		}
	}
	if dirs["data"] == "" && strings.Contains(slash(s.Path), "/.vite-plus/bin/") {
		dirs["data"], dirs["bin"] = path.Dir(bin), bin
		if override := r.getenv("VP_HOME"); override != "" && !r.equal(override, dirs["data"]) {
			return true
		}
	}
	root := dirs["data"]
	if root == "" || !r.equal(r.canonical(dirs["bin"]), r.canonical(bin)) {
		return true
	}
	if trampoline && !strings.Contains(slash(string(sidecar)), "data="+root+"\n") {
		return true
	}
	var owner struct{ Name, Package, Source, Version string }
	data, err := r.readFile(root + "/bins/codex.json")
	if err != nil || json.Unmarshal(data, &owner) != nil || owner.Name != "codex" || owner.Package != packageName || (owner.Source != "" && owner.Source != "vp") {
		return true
	}
	// Vite+ uses camelCase installId and a UUID-specific directory, or a legacy
	// package directory. Reject traversal rather than accepting arbitrary paths.
	var metadata struct {
		Name      string
		Version   string
		InstallID string `json:"installId"`
		Bins      []string
	}
	data, err = r.readFile(root + "/packages/@openai/codex.json")
	if err != nil || json.Unmarshal(data, &metadata) != nil {
		return true
	}
	if metadata.Name != packageName || metadata.Version != s.Version || (owner.Version != "" && owner.Version != s.Version) {
		return true
	}
	hasBin := false
	for _, b := range metadata.Bins {
		if b == "codex" {
			hasBin = true
		}
	}
	if !hasBin {
		return true
	}
	prefix := root + "/packages/@openai/codex"
	if metadata.InstallID != "" {
		if !viteInstallID.MatchString(metadata.InstallID) {
			return true
		}
		if strings.HasPrefix(metadata.InstallID, "#") {
			prefix += metadata.InstallID
		} else {
			prefix += "/" + metadata.InstallID
		}
	}
	pkg := prefix + "/lib/node_modules/@openai/codex"
	if !r.manifest(pkg) {
		pkg = prefix + "/node_modules/@openai/codex"
	}
	if !r.manifest(pkg) {
		return true
	}
	target := r.canonical(pkg + "/bin/codex.js")
	if target == "" {
		return true
	}
	s.RealPath, s.Source, s.Scope = target, "vite-plus", "vite-plus:"+root
	s.Command = ports.InstallCommand{Argv: []string{tool, "i", "-g", packageName}}
	return true
}

var kegPattern = regexp.MustCompile(`(?i)^(.*)/(Cellar|Caskroom)/(codex)/[^/]+/`)

func (r *Resolver) resolveBrew(ctx context.Context, s *ports.CodexInstallation) {
	m := kegPattern.FindStringSubmatch(s.RealPath)
	if len(m) == 0 {
		return
	}
	s.Source, s.VersionSource = "homebrew", "homebrew"
	tool := r.tool("brew", m[1]+"/bin/brew")
	if tool == "" {
		return
	}
	prefix, err := r.probe(ctx, []string{tool, "--prefix"}, nil)
	if err != nil || !r.equal(r.canonical(prefix), m[1]) {
		return
	}
	kind := "--formula"
	if strings.EqualFold(m[2], "Caskroom") {
		kind = "--cask"
	}
	files, err := r.probe(ctx, []string{tool, "list", kind, "codex"}, nil)
	if err != nil {
		return
	}
	owned := false
	for _, file := range strings.Split(files, "\n") {
		if r.equal(r.canonical(strings.TrimSpace(file)), s.RealPath) {
			owned = true
		}
	}
	if !owned {
		return
	}
	s.Scope = "homebrew:" + m[1]
	s.Command = ports.InstallCommand{Argv: []string{tool, "upgrade", kind, "codex"}}
}

type limitedBuffer struct {
	data     bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.data.Len()+n > maxRead {
		b.overflow = true
		p = p[:maxRead-b.data.Len()]
	}
	_, _ = b.data.Write(p)
	return n, nil
}

func (r *Resolver) probe(ctx context.Context, argv, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out := &limitedBuffer{}
	if r.launchEnv != nil {
		env = append(r.launchEnv(ctx, first(argv)), env...)
	}
	err := r.runner.RunInstall(ctx, ports.InstallCommand{Argv: argv, Env: append([]string{"CI=1", "NONINTERACTIVE=1", "HOMEBREW_NO_AUTO_UPDATE=1"}, env...), ReadOnly: true}, out, io.Discard)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	if out.overflow {
		return "", fmt.Errorf("installer probe exceeded output limit")
	}
	return strings.TrimSpace(out.data.String()), nil
}

// Latest uses Homebrew's own catalog for Homebrew ownership; all other sources
// use the upstream npm stable tag, like T3. No version read runs an upgrade.
func (r *Resolver) Latest(ctx context.Context, s ports.CodexInstallation) (string, error) {
	if s.VersionSource == "homebrew" {
		if len(s.Command.Argv) < 4 {
			return "", fmt.Errorf("owning Homebrew installation could not be verified")
		}
		out, err := r.probe(ctx, []string{s.Command.Argv[0], "info", "--json=v2", s.Command.Argv[2], "codex"}, nil)
		if err != nil {
			return "", err
		}
		var info struct {
			Formulae []struct {
				Name     string
				Versions struct{ Stable string }
			} `json:"formulae"`
			Casks []struct {
				Token   string
				Version string
			} `json:"casks"`
		}
		if err := json.Unmarshal([]byte(out), &info); err != nil {
			return "", err
		}
		if s.Command.Argv[2] == "--formula" && len(info.Formulae) == 1 && info.Formulae[0].Name == "codex" {
			return validatedVersion(info.Formulae[0].Versions.Stable)
		}
		if s.Command.Argv[2] == "--cask" && len(info.Casks) == 1 && info.Casks[0].Token == "codex" {
			return validatedVersion(strings.Split(info.Casks[0].Version, ",")[0])
		}
		return "", fmt.Errorf("owning Homebrew catalog did not return the Codex package version")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://registry.npmjs.org/@openai%2Fcodex/latest", http.NoBody)
	if err != nil {
		return "", err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("version source returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRead+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxRead {
		return "", fmt.Errorf("version response exceeds size limit")
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return "", err
	}
	return validatedVersion(info.Version)
}

func validatedVersion(v string) (string, error) {
	if parsed := Version(v); parsed != "" {
		return parsed, nil
	}
	return "", fmt.Errorf("version source returned an unrecognized version")
}

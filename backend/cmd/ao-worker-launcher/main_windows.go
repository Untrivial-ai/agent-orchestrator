//go:build windows

package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/aoagents/agent-orchestrator/backend/internal/workeridentity"
	"github.com/aoagents/agent-orchestrator/backend/internal/workerlauncher"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: ao-worker-launcher <install|bootstrap|provider|worker>")
	}
	var err error
	switch os.Args[1] {
	case "install":
		err = install(os.Args[2:])
	case "bootstrap":
		err = bootstrap(os.Args[2:])
	case "provider":
		err = provider(os.Args[2:])
	case "worker":
		err = worker(os.Args[2:])
	case "service":
		err = service(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fail(err.Error())
	}
}

func provider(args []string) error {
	fs := flag.NewFlagSet("provider", flag.ContinueOnError)
	config := fs.String("config", "", "worker identity config")
	worktree := fs.String("worktree", "", "managed worktree")
	sessionID := fs.String("session-id", "", "Claude native session ID for run or resume")
	prompt := fs.String("prompt", "", "Claude prompt for run or resume")
	if err := fs.Parse(args); err != nil {
		return err
	}
	remaining := fs.Args()
	if len(remaining) != 2 {
		return fmt.Errorf("usage: ao-worker-launcher provider --config <path> --worktree <path> [--session-id <id> --prompt <text>] <status|login|run|resume> <claude|codex>")
	}
	opts := workerlauncher.ProviderCommandOptions{SessionID: *sessionID, Prompt: *prompt}
	return workerlauncher.RunProviderCommand(*config, *worktree, remaining[0], remaining[1], opts, os.Stdin, os.Stdout)
}

func service(args []string) error {
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	config := fs.String("config", "", "identity config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return workerlauncher.RunLauncherService(*config)
}

func install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "AO data directory")
	account := fs.String("account", workeridentity.DefaultAccountName, "dedicated local account")
	workspace := fs.String("workspace-root", "", "managed worker workspace root")
	profiles := fs.String("profile-root", "", "worker session profile root")
	ao := fs.String("ao", "", "trusted ao executable")
	claude := fs.String("claude", "", "trusted Claude executable")
	codex := fs.String("codex", "", "trusted Codex executable")
	shell := fs.String("shell", filepath.Join(os.Getenv("SYSTEMROOT"), "System32", "cmd.exe"), "trusted shell executable")
	git := fs.String("git", "", "trusted Git executable")
	serviceName := fs.String("service-name", "AOAgentWorkerLauncher", "Windows launcher service name")
	var gitRoots stringList
	fs.Var(&gitRoots, "git-metadata-root", "trusted linked-worktree common Git directory (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	launcher, err := os.Executable()
	if err != nil {
		return err
	}
	executables := map[string]string{workerlauncher.ExecutableAOAgent: *ao, workerlauncher.ExecutableShell: *shell}
	if *claude != "" {
		executables[workerlauncher.ExecutableClaude] = *claude
	}
	if *codex != "" {
		executables[workerlauncher.ExecutableCodex] = *codex
	}
	if *git != "" {
		executables["git"] = *git
	}
	_, err = workeridentity.Install(workeridentity.InstallOptions{DataDir: *dataDir, AccountName: *account,
		WorkspaceRoot: *workspace, SessionProfileRoot: *profiles, LauncherPath: launcher, PTYHostPath: *ao,
		Executables: executables, GitMetadataRoots: gitRoots, ServiceName: *serviceName})
	if err == nil {
		fmt.Fprintln(os.Stdout, "Windows worker identity initialized")
	}
	return err
}

func bootstrap(args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	config := fs.String("config", "", "identity config")
	manifest := fs.String("manifest", "", "session manifest")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ready, err := workerlauncher.Bootstrap(*config, *manifest)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "READY:%d %s\n", ready.LauncherPID, portOf(ready.Address))
	return nil
}

func worker(args []string) error {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	config := fs.String("config", "", "identity config")
	manifest := fs.String("manifest", "", "session manifest")
	ready := fs.String("ready", "", "readiness address")
	nonce := fs.String("ready-nonce", "", "readiness nonce")
	logonSID := fs.String("logon-sid", "", "batch Logon SID")
	profileRoot := fs.String("profile-root", "", "loaded Windows user profile")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return workerlauncher.RunWorker(*config, *manifest, *ready, *nonce, *logonSID, *profileRoot)
}

func portOf(address string) string {
	_, port, err := netSplitHostPort(address)
	if err != nil {
		return "0"
	}
	return port
}

var netSplitHostPort = func(address string) (string, string, error) { return net.SplitHostPort(address) }

func fail(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }

type stringList []string

func (s *stringList) String() string         { return fmt.Sprint([]string(*s)) }
func (s *stringList) Set(value string) error { *s = append(*s, value); return nil }

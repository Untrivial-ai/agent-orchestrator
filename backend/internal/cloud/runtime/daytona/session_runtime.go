package daytona

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	daytonasdk "github.com/daytona/clients/sdk-go/pkg/daytona"
	"github.com/daytona/clients/sdk-go/pkg/options"
	"github.com/daytona/clients/sdk-go/pkg/types"

	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/domain"
)

const sessionName = "ao-agent"

var environmentKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ProvisionSessionRuntime creates exactly one sandbox for one AO session. The
// same path is used for orchestrators and workers; no agent ever shares compute
// with another session or with the project coordinator.
func (p *Provider) ProvisionSessionRuntime(ctx context.Context, workspace domain.Workspace, launch domain.RuntimeLaunch) (string, error) {
	zero := 0
	shortID := strings.ReplaceAll(launch.SessionID, "-", "")
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	sandbox, err := p.client.Create(ctx, types.SnapshotParams{
		Snapshot: "daytona-small",
		SandboxBaseParams: types.SandboxBaseParams{
			Name: "ao-session-" + shortID,
			Labels: map[string]string{
				"ao.cloud.workspace": workspace.ID,
				"ao.cloud.session":   launch.SessionID,
			},
			AutoPauseInterval: &zero,
		},
	}, options.WithTimeout(5*time.Minute))
	if err != nil {
		return "", fmt.Errorf("create Daytona session sandbox: %w", err)
	}
	if err = p.bootstrapSessionRuntime(ctx, sandbox, workspace, launch); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if cleanupErr := sandbox.Delete(cleanupCtx); cleanupErr != nil {
			return "", errors.Join(err, fmt.Errorf("delete failed Daytona session sandbox %q: %w", sandbox.ID, cleanupErr))
		}
		return "", err
	}
	return sandbox.ID, nil
}

func (p *Provider) bootstrapSessionRuntime(ctx context.Context, sandbox *daytonasdk.Sandbox, workspace domain.Workspace, launch domain.RuntimeLaunch) error {
	homeResult, err := run(ctx, sandbox, `printf %s "$HOME"`, time.Minute)
	if err != nil {
		return err
	}
	home := strings.TrimSpace(homeResult)
	if home == "" || !filepath.IsAbs(home) {
		return errors.New("daytona sandbox returned an invalid home directory") //nolint:staticcheck // Daytona is a product name.
	}
	root := filepath.Join(home, "workspace")
	archivePath := filepath.Join(home, ".ao", "workspace.tar.gz")
	claudePath := filepath.Join(home, ".claude", ".credentials.json")
	githubTokenPath := filepath.Join(home, ".ao", "github-token")
	askpassPath := filepath.Join(home, ".ao", "github-askpass")
	aoPath := filepath.Join(home, "bin", "ao")
	filesRoot := filepath.Join(home, ".ao", "runtime-files")

	if _, err = run(ctx, sandbox, `sudo apt-get update -qq && sudo apt-get install -y -qq ca-certificates curl git tmux && sudo env PATH="$PATH" npm install -g @anthropic-ai/claude-code`, 10*time.Minute); err != nil {
		return fmt.Errorf("install session dependencies: %w", err)
	}
	if _, err = run(ctx, sandbox, "mkdir -p "+shellQuote(filepath.Dir(archivePath))+" "+shellQuote(filepath.Dir(claudePath))+" "+shellQuote(filepath.Dir(aoPath))+" "+shellQuote(filesRoot), time.Minute); err != nil {
		return err
	}
	if err = sandbox.FileSystem.UploadFile(ctx, p.aoBinary, aoPath); err != nil {
		return fmt.Errorf("upload AO binary: %w", err)
	}
	if err = sandbox.FileSystem.UploadFile(ctx, launch.ClaudeCredentials, claudePath); err != nil {
		return fmt.Errorf("upload Claude credentials: %w", err)
	}
	if err := sandbox.FileSystem.UploadFile(ctx, []byte("{\"hasCompletedOnboarding\":true}\n"), filepath.Join(home, ".claude.json")); err != nil {
		return err
	}
	if len(p.githubToken) > 0 {
		if err := sandbox.FileSystem.UploadFile(ctx, p.githubToken, githubTokenPath); err != nil {
			return err
		}
		askpass := "#!/bin/sh\ncase \"$1\" in *Username*) printf '%s\\n' x-access-token ;; *) cat " + shellQuote(githubTokenPath) + " ;; esac\n"
		if err := sandbox.FileSystem.UploadFile(ctx, []byte(askpass), askpassPath); err != nil {
			return err
		}
	}
	if _, err = run(ctx, sandbox, "chmod 0755 "+shellQuote(aoPath)+" "+shellQuote(askpassPath)+" && chmod 0600 "+shellQuote(claudePath)+" "+shellQuote(githubTokenPath), time.Minute); err != nil {
		return err
	}

	clone := "GIT_TERMINAL_PROMPT=0"
	if len(p.githubToken) > 0 {
		clone += " GIT_ASKPASS=" + shellQuote(askpassPath)
	}
	clone += " git clone " + shellQuote(workspace.RepositoryURL) + " " + shellQuote(root)
	if _, err = run(ctx, sandbox, clone, 10*time.Minute); err != nil {
		return fmt.Errorf("clone session repository: %w", err)
	}
	if launch.Branch != "" {
		checkout := "git -C " + shellQuote(root) + " checkout -B " + shellQuote(launch.Branch)
		if workspace.RepositoryRef != "" {
			checkout += " " + shellQuote("origin/"+workspace.RepositoryRef)
		}
		if _, err = run(ctx, sandbox, checkout, 2*time.Minute); err != nil {
			return fmt.Errorf("create session branch: %w", err)
		}
	}
	if len(launch.WorkspaceArchive) > 0 {
		if err = sandbox.FileSystem.UploadFile(ctx, launch.WorkspaceArchive, archivePath); err != nil {
			return fmt.Errorf("upload prepared workspace: %w", err)
		}
		if _, err = run(ctx, sandbox, "tar -xzf "+shellQuote(archivePath)+" -C "+shellQuote(root)+" && rm -f "+shellQuote(archivePath), 3*time.Minute); err != nil {
			return fmt.Errorf("extract prepared workspace: %w", err)
		}
	}

	argv := append([]string(nil), launch.Argv...)
	pathMap := map[string]string{launch.SourceWorkspace: root}
	for i, file := range launch.Files {
		destination := filepath.Join(filesRoot, strconv.Itoa(i)+"-"+filepath.Base(file.SourcePath))
		if err = sandbox.FileSystem.UploadFile(ctx, file.Data, destination); err != nil {
			return fmt.Errorf("upload runtime file: %w", err)
		}
		pathMap[file.SourcePath] = destination
	}
	if len(argv) > 0 && filepath.IsAbs(argv[0]) {
		pathMap[argv[0]] = aoPath
	}
	for index := range argv {
		argv[index] = replaceRuntimePaths(argv[index], pathMap)
	}
	env := make(map[string]string, len(launch.Env)+3)
	for key, value := range launch.Env {
		// Coordinator-only endpoints and filesystem roots cannot be reached from
		// isolated compute. Provider credentials are supplied separately.
		if !environmentKeyPattern.MatchString(key) || strings.HasPrefix(key, "AO_BROWSER_CAPABILITY") {
			continue
		}
		env[key] = replaceRuntimePaths(value, pathMap)
	}
	env["HOME"] = home
	env["TERM"] = "xterm-256color"
	env["PATH"] = filepath.Join(home, "bin") + ":/usr/local/bin:/usr/bin:/bin"
	coordinatorURL, err := p.previewURL(ctx, workspace.SandboxID, 24*time.Hour)
	if err != nil {
		return fmt.Errorf("create coordinator capability: %w", err)
	}
	env["AO_CLOUD_COORDINATOR_URL"] = coordinatorURL
	command := "cd " + shellQuote(root) + " && exec env"
	for _, key := range sortedKeys(env) {
		command += " " + key + "=" + shellQuote(env[key])
	}
	for _, arg := range argv {
		command += " " + shellQuote(arg)
	}
	start := "tmux new-session -d -s " + shellQuote(sessionName) + " " + shellQuote(command)
	if _, err = run(ctx, sandbox, start, time.Minute); err != nil {
		return fmt.Errorf("start isolated agent session: %w", err)
	}
	return nil
}

// DeleteSessionRuntime removes all compute and disk for one agent session.
func (p *Provider) DeleteSessionRuntime(ctx context.Context, sandboxID string) error {
	sandbox, err := p.client.Get(ctx, strings.TrimSpace(sandboxID))
	if err != nil {
		return fmt.Errorf("get Daytona session sandbox: %w", err)
	}
	if err = sandbox.Delete(ctx); err != nil {
		return fmt.Errorf("delete Daytona session sandbox: %w", err)
	}
	return nil
}

// SessionRuntimeAlive probes the agent's tmux session.
func (p *Provider) SessionRuntimeAlive(ctx context.Context, sandboxID string) (bool, error) {
	sandbox, err := p.client.Get(ctx, strings.TrimSpace(sandboxID))
	if err != nil {
		return false, fmt.Errorf("get Daytona session sandbox: %w", err)
	}
	result, err := sandbox.Process.ExecuteCommand(ctx, "tmux has-session -t "+shellQuote(sessionName), options.WithExecuteTimeout(time.Minute))
	if err != nil {
		return false, err
	}
	return result.ExitCode == 0, nil
}

// SessionRuntimeOutput captures bounded terminal history.
func (p *Provider) SessionRuntimeOutput(ctx context.Context, sandboxID string, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	sandbox, err := p.client.Get(ctx, strings.TrimSpace(sandboxID))
	if err != nil {
		return "", err
	}
	return run(ctx, sandbox, "tmux capture-pane -p -t "+shellQuote(sessionName)+" -S -"+strconv.Itoa(lines), time.Minute)
}

// SessionRuntimeInput pastes input and optionally submits it.
func (p *Provider) SessionRuntimeInput(ctx context.Context, sandboxID, input string, enter bool) error {
	sandbox, err := p.client.Get(ctx, strings.TrimSpace(sandboxID))
	if err != nil {
		return err
	}
	command := "tmux set-buffer -- " + shellQuote(input) + " && tmux paste-buffer -t " + shellQuote(sessionName)
	if enter {
		command += " && tmux send-keys -t " + shellQuote(sessionName) + " Enter"
	}
	_, err = run(ctx, sandbox, command, time.Minute)
	return err
}

// SessionRuntimeInterrupt sends Ctrl-C to the isolated agent.
func (p *Provider) SessionRuntimeInterrupt(ctx context.Context, sandboxID string) error {
	sandbox, err := p.client.Get(ctx, strings.TrimSpace(sandboxID))
	if err != nil {
		return err
	}
	_, err = run(ctx, sandbox, "tmux send-keys -t "+shellQuote(sessionName)+" C-c", time.Minute)
	return err
}

func replaceRuntimePaths(value string, replacements map[string]string) string {
	for source, destination := range replacements {
		if source != "" {
			value = strings.ReplaceAll(value, source, destination)
		}
	}
	return value
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

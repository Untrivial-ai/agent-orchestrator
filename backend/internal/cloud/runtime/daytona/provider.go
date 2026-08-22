// Package daytona provisions the complete AO daemon inside Daytona sandboxes.
package daytona

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	daytonasdk "github.com/daytona/clients/sdk-go/pkg/daytona"
	"github.com/daytona/clients/sdk-go/pkg/options"
	"github.com/daytona/clients/sdk-go/pkg/types"

	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/domain"
)

const daemonPort = 3001

// Config contains server-side runtime credentials and the AO worker artifact.
type Config struct {
	APIKey       string
	APIURL       string
	Target       string
	AOBinaryPath string
	GitHubToken  []byte
}

// Provider owns Daytona sandbox creation and signed AO preview links.
type Provider struct {
	client      *daytonasdk.Client
	aoBinary    []byte
	githubToken []byte
}

// New validates credentials and creates a Daytona client.
func New(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("Daytona API key is required") //nolint:staticcheck // Daytona is a product name.
	}
	aoBinary, err := os.ReadFile(strings.TrimSpace(cfg.AOBinaryPath))
	if err != nil {
		return nil, fmt.Errorf("read AO sandbox binary: %w", err)
	}
	if len(cfg.GitHubToken) == 0 {
		return nil, errors.New("GitHub token is required")
	}
	client, err := daytonasdk.NewClientWithConfig(&types.DaytonaConfig{
		APIKey: strings.TrimSpace(cfg.APIKey),
		APIUrl: strings.TrimSpace(cfg.APIURL),
		Target: strings.TrimSpace(cfg.Target),
	})
	if err != nil {
		return nil, fmt.Errorf("create Daytona client: %w", err)
	}
	return &Provider{
		client:      client,
		aoBinary:    aoBinary,
		githubToken: append([]byte(nil), cfg.GitHubToken...),
	}, nil
}

// Close releases Daytona event-stream resources.
func (p *Provider) Close(ctx context.Context) error {
	return p.client.Close(ctx)
}

// Provision creates one sandbox, installs the existing AO daemon and Claude
// harness, clones the real repository, and registers it as an AO project.
func (p *Provider) Provision(ctx context.Context, workspace domain.Workspace, bootstrap domain.WorkspaceBootstrap) (string, error) {
	zero := 0
	sandbox, err := p.client.Create(ctx, types.SnapshotParams{
		Snapshot: "daytona-small",
		SandboxBaseParams: types.SandboxBaseParams{
			Name:              "ao-" + strings.ReplaceAll(workspace.ID, "-", "")[:12],
			Labels:            map[string]string{"ao.cloud.workspace": workspace.ID},
			AutoPauseInterval: &zero,
		},
	}, options.WithTimeout(5*time.Minute))
	if err != nil {
		return "", fmt.Errorf("create Daytona sandbox: %w", err)
	}
	if err := p.bootstrap(ctx, sandbox, workspace, bootstrap); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if cleanupErr := sandbox.Delete(cleanupCtx); cleanupErr != nil {
			return "", errors.Join(err, fmt.Errorf("delete failed Daytona sandbox %q: %w", sandbox.ID, cleanupErr))
		}
		return "", err
	}
	return sandbox.ID, nil
}

// PreviewURL returns a fresh one-hour signed URL for the sandbox AO daemon.
func (p *Provider) PreviewURL(ctx context.Context, sandboxID string) (string, error) {
	return p.previewURL(ctx, sandboxID, time.Hour)
}

func (p *Provider) previewURL(ctx context.Context, sandboxID string, ttl time.Duration) (string, error) {
	sandbox, err := p.client.Get(ctx, strings.TrimSpace(sandboxID))
	if err != nil {
		return "", fmt.Errorf("get Daytona sandbox: %w", err)
	}
	preview, err := sandbox.GetSignedPreviewLink(ctx, daemonPort, int(ttl.Seconds()))
	if err != nil {
		return "", fmt.Errorf("create Daytona preview URL: %w", err)
	}
	return preview.URL, nil
}

func (p *Provider) bootstrap(ctx context.Context, sandbox *daytonasdk.Sandbox, workspace domain.Workspace, bootstrap domain.WorkspaceBootstrap) error {
	homeResult, err := run(ctx, sandbox, `printf %s "$HOME"`, time.Minute)
	if err != nil {
		return err
	}
	home := strings.TrimSpace(homeResult)
	if home == "" || !filepath.IsAbs(home) {
		return errors.New("Daytona sandbox returned an invalid home directory") //nolint:staticcheck // Daytona is a product name.
	}
	binPath := filepath.Join(home, "bin", "ao")
	claudePath := filepath.Join(home, ".claude", ".credentials.json")
	claudeConfigPath := filepath.Join(home, ".claude.json")
	githubTokenPath := filepath.Join(home, ".ao", "github-token")
	askpassPath := filepath.Join(home, ".ao", "github-askpass")
	workspacePath := filepath.Join(home, "workspace", "ao-"+strings.ReplaceAll(workspace.ID, "-", "")[:12])

	if _, err := run(ctx, sandbox,
		`sudo apt-get update -qq && sudo apt-get install -y -qq ca-certificates curl git tmux && sudo env PATH="$PATH" npm install -g @anthropic-ai/claude-code`,
		10*time.Minute); err != nil {
		return fmt.Errorf("install sandbox dependencies: %w", err)
	}
	if _, err := run(ctx, sandbox, "mkdir -p "+shellQuote(filepath.Dir(binPath))+" "+
		shellQuote(filepath.Dir(claudePath))+" "+shellQuote(filepath.Dir(githubTokenPath))+" "+
		shellQuote(filepath.Dir(workspacePath)), time.Minute); err != nil {
		return err
	}
	if err := sandbox.FileSystem.UploadFile(ctx, p.aoBinary, binPath); err != nil {
		return fmt.Errorf("upload AO daemon: %w", err)
	}
	if err := sandbox.FileSystem.UploadFile(ctx, bootstrap.ClaudeCredentials, claudePath); err != nil {
		return fmt.Errorf("upload Claude credentials: %w", err)
	}
	// Credentials alone do not suppress Claude Code's first-run theme and login
	// wizard. That interactive wizard blocks AO's initial prompt indefinitely in
	// an unattended sandbox. Workspace trust remains additive and is written by
	// the existing Claude adapter immediately before each launch.
	if err := sandbox.FileSystem.UploadFile(ctx, []byte("{\"hasCompletedOnboarding\":true}\n"), claudeConfigPath); err != nil {
		return fmt.Errorf("initialize Claude profile: %w", err)
	}
	if len(p.githubToken) > 0 {
		if err := sandbox.FileSystem.UploadFile(ctx, p.githubToken, githubTokenPath); err != nil {
			return fmt.Errorf("upload GitHub credential: %w", err)
		}
		askpass := "#!/bin/sh\ncase \"$1\" in *Username*) printf '%s\\n' x-access-token ;; *) cat " + shellQuote(githubTokenPath) + " ;; esac\n"
		if err := sandbox.FileSystem.UploadFile(ctx, []byte(askpass), askpassPath); err != nil {
			return fmt.Errorf("upload GitHub askpass helper: %w", err)
		}
	}
	if _, err := run(ctx, sandbox,
		"chmod 0755 "+shellQuote(binPath)+" "+shellQuote(askpassPath)+" && chmod 0600 "+
			shellQuote(claudePath)+" "+shellQuote(claudeConfigPath)+" "+shellQuote(githubTokenPath), time.Minute); err != nil {
		return err
	}

	clone := "GIT_TERMINAL_PROMPT=0"
	if len(p.githubToken) > 0 {
		clone += " GIT_ASKPASS=" + shellQuote(askpassPath)
	}
	clone += " git clone --depth 1"
	if workspace.RepositoryRef != "" {
		clone += " --branch " + shellQuote(workspace.RepositoryRef)
	}
	clone += " " + shellQuote(workspace.RepositoryURL) + " " + shellQuote(workspacePath)
	if _, err := run(ctx, sandbox, clone, 10*time.Minute); err != nil {
		return fmt.Errorf("clone repository: %w", err)
	}

	dataDir := filepath.Join(home, ".ao", "data")
	runFile := filepath.Join(home, ".ao", "running.json")
	logFile := filepath.Join(home, ".ao", "daemon.log")
	start := "nohup env AO_DATA_DIR=" + shellQuote(dataDir) + " AO_RUN_FILE=" + shellQuote(runFile) +
		" AO_PORT=3001 AO_CORS_HEADERS_MANAGED_BY_PROXY=on" +
		" GIT_TERMINAL_PROMPT=0 GIT_ASKPASS=" + shellQuote(askpassPath) +
		" AO_CLOUD_RUNTIME_API_URL=" + shellQuote(bootstrap.ControlPlaneURL) +
		" AO_CLOUD_RUNTIME_TOKEN=" + shellQuote(bootstrap.RuntimeToken) +
		" AO_CLOUD_WORKSPACE_ID=" + shellQuote(workspace.ID) +
		" " + shellQuote(binPath) + " daemon >" + shellQuote(logFile) + " 2>&1 </dev/null &"
	if _, err := run(ctx, sandbox, start, time.Minute); err != nil {
		return fmt.Errorf("start AO daemon: %w", err)
	}
	if _, err := run(ctx, sandbox,
		`for i in $(seq 1 120); do curl -fsS http://127.0.0.1:3001/readyz >/dev/null && exit 0; sleep 1; done; exit 1`,
		3*time.Minute); err != nil {
		return fmt.Errorf("wait for AO daemon: %w", err)
	}
	addProject := "env AO_DATA_DIR=" + shellQuote(dataDir) + " AO_RUN_FILE=" + shellQuote(runFile) +
		" " + shellQuote(binPath) + " project add --path " + shellQuote(workspacePath) +
		" --id cloud --name " + shellQuote(repositoryName(workspace.RepositoryURL)) +
		" --worker-agent claude-code --orchestrator-agent claude-code"
	if _, err := run(ctx, sandbox, addProject, 2*time.Minute); err != nil {
		return fmt.Errorf("register cloud project: %w", err)
	}
	return nil
}

func run(ctx context.Context, sandbox *daytonasdk.Sandbox, command string, timeout time.Duration) (string, error) {
	result, err := sandbox.Process.ExecuteCommand(ctx, command, options.WithExecuteTimeout(timeout))
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		output := strings.TrimSpace(result.Result)
		if len(output) > 1200 {
			output = output[len(output)-1200:]
		}
		return "", fmt.Errorf("sandbox command exited %d: %s", result.ExitCode, output)
	}
	return result.Result, nil
}

func repositoryName(raw string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(raw), ".git")
	if slash := strings.LastIndex(trimmed, "/"); slash >= 0 && slash+1 < len(trimmed) {
		return trimmed[slash+1:]
	}
	return "Cloud Project"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

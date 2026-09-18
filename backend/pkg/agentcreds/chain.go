package agentcreds

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/processenv"
)

// Chain-sourced credentials.
//
// Bedrock and Vertex can both draw credentials from sources no amount of code
// here can read: IMDS, ECS/EKS container credentials, SSO profiles,
// credential_process executables, workload identity federation, external
// account files. Reimplementing those is not a feature — it is adopting two
// credential-chain implementations and maintaining them permanently, in
// modules that today have zero AWS, GCP, or Azure dependencies.
//
// So this file does not resolve the chain. It asks the tool that already has:
// the provider's own CLI, which the user has necessarily configured if they
// are using chain credentials at all. That is consistent with how AO shells
// out to agent binaries everywhere else.
//
// Everything here is optional and fails soft. A missing CLI, a timeout, a
// non-zero exit — all Unknown, never a rejection. The runtime 401 handler
// remains the real coverage for this tier; this only buys earliness.

// commandRunner is the narrow keychain helper seam.
type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, path, args...).CombinedOutput()
}

type commandInvocation struct {
	WorkingDir string
	Env        map[string]string
}

type commandOutput struct {
	Stdout []byte
	Stderr []byte
}

type providerCommandRunner func(context.Context, commandInvocation, string, ...string) (commandOutput, error)

func execProviderCommand(ctx context.Context, invocation commandInvocation, name string, args ...string) (commandOutput, error) {
	environment := processenv.Merge(invocation.Env)
	path, err := lookPathInEnvironment(name, environment)
	if err != nil {
		return commandOutput{}, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = strings.TrimSpace(invocation.WorkingDir)
	cmd.Env = environment
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return commandOutput{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, err
}

func lookPathInEnvironment(name string, environment []string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		return exec.LookPath(name)
	}
	var searchPath string
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "PATH") {
			searchPath = value
			break
		}
	}
	for _, dir := range filepath.SplitList(searchPath) {
		candidate := filepath.Join(dir, name)
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func (v *Validator) validateVertexViaCLI(ctx context.Context, project, region, baseURL string, invocation commandInvocation) Result {
	result := Result{Provider: ProviderVertex, Source: "gcloud", CheckedAt: time.Now()}
	out, err := v.execCmd(ctx, invocation, "gcloud", "auth", "print-access-token")
	if err != nil {
		result.State = StateUnknown
		result.Detail = "gcloud is unavailable, so Vertex credentials could not be resolved"
		result.Err = commandDiagnosticError(err, out.Stderr)
		return result
	}
	token := strings.TrimSpace(string(out.Stdout))
	if token == "" {
		result.State = StateUnknown
		result.Detail = "gcloud returned no access token"
		return result
	}
	return v.Validate(ctx, Credential{
		Kind: KindGoogleAccessToken, Secret: token, Source: "gcloud",
		Provider: ProviderVertex, Project: project, Region: region, BaseURL: baseURL,
	})
}

func (v *Validator) validateBedrockViaCLI(ctx context.Context, region string, invocation commandInvocation) Result {
	result := Result{Provider: ProviderBedrock, Source: "aws", CheckedAt: time.Now()}
	args := []string{"bedrock", "list-foundation-models", "--by-provider", "anthropic", "--output", "json"}
	if strings.TrimSpace(region) != "" {
		args = append(args, "--region", region)
	}
	out, err := v.execCmd(ctx, invocation, "aws", args...)
	if err != nil {
		result.State = StateUnknown
		result.Detail = "the aws CLI is unavailable or could not list Bedrock models"
		result.Err = commandDiagnosticError(err, out.Stderr)
		return result
	}
	models, parseErr := parseBedrockModels(out.Stdout)
	if parseErr != nil {
		result.State = StateUnknown
		result.Detail = "the aws CLI returned output this build could not read"
		result.Err = parseErr
		return result
	}
	result.Models = models
	if len(models) == 0 {
		result.State = StateUnknown
		result.Detail = "the aws CLI listed no Anthropic models; this account may lack Bedrock access to Claude"
		return result
	}
	result.State = StateUnknown
	result.Detail = "the aws CLI listed Claude models on Bedrock, but invocation permission was not verified"
	return result
}

func parseBedrockModels(body []byte) ([]Model, error) {
	var payload struct {
		ModelSummaries []struct {
			ModelID      string `json:"modelId"`
			ProviderName string `json:"providerName"`
		} `json:"modelSummaries"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(payload.ModelSummaries))
	for _, summary := range payload.ModelSummaries {
		if isClaudeModelID(summary.ModelID) || strings.EqualFold(summary.ProviderName, "Anthropic") {
			models = append(models, Model{ID: summary.ModelID})
		}
	}
	return models, nil
}

func commandDiagnosticError(err error, stderr []byte) error {
	detail := strings.TrimSpace(string(stderr))
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}

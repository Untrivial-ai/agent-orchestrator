package agentcreds

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
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
	path, err := exec.LookPath(name)
	if err != nil {
		return commandOutput{}, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = strings.TrimSpace(invocation.WorkingDir)
	cmd.Env = commandEnvironment(invocation.Env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return commandOutput{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, err
}

func commandEnvironment(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return os.Environ()
	}
	values := make(map[string]string, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = entry
		}
	}
	for key, value := range overrides {
		values[key] = key + "=" + value
	}
	out := make([]string, 0, len(values))
	for _, entry := range values {
		out = append(out, entry)
	}
	sort.Strings(out)
	return out
}

func newWithCommandRunner(client *http.Client, runner providerCommandRunner) *Validator {
	v := New(client)
	if runner != nil {
		v.execCmd = runner
	}
	return v
}

// ValidateVertexViaCLI asks gcloud for an access token and probes with it.
//
// gcloud has already resolved whatever the chain holds — user login, service
// account impersonation, workload identity federation — so one command turns
// an unresolvable credential into an ordinary bearer token.
func (v *Validator) ValidateVertexViaCLI(ctx context.Context, project, region string) Result {
	return v.validateVertexViaCLI(ctx, project, region, "", commandInvocation{})
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
	token := strings.TrimSpace(lastNonEmptyLine(string(out.Stdout)))
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

// ValidateBedrockViaCLI asks the aws CLI to list Bedrock foundation models.
//
// Here the exit code is the verdict: the CLI resolves the chain, signs, and
// calls the same control-plane endpoint the signed probe would. A non-zero
// exit is reported as Unknown rather than a rejection, because the CLI
// conflates "credentials refused" with "no CLI config", "wrong profile", and
// "network down", and this layer must never manufacture a lockout.
func (v *Validator) ValidateBedrockViaCLI(ctx context.Context, region string) Result {
	return v.validateBedrockViaCLI(ctx, region, commandInvocation{})
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

func commandDiagnosticError(err error, stderr []byte) error {
	detail := strings.TrimSpace(string(stderr))
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}

func lastNonEmptyLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if trimmed := strings.TrimSpace(lines[index]); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

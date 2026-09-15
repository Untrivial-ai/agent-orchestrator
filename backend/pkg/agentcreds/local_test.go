package agentcreds

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Phase E: chain-sourced credentials are delegated to the provider CLI, which
// has already resolved them. Nothing here may ever produce a rejection.
func TestChainDelegationNeverProducesARejection(t *testing.T) {
	tests := []struct {
		name   string
		runner providerCommandRunner
	}{
		{
			name: "cli is not installed",
			runner: func(context.Context, commandInvocation, string, ...string) (commandOutput, error) {
				return commandOutput{}, errors.New("executable file not found in $PATH")
			},
		},
		{
			name: "cli fails",
			runner: func(context.Context, commandInvocation, string, ...string) (commandOutput, error) {
				return commandOutput{Stderr: []byte("Unable to locate credentials")}, errors.New("exit status 255")
			},
		},
		{
			name: "cli times out",
			runner: func(context.Context, commandInvocation, string, ...string) (commandOutput, error) {
				return commandOutput{}, context.DeadlineExceeded
			},
		},
		{
			name: "cli returns nothing useful",
			runner: func(context.Context, commandInvocation, string, ...string) (commandOutput, error) {
				return commandOutput{Stdout: []byte("  ")}, nil
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			validator := newWithCommandRunner(nil, tc.runner)
			for _, result := range []Result{
				validator.ValidateBedrockViaCLI(context.Background(), "us-east-1"),
				validator.ValidateVertexViaCLI(context.Background(), "proj", "us-east5"),
			} {
				if result.State == StateInvalid {
					t.Fatalf("CLI delegation produced a rejection (%s); it may only ever report unknown", result.Detail)
				}
			}
		})
	}
}

// The happy path: aws resolves the chain and lists Claude models.
func TestBedrockViaCLISucceedsWhenTheChainResolves(t *testing.T) {
	validator := newWithCommandRunner(nil, func(_ context.Context, _ commandInvocation, name string, args ...string) (commandOutput, error) {
		if name != "aws" {
			t.Fatalf("ran %q, want aws", name)
		}
		joined := ""
		for _, arg := range args {
			joined += arg + " "
		}
		if !contains(joined, "list-foundation-models") {
			t.Fatalf("args = %v, want a control-plane listing", args)
		}
		if contains(joined, "invoke-model") || contains(joined, "bedrock-runtime") {
			t.Fatalf("args = %v, must never call a billable endpoint", args)
		}
		return commandOutput{Stdout: []byte(`{"modelSummaries":[{"modelId":"anthropic.claude-opus-4-5-v1:0","providerName":"Anthropic"}]}`)}, nil
	})
	result := validator.ValidateBedrockViaCLI(context.Background(), "us-east-1")
	if result.State != StateUnknown {
		t.Fatalf("state = %q (%s), want catalog-only unknown", result.State, result.Detail)
	}
	if len(result.Models) != 1 {
		t.Fatalf("models = %+v", result.Models)
	}
}

// An account that authenticates but has no Claude entitlement is Unknown, not
// valid — the same rule the direct probe applies.
func TestBedrockViaCLIWithoutClaudeAccessIsUnknown(t *testing.T) {
	validator := newWithCommandRunner(nil, func(context.Context, commandInvocation, string, ...string) (commandOutput, error) {
		return commandOutput{Stdout: []byte(`{"modelSummaries":[]}`)}, nil
	})
	result := validator.ValidateBedrockViaCLI(context.Background(), "us-east-1")
	if result.State != StateUnknown {
		t.Fatalf("state = %q, want unknown", result.State)
	}
}

// gcloud hands back an access token, which is then probed like any other.
func TestVertexViaCLIProbesWithTheResolvedToken(t *testing.T) {
	var probeAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"publisherModels":[{"name":"publishers/anthropic/models/claude-x"}]}`))
	}))
	defer server.Close()

	validator := newWithCommandRunner(server.Client(),
		func(_ context.Context, _ commandInvocation, name string, args ...string) (commandOutput, error) {
			if name != "gcloud" {
				t.Fatalf("ran %q, want gcloud", name)
			}
			return commandOutput{Stdout: []byte("ya29.chain-resolved-token\n")}, nil
		},
	)
	result := validator.validateVertexViaCLI(context.Background(), "proj", "us-east5", server.URL, commandInvocation{})
	if result.State != StateValid {
		t.Fatalf("state = %q (%s)", result.State, result.Detail)
	}
	if probeAuth != "Bearer ya29.chain-resolved-token" {
		t.Fatalf("probe Authorization = %q", probeAuth)
	}
}

func TestVertexViaCLIUsesStdoutAndProjectCommandContext(t *testing.T) {
	var probeAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"publisherModels":[{"name":"publishers/anthropic/models/claude-x"}]}`))
	}))
	defer server.Close()

	validator := newWithCommandRunner(server.Client(), func(_ context.Context, invocation commandInvocation, name string, _ ...string) (commandOutput, error) {
		if name != "gcloud" || invocation.WorkingDir != "/project" || invocation.Env["CLOUDSDK_CONFIG"] != "/project/gcloud" {
			t.Fatalf("command = %q invocation = %#v", name, invocation)
		}
		return commandOutput{Stdout: []byte("ya29.stdout-token\n"), Stderr: []byte("warning: quota project missing\n")}, nil
	})
	result := validator.validateVertexViaCLI(context.Background(), "proj", "us-east5", server.URL, commandInvocation{
		WorkingDir: "/project", Env: map[string]string{"CLOUDSDK_CONFIG": "/project/gcloud"},
	})
	if result.State != StateValid || probeAuth != "Bearer ya29.stdout-token" {
		t.Fatalf("state/auth = %q/%q", result.State, probeAuth)
	}
}

// End to end: an unreadable Bedrock credential falls through to the CLI rather
// than reporting that the user has no credentials.
func TestValidateLocalFallsBackToTheCLIForChainCredentials(t *testing.T) {
	called := false
	validator := newWithCommandRunner(nil, func(_ context.Context, invocation commandInvocation, name string, _ ...string) (commandOutput, error) {
		called = true
		if name != "aws" || invocation.WorkingDir != "/project" || invocation.Env["AWS_PROFILE"] != "sso" {
			t.Fatalf("command = %q invocation = %#v", name, invocation)
		}
		return commandOutput{Stdout: []byte(`{"modelSummaries":[{"modelId":"anthropic.claude-x","providerName":"Anthropic"}]}`)}, nil
	})
	projectEnv := map[string]string{"AWS_PROFILE": "sso", "AWS_REGION": "us-east-1"}
	result := validator.ValidateLocal(context.Background(), "bedrock", ResolveOptions{
		// An SSO profile: real credentials, none of them readable here.
		Env: envFrom(projectEnv), WorkingDir: "/project", CommandEnv: projectEnv,
	})
	if !called {
		t.Fatal("chain-sourced Bedrock credentials must be delegated to the aws CLI")
	}
	if result.State != StateUnknown || len(result.Models) != 1 {
		t.Fatalf("state/models = %q/%v, want catalog-only unknown with one model", result.State, result.Models)
	}
}

// The provider gate, end to end: an unrecognized provider probes nothing.
func TestValidateLocalStaysSilentForAnUnknownProvider(t *testing.T) {
	validator := newWithCommandRunner(nil, func(context.Context, commandInvocation, string, ...string) (commandOutput, error) {
		t.Fatal("an unknown provider must not run anything")
		return commandOutput{}, nil
	})
	result := validator.ValidateLocal(context.Background(), "some-future-provider", ResolveOptions{Env: envFrom(nil)})
	if result.State != StateUnknown {
		t.Fatalf("state = %q, want unknown", result.State)
	}
}

// I2, end to end: a malformed probe is our bug and must be downgraded to
// unknown rather than reported as the user's credential being rejected.
func TestValidateLocalDowngradesResolverBugsToUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"x-api-key header is required"}}`))
	}))
	defer server.Close()

	validator := New(server.Client())
	result := validator.ValidateLocal(context.Background(), "gateway", ResolveOptions{
		Env: envFrom(map[string]string{
			"ANTHROPIC_API_KEY": "present-but-somehow-not-sent", "ANTHROPIC_BASE_URL": server.URL,
		}),
	})
	if result.State == StateInvalid {
		t.Fatal("a malformed probe must never be reported as a rejected credential")
	}
	if result.State != StateUnknown {
		t.Fatalf("state = %q, want unknown", result.State)
	}
}

// A genuine rejection must survive that downgrade.
func TestValidateLocalKeepsGenuineRejections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"API key is invalid."}}`))
	}))
	defer server.Close()

	validator := New(server.Client())
	result := validator.ValidateLocal(context.Background(), "gateway", ResolveOptions{
		Env: envFrom(map[string]string{
			"ANTHROPIC_API_KEY": "sk-ant-revoked", "ANTHROPIC_BASE_URL": server.URL,
		}),
	})
	if result.State != StateInvalid {
		t.Fatalf("state = %q (%s), want invalid", result.State, result.Detail)
	}
	if result.Source != "ANTHROPIC_API_KEY" {
		t.Fatalf("source = %q, want the env var that supplied the credential", result.Source)
	}
}

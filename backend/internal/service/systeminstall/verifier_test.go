package systeminstall

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type verifierAgent struct {
	ports.Agent
	path       string
	resolveErr error
	authCalls  *atomic.Int32
}

func (a verifierAgent) ResolveBinary(context.Context) (string, error) { return a.path, a.resolveErr }
func (a verifierAgent) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	a.authCalls.Add(1)
	return ports.AgentAuthStatusAuthorized, nil
}

type verifierResolver map[domain.AgentHarness]ports.Agent

func (r verifierResolver) Agent(harness domain.AgentHarness) (ports.Agent, bool) {
	agent, ok := r[harness]
	return agent, ok
}

type recordingCommandRunner struct {
	argv []string
	run  func(context.Context, []string, io.Writer, io.Writer) error
}

func (r *recordingCommandRunner) Run(ctx context.Context, argv []string, stdout, stderr io.Writer) error {
	r.argv = append([]string(nil), argv...)
	if r.run != nil {
		return r.run(ctx, argv, stdout, stderr)
	}
	_, _ = io.WriteString(stdout, "codex-cli 1.2.3\n")
	return nil
}

func TestVerifierUsesAdapterResolvedBinaryWithoutAuthProbe(t *testing.T) {
	t.Parallel()
	var authCalls atomic.Int32
	runner := &recordingCommandRunner{}
	verifier := NewVerifier(verifierResolver{
		domain.HarnessCodex: verifierAgent{path: "/custom/bin/codex", authCalls: &authCalls},
	}, runner)

	result, err := verifier.Verify(context.Background(), TargetCodex)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if result.ResolvedPath != "/custom/bin/codex" || result.Output != "codex-cli 1.2.3\n" {
		t.Fatalf("result = %+v", result)
	}
	if !reflect.DeepEqual(runner.argv, []string{"/custom/bin/codex", "--version"}) {
		t.Fatalf("argv = %v, want exact resolved binary version probe", runner.argv)
	}
	if authCalls.Load() != 0 {
		t.Fatalf("auth probe calls = %d, want 0", authCalls.Load())
	}
}

func TestVerifierRejectsMissingAdapterOrBinaryResolver(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		resolver verifierResolver
	}{
		{name: "adapter missing", resolver: verifierResolver{}},
		{name: "binary capability missing", resolver: verifierResolver{domain.HarnessCodex: struct{ ports.Agent }{}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewVerifier(tt.resolver, &recordingCommandRunner{}).Verify(context.Background(), TargetCodex)
			if err == nil {
				t.Fatal("Verify error = nil")
			}
		})
	}
}

func TestVerifierBoundsVersionProbe(t *testing.T) {
	t.Parallel()
	runner := &recordingCommandRunner{run: func(ctx context.Context, _ []string, _, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	verifier := NewVerifier(verifierResolver{
		domain.HarnessCodex: verifierAgent{path: "/custom/bin/codex", authCalls: &atomic.Int32{}},
	}, runner)
	verifier.timeout = 20 * time.Millisecond

	_, err := verifier.Verify(context.Background(), TargetCodex)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Verify error = %v, want deadline exceeded", err)
	}
}

package review

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestReviewerTerminalRunsOnItsWorkersAccount(t *testing.T) {
	for _, harness := range []domain.ReviewerHarness{domain.ReviewerCodex, domain.ReviewerClaudeCode} {
		command := []string{"codex", "--model", "gpt-test"}
		accountEnv := map[string]string{"AO_PROXY_ENDPOINT": "http://127.0.0.1:4321", "AO_PROXY_TICKET": "private-session-ticket"}
		if harness == domain.ReviewerClaudeCode {
			command = []string{"claude", "--model", "claude-test"}
			accountEnv = map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:4321", "ANTHROPIC_AUTH_TOKEN": "private-session-ticket", "ANTHROPIC_API_KEY": ""}
		}
		adapterEnv := map[string]string{"KEEP": "adapter-value", "ANTHROPIC_API_KEY": "ambient-key"}
		for _, related := range []bool{true, false} {
			runtime := &fakeRuntime{}
			reviewer := &fakeReviewerWithLaunchSpec{spec: ports.ReviewCommandSpec{Argv: command, Env: adapterEnv}}
			launcher := NewLauncher(fakeReviewerResolver{reviewer: reviewer, ok: true}, runtime, t.TempDir(), WithRelatedAccountEnv(func(_ context.Context, id domain.SessionID, h domain.AgentHarness, env map[string]string) error {
				if id != "mer-1" || h != domain.AgentHarness(harness) {
					t.Errorf("owner=%s harness=%s", id, h)
				}
				if related {
					maps.Copy(env, accountEnv)
				}
				return nil
			}))
			spec := launchSpec()
			spec.Harness = harness
			if _, err := launcher.Spawn(context.Background(), spec); err != nil || !runtime.created {
				t.Fatalf("%s: created=%v err=%v", harness, runtime.created, err)
			}
			env, argv := runtime.createCfg.Env, strings.Join(runtime.createCfg.Argv, " ")
			if env["KEEP"] != "adapter-value" || adapterEnv["ANTHROPIC_API_KEY"] != "ambient-key" || strings.Contains(argv, "private-session-ticket") {
				t.Fatalf("%s: env=%v argv=%s", harness, env, argv)
			}
			for key, value := range accountEnv {
				if related != (env[key] == value) {
					t.Errorf("%s related=%v: %s=%q", harness, related, key, env[key])
				}
			}
			// Only a routed Codex reviewer is pointed at the account helper.
			if strings.Contains(argv, `model_provider="ao-managed"`) != (related && harness == domain.ReviewerCodex) {
				t.Errorf("%s related=%v: argv=%s", harness, related, argv)
			}
		}
	}
}

func TestReviewerAccountFailureLeavesAnExistingTerminalAlone(t *testing.T) {
	for _, failure := range []error{ports.ErrProviderLoginRequired, errors.New("account database unavailable")} {
		runtime := &fakeRuntime{alive: true}
		reviewer := &fakeReviewerWithLaunchSpec{spec: ports.ReviewCommandSpec{Argv: []string{"codex"}}}
		launcher := NewLauncher(fakeReviewerResolver{reviewer: reviewer, ok: true}, runtime, t.TempDir(), WithRelatedAccountEnv(func(context.Context, domain.SessionID, domain.AgentHarness, map[string]string) error {
			return failure
		}))
		spec := launchSpec()
		spec.Harness = domain.ReviewerCodex
		if _, err := launcher.Spawn(context.Background(), spec); !errors.Is(err, failure) {
			t.Fatalf("spawn error=%v", err)
		}
		if runtime.created || runtime.destroyed != "" || runtime.interrupts != 0 || runtime.sentMsg != "" || runtime.sentInput != "" {
			t.Fatalf("failure changed the existing terminal: %+v", runtime)
		}
	}
}

type recordingReviewChatStart struct {
	ReviewerChatController
	started []ReviewerChatStart
}

func (c *recordingReviewChatStart) SupportsReviewChat(domain.AgentHarness) bool { return true }

func (c *recordingReviewChatStart) StartReviewChat(_ context.Context, start ReviewerChatStart) (string, error) {
	c.started = append(c.started, start)
	return "provider-conversation", nil
}

func TestReviewerChatRunsOnItsWorkersAccount(t *testing.T) {
	failure := errors.New("account database unavailable")
	for _, tc := range []struct {
		env    map[string]string
		err    error
		ticket string
	}{
		{map[string]string{"AO_PROXY_TICKET": "private-session-ticket"}, nil, "private-session-ticket"},
		{nil, nil, ""},
		{nil, failure, ""},
	} {
		chat := &recordingReviewChatStart{}
		launcher := NewLauncher(singleReviewerResolver{reviewer: chatReviewAdapter{}}, &fakeRuntime{}, t.TempDir(), WithReviewerChat(chat), WithRelatedAccountEnv(func(_ context.Context, id domain.SessionID, h domain.AgentHarness, env map[string]string) error {
			if id != "mer-1" || h != domain.HarnessCodex {
				t.Errorf("owner=%s harness=%s", id, h)
			}
			maps.Copy(env, tc.env)
			return tc.err
		}))
		spec := launchSpec()
		spec.Harness = domain.ReviewerCodex
		_, err := launcher.Spawn(context.Background(), spec)
		if !errors.Is(err, tc.err) || (tc.err == nil) != (len(chat.started) == 1) {
			t.Fatalf("err=%v started=%d", err, len(chat.started))
		}
		if tc.err == nil && chat.started[0].Env["AO_PROXY_TICKET"] != tc.ticket {
			t.Fatalf("chat env=%v", chat.started[0].Env)
		}
	}
}

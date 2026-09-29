package systeminstall

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestFXInstallerOSMatrix(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows", "freebsd"} {
		t.Run(goos, func(t *testing.T) {
			s := newTestService(goos, "bash")
			plan := s.planAgent(Target("fx"))
			if plan.DocsURL != "https://fx.sh/docs" {
				t.Fatalf("documentation URL = %q", plan.DocsURL)
			}
			if goos != "darwin" && goos != "linux" {
				if !plan.Unsupported || plan.Method != "manual" || plan.Script != nil || len(plan.Command) != 0 {
					t.Fatalf("unsupported OS plan = %+v", plan)
				}
				if goos == "windows" && !strings.Contains(plan.Reason, "WSL") {
					t.Fatalf("Windows plan lacks WSL guidance: %+v", plan)
				}
				return
			}
			if plan.Unsupported || plan.Script == nil || plan.Method != "official-installer" {
				t.Fatalf("fx installer plan = %+v", plan)
			}
			if plan.Script.URL != "https://fx.sh/setup.sh" || len(plan.Script.Interpreter) != 1 || plan.Script.Interpreter[0] != "/usr/bin/bash" || len(plan.Command) != 0 {
				t.Fatalf("fx installer is not the fixed server-owned bash script: %+v", plan)
			}
			if plan.ExpectedDestination != "~/.local/bin/fx" {
				t.Fatalf("destination = %q", plan.ExpectedDestination)
			}
		})
	}
}

func TestFXInstallerPreviewAndValidation(t *testing.T) {
	s := newTestService("linux", "bash")
	if !Valid(Target("fx")) || !IsAgentTarget(Target("fx")) || IsSystemTarget(Target("fx")) {
		t.Fatal("fx must be accepted only by the agent installer allowlist")
	}
	plans, err := s.AgentPlans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, plan := range plans {
		if plan.AgentID != "fx" {
			continue
		}
		found = true
		if !plan.Available || !plan.Automatic || plan.Command != "/usr/bin/bash <downloaded from https://fx.sh/setup.sh>" || plan.ExpectedDestination != "~/.local/bin/fx" || len(plan.Methods) != 1 {
			t.Fatalf("fx preview = %+v", plan)
		}
		if plan.Methods[0].ReinstallAvailable {
			t.Fatal("unverified vendor reinstall must remain unavailable")
		}
	}
	if !found {
		t.Fatal("fx missing from installer previews")
	}
	for _, method := range []string{"https://evil.test/install.sh", "bash -c echo", "npm"} {
		if _, err := s.StartAgent(context.Background(), Target("fx"), method); !errors.Is(err, ErrInstallMethod) {
			t.Fatalf("client-supplied method %q error = %v", method, err)
		}
	}
	if plan := newTestService("linux").planAgent(Target("fx")); !plan.Unsupported || !strings.Contains(plan.Reason, "bash") {
		t.Fatalf("missing bash plan = %+v", plan)
	}
}

func TestFXInstallAndReinstallBlockedByActiveSession(t *testing.T) {
	for _, operation := range []AgentOperation{AgentOperationInstall, AgentOperationReinstall} {
		s := newTestService("linux", "bash")
		s.sessions = sessionListerStub{sessions: []domain.SessionRecord{{ID: "fx-1", Harness: domain.HarnessFX}}}
		if _, err := s.StartAgentOperation(context.Background(), Target("fx"), "official-installer", operation); !errors.Is(err, ErrHarnessActive) {
			t.Fatalf("%s while fx session active = %v, want ErrHarnessActive", operation, err)
		}
	}
}

func TestFXInstallBlockedByStartingSession(t *testing.T) {
	s := newTestService("linux", "bash")
	release, ok := s.TryBeginHarnessUse(domain.HarnessFX)
	if !ok {
		t.Fatal("unexpected harness-use rejection")
	}
	defer release()
	if _, err := s.StartAgent(context.Background(), Target("fx"), "official-installer"); !errors.Is(err, ErrHarnessActive) {
		t.Fatalf("install while fx is starting = %v, want ErrHarnessActive", err)
	}
}

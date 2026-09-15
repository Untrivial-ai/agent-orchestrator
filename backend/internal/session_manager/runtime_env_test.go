package sessionmanager

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type generationEnvAgent struct{ fakeAgent }

func (generationEnvAgent) AugmentRuntimeLaunchEnv(env map[string]string, dataDir string, id domain.SessionID, launchID string) {
	env["augmentation"] = dataDir + ":" + string(id) + ":" + launchID + ":" + env[EnvRuntimeLaunchID]
}

type switchGenerationEnvAgent struct{ *switchTestAgent }

func (switchGenerationEnvAgent) AugmentRuntimeLaunchEnv(env map[string]string, dataDir string, id domain.SessionID, launchID string) {
	generationEnvAgent{}.AugmentRuntimeLaunchEnv(env, dataDir, id, launchID)
}

func TestRuntimeLaunchEnvironmentAcrossSpawnRestoreAndSwitch(t *testing.T) {
	for _, operation := range []string{"spawn", "restore"} {
		t.Run(operation, func(t *testing.T) {
			store := newFakeStore()
			store.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: testRoleAgents()}
			runtime := &fakeRuntime{}
			dataDir := t.TempDir()
			m := New(Deps{Runtime: runtime, Agents: singleAgent{agent: generationEnvAgent{}}, Workspace: &fakeWorkspace{}, Store: store, Messenger: &fakeMessenger{}, Lifecycle: &fakeLCM{store: store}, DataDir: dataDir,
				LookPath: func(string) (string, error) { return "/bin/true", nil }, NewLaunchID: func() string { return "launch-new" }, Executable: func() (string, error) { return "/opt/ao", nil }})
			var id domain.SessionID
			if operation == "spawn" {
				rec, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})
				if err != nil {
					t.Fatal(err)
				}
				id = rec.ID
			} else {
				seedTerminal(store, "mer-1", domain.SessionMetadata{WorkspacePath: "/ws/mer-1", Branch: "branch", AgentSessionID: "native-1", RuntimeLaunchID: "launch-old"})
				result, err := m.RestoreWithMode(ctx, "mer-1")
				if err != nil {
					t.Fatal(err)
				}
				id = result.Session.ID
			}
			if got, want := runtime.lastCfg.Env["augmentation"], dataDir+":"+string(id)+":launch-new:launch-new"; got != want {
				t.Fatalf("%s augmentation=%q, want %q", operation, got, want)
			}
		})
	}
	t.Run("switch", func(t *testing.T) {
		runtime := &fakeRestartRuntime{fakeRuntime: &fakeRuntime{}}
		m, _, _ := newSwitchTestManager(t, runtime)
		agents := m.agents.(switchTestAgents)
		agents[domain.HarnessCodex] = switchGenerationEnvAgent{agents[domain.HarnessCodex].(*switchTestAgent)}
		sw, err := switchAgentSynchronously(ctx, m, "proj-1", SwitchAgentConfig{TargetHarness: domain.HarnessCodex, IdempotencyKey: "env-generation"})
		if err != nil {
			t.Fatal(err)
		}
		launch := string(sw.TargetGenerationID)
		if got, want := runtime.lastCfg.Env["augmentation"], m.dataDir+":proj-1:"+launch+":"+launch; got != want {
			t.Fatalf("switch augmentation=%q, want %q", got, want)
		}
	})
}

func TestRuntimeLaunchEnvironmentAugmentedAfterGeneration(t *testing.T) {
	for _, force := range []bool{false, true} {
		m := New(Deps{DataDir: "/ao-data", Executable: func() (string, error) { return "/ao", nil }})
		env := map[string]string{}
		if _, err := m.wrapAgentProcessWithLaunchID(generationEnvAgent{}, "mer-1", env, []string{"fx"}, "launch-new", force); err != nil {
			t.Fatal(err)
		}
		if env["augmentation"] != "/ao-data:mer-1:launch-new:launch-new" {
			t.Fatalf("force=%v: augmentation = %q; optional capability must run with the assigned generation", force, env["augmentation"])
		}
	}
}

package sessionmanager

import (
	"context"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestRestoreAndFreshFallbackForwardRuntimeEnvironment(t *testing.T) {
	env := map[string]string{"NATIVE_CONFIG_HOME": "/project/native-state", "AO_SESSION_ID": "mer-1"}
	for _, nativeID := range []string{"native-1", ""} {
		t.Run(nativeID, func(t *testing.T) {
			agent := &recordingAgent{}
			meta := domain.SessionMetadata{AgentSessionID: nativeID, Prompt: "resume the task"}
			_, _, _, err := restoreArgv(context.Background(), agent, "mer-1", "/workspace", meta, "system", "", ports.AgentConfig{}, domain.KindWorker, domain.HarnessCodex, "/data", env)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(agent.lastRestore.Env, env) {
				t.Fatalf("restore env = %#v, want %#v", agent.lastRestore.Env, env)
			}
			if nativeID == "" && !reflect.DeepEqual(agent.lastLaunch.Env, env) {
				t.Fatalf("fresh fallback env = %#v, want %#v", agent.lastLaunch.Env, env)
			}
		})
	}
}

package sessionmanager

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/cursor"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestRestoreArgvCursorPreservesResumeWithoutCitationTranscript(t *testing.T) {
	for _, state := range []string{"missing", "empty", "unrecognized layout"} {
		t.Run(state, func(t *testing.T) {
			dataDir := t.TempDir()
			binDir := t.TempDir()
			binary := filepath.Join(binDir, "cursor-agent")
			if runtime.GOOS == "windows" {
				binary += ".exe"
			}
			// Command construction must use the real Cursor adapter without
			// executing an installed provider or consulting its account.
			if err := os.WriteFile(binary, nil, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir)
			const nativeID = "cursor-native-1"
			if state != "missing" {
				citation := filepath.Join(dataDir, "cursor", "projects", "project", "agent-transcripts", nativeID, nativeID+".jsonl")
				if state == "unrecognized layout" {
					citation = filepath.Join(dataDir, "cursor", "different-layout", nativeID+".jsonl")
				}
				if err := os.MkdirAll(filepath.Dir(citation), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(citation, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			argv, delivery, mode, err := restoreArgv(context.Background(), cursor.New(),
				"session-1", t.TempDir(), domain.SessionMetadata{AgentSessionID: nativeID, Prompt: "do not replay this saved prompt"},
				"", "", ports.AgentConfig{Permissions: ports.PermissionModeDefault}, domain.KindWorker,
				domain.HarnessCursor, dataDir, map[string]string{"CURSOR_DATA_DIR": filepath.Join(dataDir, "cursor")})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{binary, "--resume", nativeID}
			if mode != RestoreModeNative || delivery != ports.PromptDeliveryInCommand || !reflect.DeepEqual(argv, want) {
				t.Fatalf("restore = (%v, %s, %s); want native resume %v", argv, delivery, mode, want)
			}
		})
	}
}

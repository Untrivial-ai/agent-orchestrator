package workerexec

import (
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestCodexArgsMatchesCloudSessionPermissionMode(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want []string
	}{
		{
			name: "trusted keeps the TUI yolo policy",
			mode: "trusted",
			want: []string{
				"exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-hook-trust",
				"--dangerously-bypass-approvals-and-sandbox", "--", "describe the change",
			},
		},
		{
			name: "standard stays workspace scoped",
			mode: "standard",
			want: []string{
				"exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-hook-trust",
				"--sandbox", "workspace-write", "--ask-for-approval", "on-request",
				"-c", `approvals_reviewer="auto_review"`, "--", "describe the change",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := codexArgs(worker.Turn{Mode: test.mode, Prompt: "describe the change"})
			if err != nil {
				t.Fatalf("codex args: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("args = %#v, want %#v", got, test.want)
			}
		})
	}
}

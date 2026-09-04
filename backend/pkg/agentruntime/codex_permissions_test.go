package agentruntime

import (
	"reflect"
	"testing"
)

func TestCodexPermissionsLaunchAndRestore(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy PermissionPolicy
		args   []string
	}{
		{"default", PermissionDefault, nil},
		{"unknown", PermissionPolicy("unknown"), nil},
		{"empty", "", nil},
		{"accept edits", PermissionAcceptEdits, []string{"--ask-for-approval", "on-request"}},
		{"auto", PermissionAuto, []string{"--ask-for-approval", "on-request", "-c", `approvals_reviewer="auto_review"`}},
		{"bypass", PermissionBypassPermissions, []string{"--dangerously-bypass-approvals-and-sandbox"}},
		{"durable read-only", PermissionPolicyForMode(SessionModeReadOnly), []string{"--sandbox", "read-only", "--ask-for-approval", "never"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := []string{"-c", "check_for_update_on_startup=false", "-c", "notice.hide_rate_limit_model_nudge=true", "--dangerously-bypass-hook-trust"}
			want := append(append([]string{"codex"}, base...), tc.args...)
			got, err := BuildLaunchCommand(LaunchConfig{Harness: HarnessCodex, Binary: "codex", Permission: tc.policy})
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("launch=%#v, %v; want %#v", got, err, want)
			}
			want = append(append(append([]string{"codex", "resume"}, base...), tc.args...), "native-thread")
			got, ok, err := BuildRestoreCommand(RestoreConfig{Harness: HarnessCodex, Binary: "codex", Permission: tc.policy, Metadata: map[string]string{MetadataKeyAgentSessionID: "native-thread"}})
			if err != nil || !ok || !reflect.DeepEqual(got, want) {
				t.Fatalf("restore=%#v, %v; want %#v", got, err, want)
			}
		})
	}
}

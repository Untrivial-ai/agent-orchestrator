package qodercli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// argvWithoutSettings drops the --settings flag and its value so a test can
// assert the shape of a command without pinning the payload, which has its own
// tests in settings_test.go.
func argvWithoutSettings(t *testing.T, argv []string) []string {
	t.Helper()
	index := slices.Index(argv, "--settings")
	if index < 0 {
		t.Fatalf("command carries no --settings flag: %#v", argv)
	}
	if index+1 >= len(argv) {
		t.Fatalf("--settings has no value: %#v", argv)
	}
	return slices.Delete(slices.Clone(argv), index, index+2)
}

func TestGetLaunchCommandBuildsArgv(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SessionID:       "ao-session-1",
		Permissions:     ports.PermissionModeAcceptEdits,
		AllowedTools:    []string{"Read", "Bash(git diff:*)"},
		DisallowedTools: []string{"Write"},
		SystemPrompt:    "be terse",
		Prompt:          "-fix this",
		WorkspacePath:   "/tmp/does-not-exist-workspace",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"qodercli",
		"--session-id", SessionUUID("ao-session-1"),
		"--permission-mode", "accept_edits",
		// One flag occurrence per rule: Qoder CLI does not take Claude's
		// comma-joined value.
		"--allowed-tools", "Read",
		"--allowed-tools", "Bash(git diff:*)",
		"--disallowed-tools", "Write",
		"--append-system-prompt", "be terse",
		// The task rides on -i so a prompt that looks like a subcommand is not
		// parsed as one.
		"-i", "-fix this",
	}
	if got := argvWithoutSettings(t, cmd); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, got)
	}
}

func TestGetLaunchCommandDefaultPermissionsEmitNoFlag(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(cmd, "--permission-mode") {
		t.Fatalf("default mode should defer to the user's own config: %#v", cmd)
	}
}

func TestGetLaunchCommandUsesSystemPromptFileByPath(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}
	file := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(file, []byte("standing instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SessionID:        "s",
		SystemPromptFile: file,
	})
	if err != nil {
		t.Fatal(err)
	}
	index := slices.Index(cmd, "--append-system-prompt-file")
	if index < 0 || cmd[index+1] != file {
		t.Fatalf("system prompt file should be passed by path: %#v", cmd)
	}
}

func TestGetRestoreCommandResumesWithoutPromptOrSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{ID: "ao-session-1"},
		// A resume-time turn must not reach the command line: --resume resolves
		// asynchronously in the TUI while -i submits from an unrelated effect.
		Prompt:      "next turn",
		Permissions: ports.PermissionModeBypassPermissions,
	})
	if err != nil || !ok {
		t.Fatalf("restore ok=%v err=%v", ok, err)
	}

	want := []string{
		"qodercli",
		"--permission-mode", "bypass_permissions",
		"--resume", SessionUUID("ao-session-1"),
	}
	if got := argvWithoutSettings(t, cmd); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected restore command\nwant: %#v\n got: %#v", want, got)
	}
	if slices.Contains(cmd, "--session-id") {
		t.Fatal("--session-id alongside --resume is rejected by Qoder CLI")
	}
}

func TestGetRestoreCommandPrefersHookCapturedID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}
	captured := "11111111-2222-3333-4444-555555555555"

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			ID:       "ao-session-1",
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: captured},
		},
	})
	if err != nil || !ok {
		t.Fatalf("restore ok=%v err=%v", ok, err)
	}
	index := slices.Index(cmd, "--resume")
	if index < 0 || cmd[index+1] != captured {
		t.Fatalf("restore should resume the hook-captured id: %#v", cmd)
	}
}

func TestGetRestoreCommandRefusesNonUUIDIdentity(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}

	// Qoder CLI reads a numeric --resume argument as "the Nth most recent
	// session", which would attach this pane to an unrelated conversation.
	_, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			ID:       "ao-session-1",
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a non-UUID native id must not produce a resume command")
	}
}

func TestSessionUUIDIsStableAndDistinctPerSession(t *testing.T) {
	if SessionUUID("ao-1") != SessionUUID("ao-1") {
		t.Fatal("session UUID is not stable")
	}
	if SessionUUID("ao-1") == SessionUUID("ao-2") {
		t.Fatal("distinct sessions share a native id")
	}
	if !isUUID(SessionUUID("ao-1")) {
		t.Fatal("derived native id is not a UUID, which Qoder CLI requires")
	}
}

func TestTranscriptBucketMirrorsQoderSanitization(t *testing.T) {
	bucket, ok := transcriptBucket("/Users/foo/my-project")
	if !ok || bucket != "-Users-foo-my-project" {
		t.Fatalf("bucket = %q ok=%v", bucket, ok)
	}
	if _, ok := transcriptBucket(""); ok {
		t.Fatal("an empty workspace has no bucket")
	}
	// Long paths get a hash suffix AO does not reproduce, so they fall back to
	// scanning instead of guessing a name.
	long := "/" + string(make([]byte, maxSanitizedProjectLength))
	if _, ok := transcriptBucket(long); ok {
		t.Fatal("an over-long path must not claim an exact bucket")
	}
}

func TestNativeConversationExistsIsScopedToTheWorkspaceProject(t *testing.T) {
	root := t.TempDir()
	id := SessionUUID("ao-session-1")
	workspace := "/work/repo-a"

	otherBucket := filepath.Join(root, "projects", "-work-repo-b")
	if err := os.MkdirAll(otherBucket, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherBucket, id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	plugin := &Plugin{}
	env := map[string]string{"QODER_CONFIG_DIR": root}
	session := ports.SessionRef{ID: "ao-session-1", WorkspacePath: workspace}

	// The transcript belongs to another checkout: Qoder CLI could not resume it
	// from this workspace, so AO must not report it as resumable.
	exists, err := plugin.NativeConversationExists(context.Background(), session, id, env)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("a transcript from another project must not count as resumable")
	}

	ownBucket := filepath.Join(root, "projects", "-work-repo-a")
	if err := os.MkdirAll(ownBucket, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownBucket, id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exists, err = plugin.NativeConversationExists(context.Background(), session, id, env)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("the workspace's own transcript should be resumable")
	}
}

func TestNativeConversationExistsIgnoresEmptyTranscript(t *testing.T) {
	root := t.TempDir()
	id := SessionUUID("ao-session-1")
	bucket := filepath.Join(root, "projects", "-work-repo-a")
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	// Qoder CLI creates the transcript lazily; a zero-byte file is a reserved
	// id, not a conversation, and resuming it fails.
	if err := os.WriteFile(filepath.Join(bucket, id+".jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	plugin := &Plugin{}
	exists, err := plugin.NativeConversationExists(context.Background(),
		ports.SessionRef{ID: "ao-session-1", WorkspacePath: "/work/repo-a"},
		id, map[string]string{"QODER_CONFIG_DIR": root})
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("an empty transcript must not count as an existing conversation")
	}
}

func TestNativeConversationIDBridgesTerminalAndChat(t *testing.T) {
	plugin := &Plugin{}
	session := ports.SessionRef{ID: "ao-session-1"}

	tuiID, ok, err := plugin.NativeConversationID(context.Background(), session, domain.SessionModeTUI, "")
	if err != nil || !ok || tuiID != SessionUUID("ao-session-1") {
		t.Fatalf("tui id = %q ok=%v err=%v", tuiID, ok, err)
	}

	chatID, ok, err := plugin.NativeConversationID(context.Background(), session, domain.SessionModeChat, "provider-id")
	if err != nil || !ok || chatID != "provider-id" {
		t.Fatalf("chat id = %q ok=%v err=%v", chatID, ok, err)
	}
}

func TestAuthStatusFromOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want ports.AgentAuthStatus
		ok   bool
	}{
		{name: "logged in", out: `{"logged_in":true,"username":"x"}`, want: ports.AgentAuthStatusAuthorized, ok: true},
		{name: "logged out", out: `{"logged_in":false}`, want: ports.AgentAuthStatusUnauthorized, ok: true},
		{name: "noise before json", out: "warning\n{\"logged_in\":true}", want: ports.AgentAuthStatusAuthorized, ok: true},
		{name: "not json", out: "command not found", want: ports.AgentAuthStatusUnknown, ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, ok := authStatusFromOutput([]byte(tc.out))
			if status != tc.want || ok != tc.ok {
				t.Fatalf("status = %q,%v want %q,%v", status, ok, tc.want, tc.ok)
			}
		})
	}
}

package kimchi

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifest(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "kimchi" {
		t.Fatalf("ID = %q, want kimchi", m.ID)
	}
	if m.Name != "Kimchi" {
		t.Fatalf("Name = %q, want Kimchi", m.Name)
	}
	hasAgent := false
	for _, c := range m.Capabilities {
		if c == adapters.CapabilityAgent {
			hasAgent = true
		}
	}
	if !hasAgent {
		t.Fatal("missing CapabilityAgent")
	}
}

func TestGetConfigSpecAdvertisesModelField(t *testing.T) {
	spec, err := (&Plugin{}).GetConfigSpec(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, f := range spec.Fields {
		if f.Key == "model" {
			return
		}
	}
	t.Fatalf("GetConfigSpec does not advertise a model field: %#v", spec.Fields)
}

func TestGetConfigSpecAdvertisesPermissionsField(t *testing.T) {
	spec, err := (&Plugin{}).GetConfigSpec(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, f := range spec.Fields {
		if f.Key == "permissions" {
			if f.Type != ports.ConfigFieldEnum {
				t.Fatalf("permissions field type = %v, want %v", f.Type, ports.ConfigFieldEnum)
			}
			want := []string{
				string(ports.PermissionModeDefault),
				string(ports.PermissionModeAcceptEdits),
				string(ports.PermissionModeAuto),
				string(ports.PermissionModeBypassPermissions),
			}
			if !reflect.DeepEqual(f.Enum, want) {
				t.Fatalf("permissions enum = %#v, want %#v", f.Enum, want)
			}
			return
		}
	}
	t.Fatalf("GetConfigSpec does not advertise a permissions field: %#v", spec.Fields)
}

func TestGetPromptDeliveryStrategy(t *testing.T) {
	s, err := (&Plugin{}).GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if s != ports.PromptDeliveryInCommand {
		t.Fatalf("strategy = %q, want %q", s, ports.PromptDeliveryInCommand)
	}
}

func TestGetLaunchCommandWithPrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt: "add a health check",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"kimchi", "add a health check"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestSanitizePrompt(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		// Reserved leading characters must be neutralized with a leading newline.
		{"dash flag", "--yolo", "\n--yolo"},
		{"at file reference", "@README.md", "\n@README.md"},
		{"single dash", "-x", "\n-x"},
		// Normal prompts pass through unchanged.
		{"normal text", "add a health check", "add a health check"},
		{"filename no at", "README.md", "README.md"},
		{"multi-word", "do the thing", "do the thing"},
		// Empty string passes through (the caller already guards on empty).
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizePrompt(tc.prompt)
			if got != tc.want {
				t.Fatalf("sanitizePrompt(%q) = %q, want %q", tc.prompt, got, tc.want)
			}
		})
	}
}

func TestGetLaunchCommandSanitizesFlagPrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt: "--yolo",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"kimchi", "\n--yolo"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandSanitizesAtPrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt: "@README.md",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"kimchi", "\n@README.md"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandNormalPromptUnchanged(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt: "README.md",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"kimchi", "README.md"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandSanitizesPromptWithSystemPrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPrompt: "follow repo rules",
		Prompt:       "-flag",
	})
	if err != nil {
		t.Fatal(err)
	}

	// System prompt is prepended normally; the prompt is still sanitized.
	want := []string{"kimchi", "--append-system-prompt", "follow repo rules", "\n-flag"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandPermissions(t *testing.T) {
	tests := []struct {
		mode ports.PermissionMode
		want []string
	}{
		{ports.PermissionModeDefault, []string{"kimchi"}},
		{"", []string{"kimchi"}},
		{ports.PermissionModeAcceptEdits, []string{"kimchi", "--auto"}},
		{ports.PermissionModeAuto, []string{"kimchi", "--auto"}},
		{ports.PermissionModeBypassPermissions, []string{"kimchi", "--yolo"}},
	}

	for _, tc := range tests {
		t.Run(string(tc.mode), func(t *testing.T) {
			p := &Plugin{resolvedBinary: "kimchi"}
			cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Permissions: tc.mode})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cmd, tc.want) {
				t.Fatalf("cmd = %#v, want %#v", cmd, tc.want)
			}
		})
	}
}

func TestGetLaunchCommandAppendsSystemPrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPrompt: "follow repo rules",
		Prompt:       "do the thing",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"kimchi", "--append-system-prompt", "follow repo rules", "do the thing"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandInlinesSystemPromptFileContents(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "system.md")
	if err := os.WriteFile(file, []byte("file contents win"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &Plugin{resolvedBinary: "kimchi"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: file,
		SystemPrompt:     "inline ignored",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"kimchi", "--append-system-prompt", "file contents win"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandSystemPromptFileReadError(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}
	_, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: filepath.Join(t.TempDir(), "missing.md"),
		SystemPrompt:     "inline ignored",
	})
	if err == nil {
		t.Fatal("expected error for unreadable system-prompt file, got nil")
	}
}

func TestGetLaunchCommandEmitsToolAllowlist(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}

	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		AllowedTools:    []string{"read", "grep", "bash(git diff:*)"},
		DisallowedTools: []string{"edit", "write", "bash(git push:*)"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Kimchi's permission loader splits --allow-tool/--deny-tool values by
	// comma (splitFlag), and repeated flags overwrite (last-wins), so the
	// adapter must emit a single comma-joined value per flag — not one pair
	// per rule — to avoid the deny list collapsing to a single entry.
	if !containsSubsequence(cmd, []string{"--allow-tool", "read,grep,bash(git diff:*)"}) {
		t.Fatalf("missing single comma-joined --allow-tool; got %#v", cmd)
	}
	if !containsSubsequence(cmd, []string{"--deny-tool", "edit,write,bash(git push:*)"}) {
		t.Fatalf("missing single comma-joined --deny-tool; got %#v", cmd)
	}
	if n := countFlagOccurrences(cmd, "--allow-tool"); n != 1 {
		t.Fatalf("--allow-tool appears %d times, want 1; got %#v", n, cmd)
	}
	if n := countFlagOccurrences(cmd, "--deny-tool"); n != 1 {
		t.Fatalf("--deny-tool appears %d times, want 1; got %#v", n, cmd)
	}
}

func TestAppendToolFlagsRejectsCommaInRule(t *testing.T) {
	t.Run("allowed", func(t *testing.T) {
		var cmd []string
		err := appendToolFlags(&cmd, []string{"bash(git diff:*, --force)"}, nil)
		if err == nil {
			t.Fatal("expected error for comma in allowed rule, got nil")
		}
		if !contains(err.Error(), "bash(git diff:*, --force)") {
			t.Fatalf("error should name the offending rule; got %q", err.Error())
		}
		if len(cmd) != 0 {
			t.Fatalf("cmd should be empty on error; got %#v", cmd)
		}
	})

	t.Run("disallowed", func(t *testing.T) {
		var cmd []string
		err := appendToolFlags(&cmd, nil, []string{"edit,write"})
		if err == nil {
			t.Fatal("expected error for comma in disallowed rule, got nil")
		}
		if !contains(err.Error(), "edit,write") {
			t.Fatalf("error should name the offending rule; got %q", err.Error())
		}
		if len(cmd) != 0 {
			t.Fatalf("cmd should be empty on error; got %#v", cmd)
		}
	})
}

func TestGetLaunchCommandRejectsCommaInAllowedTool(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}
	_, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		AllowedTools: []string{"read,write"},
	})
	if err == nil {
		t.Fatal("expected error for comma in allowed tool rule, got nil")
	}
	if !contains(err.Error(), "read,write") {
		t.Fatalf("error should name the offending rule; got %q", err.Error())
	}
}

func TestGetLaunchCommandOmitsToolFlagsWhenUnset(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}

	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if containsString(cmd, "--allow-tool") || containsString(cmd, "--deny-tool") {
		t.Fatalf("unrestricted launch should emit no tool flags; got %#v", cmd)
	}
}

func containsSubsequence(values, needle []string) bool {
	if len(needle) == 0 {
		return true
	}
	for start := 0; start+len(needle) <= len(values); start++ {
		ok := true
		for i, w := range needle {
			if values[start+i] != w {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// countFlagOccurrences counts how many times flagName appears as an element
// of values, asserting that --allow-tool / --deny-tool are emitted at most
// once each.
func countFlagOccurrences(values []string, flagName string) int {
	n := 0
	for _, v := range values {
		if v == flagName {
			n++
		}
	}
	return n
}

// containsString reports whether values contains the given string. It is the
// slice-membership counterpart to the file's existing substring contains();
// the two share a name in the claudecode test but are distinguished here to
// avoid a collision with the substring helper below.
func containsString(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}

func TestGetRestoreCommand(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "019e950e-52e0-7411-961b-d380ca7e610f"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}

	// Full argv: binary + (no permission flag for default) + (no system prompt)
	// + --session <id> last. Permissions and system prompt are absent here
	// because the RestoreConfig carries neither, so the only trailing flag is
	// --session. The tests below assert the permission and system-prompt
	// re-application paths separately.
	want := []string{"kimchi", "--session", "019e950e-52e0-7411-961b-d380ca7e610f"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

// TestGetRestoreCommandAppendsResumeTimePrompt covers resuming a reviewer
// after it was killed and re-triggered: the new task must be embedded in the
// resume argv (mirroring GetLaunchCommand), or the resumed session would sit
// idle with no work to act on.
func TestGetRestoreCommandAppendsResumeTimePrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Prompt: "review the new commit",
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "019e950e-52e0-7411-961b-d380ca7e610f"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := []string{"kimchi", "--session", "019e950e-52e0-7411-961b-d380ca7e610f", "review the new commit"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandWithPermissions(t *testing.T) {
	tests := []struct {
		mode ports.PermissionMode
		want []string
	}{
		{ports.PermissionModeDefault, []string{"kimchi"}},
		{"", []string{"kimchi"}},
		{ports.PermissionModeAcceptEdits, []string{"kimchi", "--auto"}},
		{ports.PermissionModeAuto, []string{"kimchi", "--auto"}},
		{ports.PermissionModeBypassPermissions, []string{"kimchi", "--yolo"}},
	}

	for _, tc := range tests {
		t.Run(string(tc.mode), func(t *testing.T) {
			p := &Plugin{resolvedBinary: "kimchi"}
			cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
				Session: ports.SessionRef{
					Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "sess-1"},
				},
				Permissions: tc.mode,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatal("ok=false, want true")
			}
			// The permission flag must be re-applied on resume before
			// --session, which is appended last.
			want := append(append([]string{}, tc.want...), "--session", "sess-1")
			if !reflect.DeepEqual(cmd, want) {
				t.Fatalf("cmd = %#v, want %#v", cmd, want)
			}
		})
	}
}

func TestGetRestoreCommandEmitsToolAllowlist(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}

	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "sess-1"},
		},
		AllowedTools:    []string{"read", "grep", "bash(git diff:*)"},
		DisallowedTools: []string{"edit", "write", "bash(git push:*)"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}

	// Kimchi's permission loader splits --allow-tool/--deny-tool values by
	// comma (splitFlag), and repeated flags overwrite (last-wins), so the
	// adapter must emit a single comma-joined value per flag — not one pair
	// per rule — to avoid the deny list collapsing to a single entry.
	if !containsSubsequence(cmd, []string{"--allow-tool", "read,grep,bash(git diff:*)"}) {
		t.Fatalf("missing single comma-joined --allow-tool; got %#v", cmd)
	}
	if !containsSubsequence(cmd, []string{"--deny-tool", "edit,write,bash(git push:*)"}) {
		t.Fatalf("missing single comma-joined --deny-tool; got %#v", cmd)
	}
	if n := countFlagOccurrences(cmd, "--allow-tool"); n != 1 {
		t.Fatalf("--allow-tool appears %d times, want 1; got %#v", n, cmd)
	}
	if n := countFlagOccurrences(cmd, "--deny-tool"); n != 1 {
		t.Fatalf("--deny-tool appears %d times, want 1; got %#v", n, cmd)
	}
	// Tool flags must appear before --session, which is appended last.
	if !containsSubsequence(cmd, []string{"--deny-tool", "edit,write,bash(git push:*)", "--session", "sess-1"}) {
		t.Fatalf("tool flags must precede --session; got %#v", cmd)
	}
}

func TestGetRestoreCommandOmitsToolFlagsWhenUnset(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}

	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "sess-1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	if containsString(cmd, "--allow-tool") || containsString(cmd, "--deny-tool") {
		t.Fatalf("unrestricted restore should emit no tool flags; got %#v", cmd)
	}
}

func TestGetRestoreCommandWithSystemPromptFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "system.md")
	// Write content WITH a trailing newline to verify the raw file contents
	// are passed through without TrimRight trimming.
	if err := os.WriteFile(file, []byte("file contents win\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &Plugin{resolvedBinary: "kimchi"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "019e950e-52e0-7411-961b-d380ca7e610f"},
		},
		SystemPromptFile: file,
		SystemPrompt:     "inline ignored",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}

	// The file's raw contents (including the trailing newline) are inlined
	// as --append-system-prompt before --session (which is appended last).
	// Inline cfg.SystemPrompt is ignored because the file takes precedence,
	// mirroring the launch-side behavior.
	want := []string{"kimchi", "--append-system-prompt", "file contents win\n", "--session", "019e950e-52e0-7411-961b-d380ca7e610f"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestReadBoundedSystemPromptReadsRegularFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "system.md")
	if err := os.WriteFile(file, []byte("hello system prompt"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readBoundedSystemPrompt(context.Background(), file)
	if err != nil {
		t.Fatalf("readBoundedSystemPrompt: %v", err)
	}
	if got != "hello system prompt" {
		t.Fatalf("got %q, want %q", got, "hello system prompt")
	}
}

func TestReadBoundedSystemPromptHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := readBoundedSystemPrompt(ctx, filepath.Join(t.TempDir(), "missing.md"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestGetLaunchCommandRejectsOversizedSystemPromptFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "system.md")
	// 128 KiB + 1 byte — one byte over the limit.
	if err := os.WriteFile(file, bytes.Repeat([]byte("a"), 128*1024+1), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &Plugin{resolvedBinary: "kimchi"}
	_, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: file,
	})
	if err == nil {
		t.Fatal("expected error for oversized system-prompt file, got nil")
	}
}

func TestGetRestoreCommandRejectsOversizedSystemPromptFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "system.md")
	// 128 KiB + 1 byte — one byte over the limit.
	if err := os.WriteFile(file, bytes.Repeat([]byte("a"), 128*1024+1), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &Plugin{resolvedBinary: "kimchi"}
	_, _, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "sess-1"},
		},
		SystemPromptFile: file,
	})
	if err == nil {
		t.Fatal("expected error for oversized system-prompt file, got nil")
	}
}

func TestGetRestoreCommandSystemPromptFileReadError(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}
	_, _, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "sess-1"},
		},
		SystemPromptFile: filepath.Join(t.TempDir(), "missing.md"),
	})
	if err == nil {
		t.Fatal("expected error for unreadable system-prompt file, got nil")
	}
}

func TestGetRestoreCommandNoID(t *testing.T) {
	p := &Plugin{resolvedBinary: "kimchi"}
	_, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{Metadata: map[string]string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("ok=true with no agentSessionId, want false")
	}
}

func TestSessionInfoWithMetadata(t *testing.T) {
	info, ok, err := (&Plugin{}).SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "019e950e-52e0-7411-961b-d380ca7e610f",
			ports.MetadataKeyTitle:          "add health check",
			ports.MetadataKeySummary:        "added /health endpoint",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	if info.AgentSessionID != "019e950e-52e0-7411-961b-d380ca7e610f" {
		t.Fatalf("AgentSessionID = %q", info.AgentSessionID)
	}
	if info.Title != "add health check" {
		t.Fatalf("Title = %q", info.Title)
	}
	if info.Summary != "added /health endpoint" {
		t.Fatalf("Summary = %q", info.Summary)
	}
}

func TestSessionInfoNoMetadata(t *testing.T) {
	_, ok, err := (&Plugin{}).SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("ok=true with no metadata, want false")
	}
}

func TestGetAgentHooksWritesSettingsFile(t *testing.T) {
	workspace := t.TempDir()
	p := &Plugin{}
	if err := p.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
	}); err != nil {
		t.Fatal(err)
	}

	settingsFile := filepath.Join(workspace, ".kimchi", "hooks.local.json")
	data, err := os.ReadFile(settingsFile)
	if err != nil {
		t.Fatalf("settings file not created: %v", err)
	}

	content := string(data)
	for _, cmd := range []string{
		"ao hooks kimchi session-start",
		"ao hooks kimchi user-prompt-submit",
		"ao hooks kimchi stop",
		"ao hooks kimchi notification",
		"ao hooks kimchi session-end",
		"ao hooks kimchi pre-tool-use",
		"ao hooks kimchi post-tool-use",
		"ao hooks kimchi post-tool-use-failure",
	} {
		if !contains(content, cmd) {
			t.Errorf("settings missing hook command %q", cmd)
		}
	}

	gitignore := filepath.Join(workspace, ".kimchi", ".gitignore")
	if _, err := os.Stat(gitignore); err != nil {
		t.Fatalf("gitignore not created: %v", err)
	}
}

func TestGetAgentHooksIdempotent(t *testing.T) {
	workspace := t.TempDir()
	p := &Plugin{}
	cfg := ports.WorkspaceHookConfig{WorkspacePath: workspace}

	if err := p.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	settingsFile := filepath.Join(workspace, ".kimchi", "hooks.local.json")
	data, err := os.ReadFile(settingsFile)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	count := countOccurrences(content, "ao hooks kimchi session-start")
	if count != 1 {
		t.Fatalf("session-start hook duplicated: found %d occurrences", count)
	}
}

func TestUninstallHooks(t *testing.T) {
	workspace := t.TempDir()
	p := &Plugin{}
	cfg := ports.WorkspaceHookConfig{WorkspacePath: workspace}

	if err := p.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.UninstallHooks(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}

	settingsFile := filepath.Join(workspace, ".kimchi", "hooks.local.json")
	data, err := os.ReadFile(settingsFile)
	if err != nil {
		t.Fatal(err)
	}
	if contains(string(data), "ao hooks kimchi") {
		t.Fatal("hooks not removed after uninstall")
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := (&Plugin{}).GetConfigSpec(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetConfigSpec err = %v, want context.Canceled", err)
	}
	if _, err := (&Plugin{}).GetPromptDeliveryStrategy(ctx, ports.LaunchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetPromptDeliveryStrategy err = %v, want context.Canceled", err)
	}
	if err := (&Plugin{}).GetAgentHooks(ctx, ports.WorkspaceHookConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetAgentHooks err = %v, want context.Canceled", err)
	}
	if _, _, err := (&Plugin{}).GetRestoreCommand(ctx, ports.RestoreConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetRestoreCommand err = %v, want context.Canceled", err)
	}
	if _, _, err := (&Plugin{}).SessionInfo(ctx, ports.SessionRef{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("SessionInfo err = %v, want context.Canceled", err)
	}
}

func TestResolveKimchiBinaryContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ResolveKimchiBinary(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveKimchiBinary err = %v, want context.Canceled", err)
	}
}

func TestEmitsSubmitActivity(t *testing.T) {
	p := &Plugin{}
	if !p.EmitsSubmitActivity() {
		t.Fatal("EmitsSubmitActivity() = false, want true")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func countOccurrences(s, substr string) int {
	count := 0
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			count++
		}
	}
	return count
}

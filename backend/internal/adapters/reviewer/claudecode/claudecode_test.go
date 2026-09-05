package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	workeragent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// captureAgent is a stub ports.Agent that records the LaunchConfig the reviewer
// builds, so the test asserts the reviewer's tool policy without needing the
// real claude binary on PATH.
type captureAgent struct {
	got        ports.LaunchConfig
	gotRestore ports.RestoreConfig
	hooks      []ports.WorkspaceHookConfig
	prelaunch  []ports.LaunchConfig
}

type captureHistoryAgent struct {
	captureAgent
	existing map[string]bool
	probed   []string
	err      error
}

func (a *captureHistoryAgent) NativeConversationExists(_ context.Context, _ ports.SessionRef, id string, _ map[string]string) (bool, error) {
	a.probed = append(a.probed, id)
	return a.existing[id], a.err
}

func (a *captureAgent) GetConfigSpec(context.Context) (ports.ConfigSpec, error) {
	return ports.ConfigSpec{}, nil
}
func (a *captureAgent) GetLaunchCommand(_ context.Context, cfg ports.LaunchConfig) ([]string, error) {
	a.got = cfg
	return []string{"claude"}, nil
}
func (a *captureAgent) GetPromptDeliveryStrategy(context.Context, ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	return ports.PromptDeliveryInCommand, nil
}
func (a *captureAgent) GetAgentHooks(_ context.Context, cfg ports.WorkspaceHookConfig) error {
	a.hooks = append(a.hooks, cfg)
	return nil
}
func (a *captureAgent) PreLaunch(_ context.Context, cfg ports.LaunchConfig) error {
	a.prelaunch = append(a.prelaunch, cfg)
	return nil
}
func (a *captureAgent) GetRestoreCommand(_ context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	a.gotRestore = cfg
	id := cfg.Session.Metadata[ports.MetadataKeyAgentSessionID]
	if id == "" {
		id = cfg.Session.ID
	}
	if id == "" {
		return nil, false, nil
	}
	return []string{"claude", "--resume", id}, true, nil
}
func (a *captureAgent) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error) {
	return ports.SessionInfo{}, false, nil
}

func TestReviewCommandLaunchesReadOnlyOffBypass(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	spec, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:    "review-w1",
		WorkspacePath: "/ws/w1",
		Prompt:        "review it",
		SystemPrompt:  "you are a reviewer",
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}

	// The allowlist is what enforces read-only, so it must launch in an
	// explicit non-bypass mode: bypassPermissions ignores allow/deny rules
	// entirely, and an empty mode would defer to a user's defaultMode.
	if agent.got.Permissions != ports.PermissionModeAuto {
		t.Fatalf("reviewer must launch in auto permission mode; got %q", agent.got.Permissions)
	}
	if agent.got.SessionID == "" {
		t.Fatal("reviewer must pass its stable AO session id")
	}
	if spec.AgentSessionID != agent.got.NativeSessionID {
		t.Fatalf("persisted agent session id = %q, requested native session id = %q", spec.AgentSessionID, agent.got.NativeSessionID)
	}
	if agent.got.SessionID != "review-w1" {
		t.Fatalf("AO session id = %q, want review-w1", agent.got.SessionID)
	}
	if !contains(agent.got.AllowedTools, "Read") || !contains(agent.got.AllowedTools, "Bash(ao review submit:*)") {
		t.Fatalf("allowlist missing read-only review tools: %#v", agent.got.AllowedTools)
	}
	for _, denied := range []string{"Edit", "Write", "Bash(git push:*)", "Bash(git commit:*)"} {
		if !contains(agent.got.DisallowedTools, denied) {
			t.Fatalf("disallow list missing %q: %#v", denied, agent.got.DisallowedTools)
		}
	}
}

func TestReviewCommandEmitsPersistedNativeSessionID(t *testing.T) {
	binDir := t.TempDir()
	binaryName := "claude"
	if runtime.GOOS == "windows" {
		binaryName = "claude.exe"
	}
	if err := os.WriteFile(filepath.Join(binDir, binaryName), []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	spec, err := New().ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID: "review-w1",
		Prompt:     "review it",
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}
	emittedID := flagValue(spec.Argv, "--session-id")
	if emittedID == "" {
		t.Fatalf("argv missing --session-id: %#v", spec.Argv)
	}
	if emittedID != spec.AgentSessionID {
		t.Fatalf("emitted session id = %q, persisted session id = %q", emittedID, spec.AgentSessionID)
	}
	if emittedID != workeragent.SessionUUID("review-w1") {
		t.Fatalf("emitted session id = %q, want single-derived reviewer id", emittedID)
	}
	if emittedID == workeragent.SessionUUID(spec.AgentSessionID) {
		t.Fatalf("emitted session id was derived twice: %q", emittedID)
	}
}

func TestPreLaunchInstallsSelectedReviewerHooksAndTrustsWorkspace(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	if err := r.PreLaunch(context.Background(), ports.ReviewInvocation{
		ReviewerID:    "review-w1",
		WorkspacePath: "/ws/w1",
	}); err != nil {
		t.Fatalf("PreLaunch: %v", err)
	}
	if len(agent.hooks) != 1 || agent.hooks[0].WorkspacePath != "/ws/w1" {
		t.Fatalf("hooks = %#v, want selected reviewer hook install for /ws/w1", agent.hooks)
	}
	if len(agent.prelaunch) != 1 || agent.prelaunch[0].WorkspacePath != "/ws/w1" || agent.prelaunch[0].SessionID == "" {
		t.Fatalf("prelaunch = %#v, want trusted workspace with pinned session id", agent.prelaunch)
	}
}

func TestAllowlistCoversPromptRequiredPipedCommands(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	if _, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:    "review-w1",
		WorkspacePath: "/ws/w1",
		Prompt:        "review it",
		SystemPrompt:  "you are a reviewer",
	}); err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}

	if !contains(agent.got.AllowedTools, "Bash(printf:*)") {
		t.Fatalf("allowlist missing printf for piped review commands: %#v", agent.got.AllowedTools)
	}

	for _, cmd := range []string{
		"printf '%s' '{ \"event\": \"COMMENT\", \"body\": \"x\" }' | gh api --method POST repos/o/r/pulls/1/reviews --input - --jq '.id'",
		"printf '%s' '{ \"reviews\": [] }' | ao review submit --session sess-1 --reviews -",
	} {
		if !compoundCommandCovered(agent.got.AllowedTools, cmd) {
			t.Fatalf("allowlist does not cover prompt-required command %q with tools %#v", cmd, agent.got.AllowedTools)
		}
	}

	disallowed := "printf x | rm -rf /"
	if compoundCommandCovered(agent.got.AllowedTools, disallowed) {
		t.Fatalf("allowlist unexpectedly covers disallowed command %q with tools %#v", disallowed, agent.got.AllowedTools)
	}
}

func TestReviewCommandUsesHiddenSystemPromptFile(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	if _, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		Prompt:           "Start the AO review task.",
		SystemPromptFile: "/ao/prompts/reviewer/system.md",
	}); err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}
	if agent.got.Prompt != "Start the AO review task." || agent.got.SystemPrompt != "" || agent.got.SystemPromptFile != "/ao/prompts/reviewer/system.md" {
		t.Fatalf("launch config = %+v", agent.got)
	}
}

func TestReviewRestoreCommandUsesNativeSessionIDAndReadOnlyPolicy(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	got, ok, err := r.ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:       "review-w1",
		RunID:            "run-2",
		AgentSessionID:   "claude-native-1",
		WorkspacePath:    "/ws/w1",
		Prompt:           "read the new review task",
		SystemPromptFile: "/ao/prompts/reviewer/system.md",
	})
	if err != nil {
		t.Fatalf("ReviewRestoreCommand: %v", err)
	}
	if !ok {
		t.Fatal("ReviewRestoreCommand ok = false, want true")
	}
	if !got.NativeResumed {
		t.Fatal("ReviewRestoreCommand did not report native resume")
	}
	if strings.Join(got.Argv, " ") != "claude --resume claude-native-1" {
		t.Fatalf("argv = %#v", got.Argv)
	}
	if agent.gotRestore.Session.Metadata[ports.MetadataKeyAgentSessionID] != "claude-native-1" {
		t.Fatalf("restore metadata = %#v", agent.gotRestore.Session.Metadata)
	}
	if agent.gotRestore.Permissions != ports.PermissionModeAuto {
		t.Fatalf("restore permissions = %q, want auto", agent.gotRestore.Permissions)
	}
	if agent.gotRestore.Prompt != "read the new review task" || agent.gotRestore.SystemPromptFile != "/ao/prompts/reviewer/system.md" {
		t.Fatalf("restore prompt configuration = %+v", agent.gotRestore)
	}
	if !contains(agent.gotRestore.AllowedTools, "Read") || !contains(agent.gotRestore.DisallowedTools, "Write") {
		t.Fatalf("restore tool policy allowed=%#v disallowed=%#v", agent.gotRestore.AllowedTools, agent.gotRestore.DisallowedTools)
	}
}

func TestReviewRestoreCommandDetectsExistingTranscript(t *testing.T) {
	persistedID := workeragent.SessionUUID("review-w1")
	agent := &captureHistoryAgent{existing: map[string]bool{persistedID: true}}
	r := &Reviewer{agent: agent}

	got, ok, err := r.ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:     "review-w1",
		RunID:          "run-2",
		AgentSessionID: persistedID,
		Prompt:         "review the new commit",
	})
	if err != nil || !ok {
		t.Fatalf("ReviewRestoreCommand = (ok=%v, err=%v), want existing transcript resume", ok, err)
	}
	if len(agent.probed) != 1 || agent.probed[0] != persistedID {
		t.Fatalf("probed ids = %#v, want %q", agent.probed, persistedID)
	}
	if got.AgentSessionID != persistedID || agent.gotRestore.Prompt != "review the new commit" {
		t.Fatalf("restore result = %+v config=%+v", got, agent.gotRestore)
	}
}

func TestReviewRestoreCommandEmitsResumeWithPolicySystemPromptAndTask(t *testing.T) {
	home := t.TempDir()
	binDir := t.TempDir()
	binaryName := "claude"
	if runtime.GOOS == "windows" {
		binaryName = "claude.exe"
	}
	binary := filepath.Join(binDir, binaryName)
	if err := os.WriteFile(binary, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PATH", binDir)

	persistedID := workeragent.SessionUUID("review-w1")
	transcript := filepath.Join(home, ".claude", "projects", "workspace", persistedID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	systemPromptFile := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(systemPromptFile, []byte("review read-only"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, ok, err := New().ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:       "review-w1",
		RunID:            "run-2",
		AgentSessionID:   persistedID,
		Prompt:           "read the new task file",
		SystemPromptFile: systemPromptFile,
	})
	if err != nil || !ok {
		t.Fatalf("ReviewRestoreCommand = (ok=%v, err=%v), want command", ok, err)
	}
	for _, want := range [][]string{
		{"--permission-mode", "auto"},
		{"--allowedTools", strings.Join(reviewerAllowedTools, ",")},
		{"--disallowedTools", strings.Join(reviewerDisallowedTools, ",")},
		{"--append-system-prompt-file", systemPromptFile},
		{"--resume", persistedID},
		{"--", "read the new task file"},
	} {
		if !containsSubsequence(got.Argv, want) {
			t.Fatalf("argv %#v missing %#v", got.Argv, want)
		}
	}
	if contains(got.Argv, "--session-id") {
		t.Fatalf("resume argv unexpectedly starts a fresh session: %#v", got.Argv)
	}
}

func TestReviewRestoreCommandPropagatesTranscriptProbeError(t *testing.T) {
	persistedID := workeragent.SessionUUID("review-w1")
	agent := &captureHistoryAgent{existing: map[string]bool{}, err: os.ErrPermission}
	r := &Reviewer{agent: agent}

	got, ok, err := r.ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:     "review-w1",
		AgentSessionID: persistedID,
	})
	if err == nil || !os.IsPermission(err) || ok || len(got.Argv) != 0 {
		t.Fatalf("ReviewRestoreCommand = (%+v, %v, %v), want probe error", got, ok, err)
	}
	if agent.gotRestore.Session.ID != "" {
		t.Fatalf("probe error was masked by restore command: %+v", agent.gotRestore)
	}
}

func TestReviewRestoreCommandAllowsAdapterFallbackWithoutNativeSessionID(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	got, ok, err := r.ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:       "review-w1",
		WorkspacePath:    "/ws/w1",
		SystemPromptFile: "/ao/prompts/reviewer/system.md",
	})
	if err != nil {
		t.Fatalf("ReviewRestoreCommand: %v", err)
	}
	if !ok {
		t.Fatal("ReviewRestoreCommand ok = false, want true")
	}
	if strings.Join(got.Argv, " ") != "claude --resume review-w1" {
		t.Fatalf("argv = %#v", got.Argv)
	}
	if agent.gotRestore.Session.ID != "review-w1" {
		t.Fatalf("restore session id = %q, want review-w1", agent.gotRestore.Session.ID)
	}
	if _, ok := agent.gotRestore.Session.Metadata[ports.MetadataKeyAgentSessionID]; ok {
		t.Fatalf("restore metadata should not invent native id: %#v", agent.gotRestore.Session.Metadata)
	}
}

func TestReviewRestoreCommandMigratesLegacyDoubleHashedSession(t *testing.T) {
	persistedID := workeragent.SessionUUID("review-w1")
	legacyID := workeragent.SessionUUID(persistedID)
	agent := &captureHistoryAgent{existing: map[string]bool{legacyID: true}}
	r := &Reviewer{agent: agent}

	got, ok, err := r.ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:     "review-w1",
		AgentSessionID: persistedID,
	})
	if err != nil || !ok {
		t.Fatalf("ReviewRestoreCommand = (ok=%v, err=%v), want legacy restore", ok, err)
	}
	if strings.Join(got.Argv, " ") != "claude --resume "+legacyID {
		t.Fatalf("argv = %#v, want legacy session %q", got.Argv, legacyID)
	}
	if got.AgentSessionID != legacyID {
		t.Fatalf("migrated agent session id = %q, want %q", got.AgentSessionID, legacyID)
	}
	if strings.Join(agent.probed, ",") != persistedID+","+legacyID {
		t.Fatalf("probed ids = %#v", agent.probed)
	}
}

func TestReviewRestoreCommandFallsBackWhenPersistedConversationIsMissing(t *testing.T) {
	persistedID := workeragent.SessionUUID("review-w1")
	agent := &captureHistoryAgent{existing: map[string]bool{}}
	r := &Reviewer{agent: agent}

	got, ok, err := r.ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:     "review-w1",
		AgentSessionID: persistedID,
	})
	if err != nil {
		t.Fatalf("ReviewRestoreCommand: %v", err)
	}
	if ok || len(got.Argv) != 0 {
		t.Fatalf("ReviewRestoreCommand = (%#v, %v), want fresh-launch fallback", got, ok)
	}
}

func TestReviewCancelSendsDoubleEscapeInput(t *testing.T) {
	spec, err := (&Reviewer{}).ReviewCancel(context.Background())
	if err != nil {
		t.Fatalf("ReviewCancel: %v", err)
	}
	if spec.Mode != ports.ReviewCancelInput {
		t.Fatalf("cancel mode = %q, want %q", spec.Mode, ports.ReviewCancelInput)
	}
	if len(spec.Inputs) != 2 || spec.Inputs[0] != "\x1b" || spec.Inputs[1] != "\x1b" {
		t.Fatalf("inputs = %#v, want double escape", spec.Inputs)
	}
	if spec.InputDelay != 150*time.Millisecond {
		t.Fatalf("input delay = %s, want 150ms", spec.InputDelay)
	}
}

func compoundCommandCovered(allowedTools []string, cmd string) bool {
	for _, segment := range splitPipedCommand(cmd) {
		if !bashSegmentCovered(allowedTools, segment) {
			return false
		}
	}
	return true
}

func splitPipedCommand(cmd string) []string {
	rawSegments := strings.Split(cmd, "|")
	segments := make([]string, 0, len(rawSegments))
	for _, segment := range rawSegments {
		if trimmed := strings.TrimSpace(segment); trimmed != "" {
			segments = append(segments, trimmed)
		}
	}
	return segments
}

func bashSegmentCovered(allowedTools []string, segment string) bool {
	for _, tool := range allowedTools {
		cmd, ok := strings.CutPrefix(tool, "Bash(")
		if !ok {
			continue
		}
		cmd, ok = strings.CutSuffix(cmd, ":*)")
		if !ok {
			continue
		}
		if strings.HasPrefix(segment, cmd) {
			return true
		}
	}
	return false
}

func contains(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}

func containsSubsequence(values, subsequence []string) bool {
	for i := 0; i+len(subsequence) <= len(values); i++ {
		match := true
		for j := range subsequence {
			if values[i+j] != subsequence[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func flagValue(argv []string, flag string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	return ""
}

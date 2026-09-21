//go:build !windows

package cursoracp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/cursor"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	cursorHandoffFlushTimeout = 10 * time.Second
	cursorHandoffTurnTimeout  = 2 * time.Minute
)

// Provider evidence (blocked 2026-09-21): Cursor 2026.09.18-9a7762b on
// darwin/arm64 completed an authenticated ACP turn and resumed the same native
// conversation id in its interactive TUI after a bounded 10s flush. The resumed
// beforeSubmitPrompt hook reported that id, but the TUI did not recall the ACP
// marker. Cursor also deliberately omits sessionStart for --resume. Do not enable
// Cursor switching from this evidence. The default remains an isolated profile;
// AO_CURSOR_HANDOFF_USE_DEFAULT_PROFILE=1 is an explicit local-only escape hatch
// that uses the user's existing Cursor profile without copying credentials.
func TestLiveCursorInterfaceHandoff(t *testing.T) {
	if os.Getenv("AO_LIVE_CURSOR_HANDOFF") != "1" {
		t.Skip("set AO_LIVE_CURSOR_HANDOFF=1 to run the Cursor cross-interface contract")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	plugin := cursor.New()
	harness := newCursorHandoffHarness(t, plugin)
	harness.run(ctx)
}

func TestValidateCursorHandoffDataDirAcceptsRealTemporaryDirectory(t *testing.T) {
	root := t.TempDir()
	got, err := validateCursorHandoffDataDir(root)
	if err != nil {
		t.Fatalf("validateCursorHandoffDataDir: %v", err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("data dir = %q, want real path %q", got, want)
	}
	profile, err := os.Lstat(filepath.Join(root, "cursor"))
	if err != nil || !profile.IsDir() || profile.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("cursor profile = (%v, %v), want real directory", profile, err)
	}
}

func TestValidateCursorHandoffDataDirRejectsSymlinkedScratchPaths(t *testing.T) {
	t.Run("ancestor", func(t *testing.T) {
		parent := t.TempDir()
		target := t.TempDir()
		if err := os.Mkdir(filepath.Join(target, "scratch"), 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "linked-parent")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := validateCursorHandoffDataDir(filepath.Join(link, "scratch")); err == nil {
			t.Fatal("scratch root beneath a symlinked component was accepted")
		}
	})

	t.Run("root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "scratch-link")
		if err := os.Symlink(t.TempDir(), root); err != nil {
			t.Fatal(err)
		}
		if _, err := validateCursorHandoffDataDir(root); err == nil {
			t.Fatal("symlinked scratch root was accepted")
		}
	})

	t.Run("cursor profile", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "cursor")); err != nil {
			t.Fatal(err)
		}
		if _, err := validateCursorHandoffDataDir(root); err == nil {
			t.Fatal("symlinked cursor profile was accepted")
		}
	})
}

func TestValidateCursorHandoffDataDirRejectsNonScratchPaths(t *testing.T) {
	if _, err := validateCursorHandoffDataDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing scratch root was accepted")
	}
	if _, err := validateCursorHandoffDataDir(string(filepath.Separator)); err == nil {
		t.Fatal("non-temporary root was accepted")
	}
}

func TestRewriteCursorHandoffHookCommandsPreservesConfig(t *testing.T) {
	workspace := t.TempDir()
	hooksDir := filepath.Join(workspace, ".cursor")
	if err := os.Mkdir(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(hooksDir, "hooks.json")
	original := `{"version":1,"hooks":{"sessionStart":[{"command":"ao hooks cursor session-start","failClosed":true},{"command":"custom hook"}]}}`
	if err := os.WriteFile(hooksPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeAO := filepath.Join(t.TempDir(), "fake ao")
	rewriteCursorHandoffHookCommands(t, workspace, fakeAO)
	var config struct {
		Version int `json:"version"`
		Hooks   map[string][]struct {
			Command    string `json:"command"`
			FailClosed bool   `json:"failClosed"`
		} `json:"hooks"`
	}
	payload, err := os.ReadFile(hooksPath)
	if err != nil || json.Unmarshal(payload, &config) != nil {
		t.Fatalf("read rewritten hooks: %v", err)
	}
	entries := config.Hooks["sessionStart"]
	want := strconv.Quote(fakeAO) + " hooks cursor session-start"
	if config.Version != 1 || len(entries) != 2 || entries[0].Command != want || !entries[0].FailClosed || entries[1].Command != "custom hook" {
		t.Fatalf("rewritten hooks = version:%d entries:%+v", config.Version, entries)
	}
}

func TestCursorHandoffEnterSequence(t *testing.T) {
	if got := cursorHandoffEnterSequence([]byte("plain terminal")); got != "\r" {
		t.Fatalf("plain enter = %q", got)
	}
	if got := cursorHandoffEnterSequence([]byte("\x1b[>1u")); got != "\x1b[13u" {
		t.Fatalf("kitty enter = %q", got)
	}
}

type cursorHandoffHarness struct {
	t         *testing.T
	plugin    *cursor.Plugin
	driver    ports.ChatDriver
	workspace string
	dataDir   string
	hookDir   string
	env       map[string]string
	version   string
	markers   [3]string
}

type cursorHandoffDefaultProfilePlugin struct{ plugin *cursor.Plugin }

func (p cursorHandoffDefaultProfilePlugin) ResolveBinary(ctx context.Context) (string, error) {
	return p.plugin.ResolveBinary(ctx)
}

func (cursorHandoffDefaultProfilePlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return ports.AgentAuthStatusAuthorized, nil
}

func newCursorHandoffHarness(t *testing.T, plugin *cursor.Plugin) *cursorHandoffHarness {
	t.Helper()
	dataDir := cursorHandoffDataDir(t)
	workspace := filepath.Join(dataDir, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatalf("Cursor handoff setup failed: create workspace")
	}
	actualProfile := os.Getenv("AO_CURSOR_HANDOFF_USE_DEFAULT_PROFILE") == "1"
	hookDir := filepath.Join(dataDir, "cursor-handoff-hooks")
	binDir := filepath.Join(dataDir, "cursor-handoff-bin")
	if err := os.MkdirAll(hookDir, 0o700); err != nil {
		t.Fatalf("Cursor handoff setup failed: create hook recorder")
	}
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("Cursor handoff setup failed: create fake ao directory")
	}
	fakeAO := []byte("#!/bin/sh\n" +
		"set -eu\n" +
		"event=${3:-unknown}\n" +
		"tmp=\"$AO_CURSOR_HANDOFF_HOOK_DIR/.hook.$$\"\n" +
		"umask 077\n" +
		"cat > \"$tmp\"\n" +
		"mv \"$tmp\" \"$AO_CURSOR_HANDOFF_HOOK_DIR/$event.$$.json\"\n" +
		"printf '{}\\n'\n")
	if err := os.WriteFile(filepath.Join(binDir, "ao"), fakeAO, 0o700); err != nil {
		t.Fatalf("Cursor handoff setup failed: write fake ao executable")
	}

	env := liveEnvMap()
	env["AO_DATA_DIR"] = dataDir
	env["AO_CURSOR_HANDOFF_HOOK_DIR"] = hookDir
	env["PATH"] = binDir + string(os.PathListSeparator) + env["PATH"]
	env["TERM"] = "xterm-256color"
	driver := New(plugin, nil)
	if actualProfile {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal("Cursor handoff setup failed: home directory unavailable")
		}
		env["CURSOR_DATA_DIR"] = filepath.Join(home, ".cursor")
		driver = New(cursorHandoffDefaultProfilePlugin{plugin: plugin}, nil)
	} else {
		plugin.AugmentRuntimeEnv(env, dataDir)
	}
	t.Setenv("CURSOR_DATA_DIR", env["CURSOR_DATA_DIR"])
	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		DataDir: dataDir, Env: env, SessionID: "live-cursor-handoff", WorkspacePath: workspace,
	}); err != nil {
		t.Fatalf("Cursor handoff setup failed: install hooks")
	}
	t.Cleanup(func() {
		_ = plugin.CleanupWorkspace(context.Background(), ports.WorkspaceHookConfig{
			DataDir: dataDir, Env: env, SessionID: "live-cursor-handoff", WorkspacePath: workspace,
		})
	})
	rewriteCursorHandoffHookCommands(t, workspace, filepath.Join(binDir, "ao"))

	version := cursorHandoffVersion(t, plugin)
	return &cursorHandoffHarness{
		t: t, plugin: plugin, driver: driver, workspace: workspace,
		dataDir: dataDir, hookDir: hookDir, env: env, version: version,
		markers: [3]string{randomCursorHandoffMarker(t), randomCursorHandoffMarker(t), randomCursorHandoffMarker(t)},
	}
}

func rewriteCursorHandoffHookCommands(t *testing.T, workspace, fakeAO string) {
	t.Helper()
	hooksPath := filepath.Join(workspace, ".cursor", "hooks.json")
	payload, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("Cursor handoff setup failed: read hooks")
	}
	var config struct {
		Version int                         `json:"version"`
		Hooks   map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(payload, &config); err != nil {
		t.Fatalf("Cursor handoff setup failed: decode hooks")
	}
	for _, entries := range config.Hooks {
		for _, entry := range entries {
			command, _ := entry["command"].(string)
			if strings.HasPrefix(command, "ao ") {
				entry["command"] = strconv.Quote(fakeAO) + strings.TrimPrefix(command, "ao")
			}
		}
	}
	payload, err = json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("Cursor handoff setup failed: encode hooks")
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(hooksPath, payload, 0o600); err != nil {
		t.Fatalf("Cursor handoff setup failed: write hooks")
	}
}

func cursorHandoffDataDir(t *testing.T) string {
	t.Helper()
	dataDir, configured := os.LookupEnv("AO_CURSOR_HANDOFF_DATA_DIR")
	if !configured {
		dataDir = t.TempDir()
	}
	dataDir = strings.TrimSpace(dataDir)
	validated, err := validateCursorHandoffDataDir(dataDir)
	if err != nil {
		t.Fatalf("AO_CURSOR_HANDOFF_DATA_DIR is not a real temporary scratch root: %v", err)
	}
	return validated
}

func validateCursorHandoffDataDir(dataDir string) (string, error) {
	dataDir = filepath.Clean(strings.TrimSpace(dataDir))
	if dataDir == "." || !filepath.IsAbs(dataDir) {
		return "", errors.New("path must be non-empty and absolute")
	}
	info, err := os.Lstat(dataDir)
	if err != nil {
		return "", errors.New("scratch root must already exist")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("scratch root must be a real directory")
	}
	realRoot, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return "", errors.New("scratch root cannot be resolved")
	}
	realTemp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", errors.New("system temporary root cannot be resolved")
	}
	tempPath := filepath.Clean(os.TempDir())
	rel, err := filepath.Rel(tempPath, dataDir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("scratch root must be a child of the system temporary directory")
	}
	if filepath.Join(realTemp, rel) != realRoot {
		return "", errors.New("scratch root path must not contain symlinked components")
	}

	profile := filepath.Join(dataDir, "cursor")
	profileInfo, err := os.Lstat(profile)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(profile, 0o700); err != nil {
			return "", errors.New("cursor profile directory cannot be created")
		}
		profileInfo, err = os.Lstat(profile)
	}
	if err != nil || profileInfo.Mode()&os.ModeSymlink != 0 || !profileInfo.IsDir() {
		return "", errors.New("cursor profile must be a real directory")
	}
	realProfile, err := filepath.EvalSymlinks(profile)
	if err != nil || filepath.Dir(realProfile) != realRoot {
		return "", errors.New("cursor profile must remain inside the scratch root")
	}
	return realRoot, nil
}

func (h *cursorHandoffHarness) run(ctx context.Context) {
	h.t.Helper()
	if _, err := h.driver.Probe(ctx); err != nil {
		h.t.Fatalf("Cursor %s handoff probe failed", h.version)
	}

	conversation, err := h.driver.Start(ctx, ports.ChatStartConfig{
		SessionID: "live-cursor-handoff", DataDir: h.dataDir, WorkspacePath: h.workspace,
		Env: h.env, Permissions: ports.PermissionModeDefault,
		ProviderScopeID: "live-cursor-handoff", ProviderIDsScoped: true,
	})
	if err != nil {
		if errors.Is(err, ports.ErrChatAuthRequired) {
			h.t.Fatalf("Cursor %s ACP start failed: authentication required for isolated CURSOR_DATA_DIR", h.version)
		}
		h.t.Fatalf("Cursor %s ACP start failed", h.version)
	}
	stopConversation := h.registerConversationCleanup(conversation)
	providerID := strings.TrimSpace(conversation.ProviderConversationID())
	if providerID == "" {
		h.t.Fatalf("Cursor %s native conversation id is empty", h.version)
	}

	acknowledgement := randomCursorHandoffMarker(h.t)
	ref := sendLiveTurnSanitized(ctx, h.t, conversation,
		"Remember the private code "+h.markers[0]+" for later. Reply with the acknowledgement "+acknowledgement+".")
	answer := waitForLiveTurnSanitized(ctx, h.t, conversation, ref.ProviderTurnID)
	if !strings.Contains(answer, acknowledgement) {
		h.t.Fatalf("Cursor %s ACP turn A completed without its in-memory acknowledgement", h.version)
	}
	h.stopConversation(stopConversation, "ACP turn A")
	time.Sleep(cursorHandoffFlushTimeout)

	tui := h.startTUI(ctx, providerID)
	if tui.outputContains("Failed to resume chat") {
		h.t.Fatalf("Cursor %s TUI rejected ACP native id=%s", h.version, providerID)
	}
	tui.clearOutput()
	stopCount := len(h.hookRecords("stop"))
	promptRecords := h.hookRecordNames("user-prompt-submit")
	bOpen, bClose := randomCursorHandoffDelimiter(h.t), randomCursorHandoffDelimiter(h.t)
	bRecallSentinel := bOpen + h.markers[0] + bClose
	tui.writePrompt(h.t, "Recall the private code from the previous turn and place it directly between "+bOpen+
		" and "+bClose+", with no spaces. Then include this second code: "+h.markers[1]+".")
	if !tui.waitForOutput([]string{h.markers[1]}, 5*time.Second) {
		h.t.Fatalf("Cursor %s TUI did not echo submitted input", h.version)
	}
	h.waitForHookCount("user-prompt-submit", len(promptRecords)+1, 30*time.Second)
	h.assertNewForegroundPromptID(providerID, promptRecords)
	if !tui.waitForOutput([]string{bRecallSentinel}, cursorHandoffTurnTimeout) {
		h.t.Fatalf("Cursor %s TUI turn B did not recall the ACP marker; native id=%s", h.version, providerID)
	}
	h.waitForHookCount("stop", stopCount+1, 5*time.Second)
	h.stopTUI(tui)

	resumed, stopResumed := h.resumeACP(ctx, providerID)
	history := h.waitForReplay(ctx, resumed, h.markers[:2])
	h.assertStableReplay(ctx, resumed, history, h.markers[:2])

	ref = sendLiveTurnSanitized(ctx, h.t, resumed,
		"State the two private codes already in this conversation, then include this third code: "+h.markers[2]+".")
	answer = waitForLiveTurnSanitized(ctx, h.t, resumed, ref.ProviderTurnID)
	for _, marker := range h.markers {
		if !strings.Contains(answer, marker) {
			h.t.Fatalf("Cursor %s ACP turn C omitted an in-memory prior marker; native id=%s", h.version, providerID)
		}
	}
	h.stopConversation(stopResumed, "ACP turn C")

	tui = h.startTUI(ctx, providerID)
	tui.clearOutput()
	stopCount = len(h.hookRecords("stop"))
	promptRecords = h.hookRecordNames("user-prompt-submit")
	var expected []string
	var echoedDelimiter string
	var instruction strings.Builder
	instruction.WriteString("State all three private codes from this conversation. For each code in order, place it directly between its assigned delimiters with no spaces: ")
	for index, marker := range h.markers {
		open, closing := randomCursorHandoffDelimiter(h.t), randomCursorHandoffDelimiter(h.t)
		echoedDelimiter = open
		expected = append(expected, open+marker+closing)
		if index > 0 {
			instruction.WriteString("; ")
		}
		fmt.Fprintf(&instruction, "code %d between %s and %s", index+1, open, closing)
	}
	tui.writePrompt(h.t, instruction.String()+".")
	if !tui.waitForOutput([]string{echoedDelimiter}, 5*time.Second) {
		h.t.Fatalf("Cursor %s final TUI did not echo submitted input", h.version)
	}
	h.waitForHookCount("user-prompt-submit", len(promptRecords)+1, 30*time.Second)
	h.assertNewForegroundPromptID(providerID, promptRecords)
	if !tui.waitForOutput(expected, cursorHandoffTurnTimeout) {
		h.t.Fatalf("Cursor %s final TUI turn omitted a prior marker; native id=%s", h.version, providerID)
	}
	h.waitForHookCount("stop", stopCount+1, 5*time.Second)
	h.stopTUI(tui)
}

func (h *cursorHandoffHarness) resumeACP(
	ctx context.Context,
	providerID string,
) (ports.ChatConversation, func() error) {
	h.t.Helper()
	conversation, err := h.driver.Resume(ctx, ports.ChatResumeConfig{
		SessionID: "live-cursor-handoff", ProviderConversationID: providerID,
		DataDir: h.dataDir, WorkspacePath: h.workspace, Env: h.env,
		Permissions: ports.PermissionModeDefault, ProviderScopeID: "live-cursor-handoff",
		ProviderIDsScoped: true,
	})
	if err != nil {
		h.t.Fatalf("Cursor %s ACP resume failed; native id=%s", h.version, providerID)
	}
	stopConversation := h.registerConversationCleanup(conversation)
	if got := strings.TrimSpace(conversation.ProviderConversationID()); got != providerID {
		h.stopConversation(stopConversation, "mismatched ACP resume")
		h.t.Fatalf("Cursor %s ACP resume native id=%s, want=%s", h.version, got, providerID)
	}
	if _, ok := conversation.(ports.ChatHistoryReader); !ok {
		h.stopConversation(stopConversation, "missing history reader")
		h.t.Fatalf("Cursor %s ACP resume has no ChatHistoryReader; native id=%s", h.version, providerID)
	}
	if _, ok := conversation.(ports.ChatHistoryRefresher); !ok {
		h.stopConversation(stopConversation, "missing history refresher")
		h.t.Fatalf("Cursor %s ACP resume has no ChatHistoryRefresher; native id=%s", h.version, providerID)
	}
	return conversation, stopConversation
}

func (h *cursorHandoffHarness) waitForReplay(
	ctx context.Context,
	conversation ports.ChatConversation,
	markers []string,
) []ports.ChatEvent {
	h.t.Helper()
	reader := conversation.(ports.ChatHistoryReader)
	refresher := conversation.(ports.ChatHistoryRefresher)
	deadline := time.Now().Add(cursorHandoffFlushTimeout)
	history, err := reader.ReadHistory(ctx)
	for {
		counts, order := cursorHandoffMarkerCounts(history, markers)
		if err == nil && allCursorHandoffCountsEqual(counts, 1) && sort.IntsAreSorted(order) {
			h.assertReplayIdentities(history, markers)
			return history
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("Cursor %s ACP replay did not converge within %s; marker counts=%v order=%v native id=%s",
				h.version, cursorHandoffFlushTimeout, counts, order, conversation.ProviderConversationID())
		}
		time.Sleep(250 * time.Millisecond)
		history, err = refresher.RefreshHistory(ctx)
	}
}

func (h *cursorHandoffHarness) assertReplayIdentities(history []ports.ChatEvent, markers []string) {
	h.t.Helper()
	for markerIndex, marker := range markers {
		var user *ports.ChatEvent
		for i := range history {
			if history[i].Kind == ports.ChatEventUserMessageCompleted && strings.Contains(history[i].Text, marker) {
				user = &history[i]
				break
			}
		}
		if user == nil {
			h.t.Fatalf("Cursor %s ACP replay marker index=%d is absent", h.version, markerIndex)
		}
		if user.NativeUserMessageID == "" || user.ProviderTurnID == "" || user.ProviderItemID == "" || user.ProviderEventID == "" {
			h.t.Fatalf("Cursor %s ACP replay marker index=%d identity fields: NativeUserMessageID=%q ProviderTurnID=%q ProviderItemID=%q ProviderEventID=%q",
				h.version, markerIndex, user.NativeUserMessageID, user.ProviderTurnID, user.ProviderItemID, user.ProviderEventID)
		}
		assistantItems := make(map[string]struct{})
		turnCompleted := false
		for _, event := range history {
			if event.ProviderTurnID != user.ProviderTurnID {
				continue
			}
			if event.ProviderEventID == "" {
				h.t.Fatalf("Cursor %s ACP replay marker index=%d has empty ProviderEventID; ProviderTurnID=%s kind=%s",
					h.version, markerIndex, user.ProviderTurnID, event.Kind)
			}
			if event.Kind == ports.ChatEventMessageCompleted && event.ProviderItemID != "" {
				assistantItems[event.ProviderItemID] = struct{}{}
			}
			turnCompleted = turnCompleted || event.Kind == ports.ChatEventTurnCompleted
		}
		if len(assistantItems) == 0 || !turnCompleted {
			h.t.Fatalf("Cursor %s ACP replay marker index=%d lacks a structured assistant identity or completed boundary; ProviderTurnID=%s assistant item count=%d completed=%t",
				h.version, markerIndex, user.ProviderTurnID, len(assistantItems), turnCompleted)
		}
	}
}

func (h *cursorHandoffHarness) assertStableReplay(
	ctx context.Context,
	conversation ports.ChatConversation,
	want []ports.ChatEvent,
	markers []string,
) {
	h.t.Helper()
	got, err := conversation.(ports.ChatHistoryRefresher).RefreshHistory(ctx)
	if err != nil {
		h.t.Fatalf("Cursor %s ACP replay refresh failed; native id=%s", h.version, conversation.ProviderConversationID())
	}
	gotCounts, gotOrder := cursorHandoffMarkerCounts(got, markers)
	if !allCursorHandoffCountsEqual(gotCounts, 1) || !sort.IntsAreSorted(gotOrder) {
		h.t.Fatalf("Cursor %s refreshed ACP replay marker counts=%v order=%v native id=%s",
			h.version, gotCounts, gotOrder, conversation.ProviderConversationID())
	}
	if len(got) != len(want) {
		h.t.Fatalf("Cursor %s ACP replay event count changed across refresh: first=%d second=%d native id=%s",
			h.version, len(want), len(got), conversation.ProviderConversationID())
	}
	for i := range want {
		if cursorHandoffEventIdentity(want[i]) != cursorHandoffEventIdentity(got[i]) {
			h.t.Fatalf("Cursor %s ACP replay identity changed at index=%d; first=%s second=%s native id=%s",
				h.version, i, cursorHandoffEventIdentity(want[i]), cursorHandoffEventIdentity(got[i]),
				conversation.ProviderConversationID())
		}
	}
}

func cursorHandoffEventIdentity(event ports.ChatEvent) string {
	return fmt.Sprintf("kind=%s NativeUserMessageID=%s ProviderTurnID=%s ProviderItemID=%s ProviderEventID=%s",
		event.Kind, event.NativeUserMessageID, event.ProviderTurnID, event.ProviderItemID, event.ProviderEventID)
}

func cursorHandoffMarkerCounts(history []ports.ChatEvent, markers []string) ([]int, []int) {
	counts := make([]int, len(markers))
	order := make([]int, 0, len(markers))
	for _, event := range history {
		if event.Kind != ports.ChatEventUserMessageCompleted {
			continue
		}
		for markerIndex, marker := range markers {
			if strings.Contains(event.Text, marker) {
				counts[markerIndex]++
				if counts[markerIndex] == 1 {
					// Record which expected marker appeared next; event indices
					// alone are always sorted because history is traversed in order.
					order = append(order, markerIndex)
				}
			}
		}
	}
	return counts, order
}

func TestCursorHandoffMarkerCountsRejectsReversedChronology(t *testing.T) {
	for _, tt := range []struct {
		name          string
		texts         []string
		wantConverged bool
	}{
		{"ordered", []string{"first-marker", "second-marker"}, true},
		{"reversed", []string{"second-marker", "first-marker"}, false},
		{"missing", []string{"first-marker"}, false},
		{"duplicated", []string{"first-marker", "second-marker", "first-marker"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			history := []ports.ChatEvent{{Kind: ports.ChatEventMessageCompleted, Text: "second-marker first-marker"}}
			for _, text := range tt.texts {
				history = append(history, ports.ChatEvent{Kind: ports.ChatEventUserMessageCompleted, Text: text})
			}
			counts, order := cursorHandoffMarkerCounts(history, []string{"first-marker", "second-marker"})
			if got := allCursorHandoffCountsEqual(counts, 1) && sort.IntsAreSorted(order); got != tt.wantConverged {
				t.Fatalf("counts=%v order=%v converged=%t, want %t", counts, order, got, tt.wantConverged)
			}
		})
	}
}

func allCursorHandoffCountsEqual(counts []int, want int) bool {
	for _, count := range counts {
		if count != want {
			return false
		}
	}
	return true
}

type cursorHandoffPTY struct {
	file          *os.File
	cmd           *exec.Cmd
	done          chan error
	mu            sync.Mutex
	out           bytes.Buffer
	kittyKeyboard bool
}

func (h *cursorHandoffHarness) startTUI(
	ctx context.Context,
	providerID string,
) *cursorHandoffPTY {
	h.t.Helper()
	argv, ok, err := h.plugin.GetRestoreCommand(ctx, ports.RestoreConfig{
		DataDir: h.dataDir, Permissions: ports.PermissionModeDefault,
		Session: ports.SessionRef{
			ID: "live-cursor-handoff", WorkspacePath: h.workspace, DataDir: h.dataDir,
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: providerID},
		},
	})
	if err != nil || !ok || len(argv) == 0 {
		h.t.Fatalf("Cursor %s could not construct TUI restore command; native id=%s", h.version, providerID)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = h.workspace
	cmd.Env = cursorHandoffEnvList(h.env)
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		h.t.Fatalf("Cursor %s TUI resume failed; native id=%s", h.version, providerID)
	}
	process := &cursorHandoffPTY{file: file, cmd: cmd, done: make(chan error, 1)}
	go process.capture()
	go func() { process.done <- cmd.Wait() }()
	h.t.Cleanup(func() { process.forceStop() })
	if !process.waitForOutput([]string{"\x1b[?2004h"}, 30*time.Second) {
		process.mu.Lock()
		resumeRejected := strings.Contains(process.out.String(), "Failed to resume chat")
		process.mu.Unlock()
		h.t.Fatalf("Cursor %s resumed TUI did not reach the composer; resume rejected=%t", h.version, resumeRejected)
	}
	return process
}

func (p *cursorHandoffPTY) capture() {
	buffer := make([]byte, 4096)
	for {
		n, err := p.file.Read(buffer)
		if n > 0 {
			p.mu.Lock()
			_, _ = p.out.Write(buffer[:n])
			p.kittyKeyboard = p.kittyKeyboard || cursorHandoffEnterSequence(p.out.Bytes()) == "\x1b[13u"
			p.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (p *cursorHandoffPTY) clearOutput() {
	p.mu.Lock()
	p.out.Reset()
	p.mu.Unlock()
}

func (p *cursorHandoffPTY) outputContains(marker string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return bytes.Contains(p.out.Bytes(), []byte(marker))
}

func (p *cursorHandoffPTY) waitForOutput(markers []string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		found := true
		for _, marker := range markers {
			found = found && p.outputContains(marker)
		}
		if found {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func (p *cursorHandoffPTY) writePrompt(t *testing.T, prompt string) {
	t.Helper()
	p.mu.Lock()
	enter := "\r"
	if p.kittyKeyboard {
		enter = "\x1b[13u"
	}
	p.mu.Unlock()
	if _, err := io.WriteString(p.file, prompt+enter); err != nil {
		t.Fatal("Cursor TUI prompt submission failed")
	}
}

func cursorHandoffEnterSequence(output []byte) string {
	if bytes.Contains(output, []byte("\x1b[>1u")) {
		return "\x1b[13u"
	}
	return "\r"
}

func (h *cursorHandoffHarness) stopTUI(process *cursorHandoffPTY) {
	h.t.Helper()
	_, _ = process.file.Write([]byte{3})
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-process.done:
		_ = process.file.Close()
		return
	case <-timer.C:
		_, _ = process.file.Write([]byte{3})
	}
	timer.Reset(5 * time.Second)
	select {
	case <-process.done:
		_ = process.file.Close()
	case <-timer.C:
		process.forceStop()
		h.t.Fatalf("Cursor %s TUI did not stop within 10s", h.version)
	}
}

func (p *cursorHandoffPTY) forceStop() {
	_ = p.file.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

type cursorHookRecord struct {
	name   string
	fields map[string]json.RawMessage
}

func (h *cursorHandoffHarness) hookRecords(event string) []cursorHookRecord {
	h.t.Helper()
	entries, err := os.ReadDir(h.hookDir)
	if err != nil {
		h.t.Fatalf("Cursor %s hook record count failed", h.version)
	}
	records := make([]cursorHookRecord, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), event+".") {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(h.hookDir, entry.Name()))
		if err != nil {
			continue
		}
		fields := make(map[string]json.RawMessage)
		if json.Unmarshal(payload, &fields) == nil {
			records = append(records, cursorHookRecord{name: entry.Name(), fields: fields})
		}
	}
	return records
}

func (h *cursorHandoffHarness) hookRecordNames(event string) map[string]struct{} {
	h.t.Helper()
	names := make(map[string]struct{})
	for _, record := range h.hookRecords(event) {
		names[record.name] = struct{}{}
	}
	return names
}

func (h *cursorHandoffHarness) waitForHookCount(event string, want int, timeout time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(h.hookRecords(event)) >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("Cursor %s hook event=%s count=%d want-at-least=%d",
		h.version, event, len(h.hookRecords(event)), want)
}

func (h *cursorHandoffHarness) assertNewForegroundPromptID(want string, before map[string]struct{}) {
	h.t.Helper()
	records := h.hookRecords("user-prompt-submit")
	var newRecords []cursorHookRecord
	for _, record := range records {
		if _, existed := before[record.name]; !existed {
			newRecords = append(newRecords, record)
		}
	}
	if len(newRecords) != 1 {
		h.t.Fatalf("Cursor %s beforeSubmitPrompt new hook count=%d want=1", h.version, len(newRecords))
	}
	record := newRecords[0]
	field, got := cursorHookNativeID(record.fields)
	if got != want {
		h.t.Fatalf("Cursor %s beforeSubmitPrompt identity field=%s id=%s want=%s fields=%v",
			h.version, field, got, want, cursorHookFieldNames(record.fields))
	}
	for _, childField := range []string{
		"agent_id", "agentId", "subagent_id", "subagentId", "parent_agent_id", "parentAgentId",
	} {
		if _, present := record.fields[childField]; present {
			h.t.Fatalf("Cursor %s beforeSubmitPrompt carried child field=%s; native id=%s",
				h.version, childField, want)
		}
	}
}

func cursorHookNativeID(fields map[string]json.RawMessage) (string, string) {
	for _, name := range []string{"session_id", "sessionId", "conversation_id", "conversationId"} {
		var value string
		if json.Unmarshal(fields[name], &value) == nil && strings.TrimSpace(value) != "" {
			return name, strings.TrimSpace(value)
		}
	}
	return "", ""
}

func cursorHookFieldNames(fields map[string]json.RawMessage) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (h *cursorHandoffHarness) registerConversationCleanup(conversation ports.ChatConversation) func() error {
	h.t.Helper()
	var once sync.Once
	var stopErr error
	stop := func() error {
		once.Do(func() {
			if terminator, ok := conversation.(ports.ChatProviderTerminator); ok {
				stopErr = terminator.Terminate()
				return
			}
			stopErr = conversation.Close()
		})
		return stopErr
	}
	h.t.Cleanup(func() { _ = stop() })
	return stop
}

func (h *cursorHandoffHarness) stopConversation(stop func() error, stage string) {
	h.t.Helper()
	if err := stop(); err != nil {
		h.t.Fatalf("Cursor %s %s stop failed", h.version, stage)
	}
}

func cursorHandoffVersion(t *testing.T, plugin *cursor.Plugin) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	binary, err := plugin.ResolveBinary(ctx)
	if err != nil {
		t.Fatal("Cursor handoff setup failed: binary unavailable")
	}
	if resolved, resolveErr := filepath.EvalSymlinks(binary); resolveErr == nil {
		versionDir := filepath.Base(filepath.Dir(resolved))
		if _, ok := parseCursorVersion(versionDir); ok {
			return versionDir
		}
	}
	output, err := exec.CommandContext(ctx, binary, "--version").CombinedOutput()
	if err != nil {
		t.Fatal("Cursor handoff setup failed: version unavailable")
	}
	version := strings.TrimSpace(string(output))
	if version == "" {
		t.Fatal("Cursor handoff setup failed: empty version")
	}
	return strings.Join(strings.Fields(version), " ")
}

func randomCursorHandoffMarker(t *testing.T) string {
	t.Helper()
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		t.Fatal("Cursor handoff setup failed: random marker")
	}
	return "ao-cursor-handoff-" + hex.EncodeToString(value)
}

func randomCursorHandoffDelimiter(t *testing.T) string {
	t.Helper()
	value := make([]byte, 6)
	if _, err := rand.Read(value); err != nil {
		t.Fatal("Cursor handoff setup failed: random recall delimiter")
	}
	return "d" + hex.EncodeToString(value) + "_"
}

func cursorHandoffEnvList(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	values := make([]string, 0, len(names))
	for _, name := range names {
		values = append(values, name+"="+env[name])
	}
	return values
}

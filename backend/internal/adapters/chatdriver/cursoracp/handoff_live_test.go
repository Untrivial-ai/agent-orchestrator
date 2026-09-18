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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/cursor"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/creack/pty"
)

const (
	cursorHandoffFlushTimeout = 10 * time.Second
	cursorHandoffTurnTimeout  = 2 * time.Minute
)

// Provider evidence (blocked 2026-09-18): Cursor 2026.09.02-c22c1a3 on
// darwin/arm64 reached ACP session/new, which returned Authentication required
// for the isolated CURSOR_DATA_DIR at <scratch AO_DATA_DIR>/cursor. The user's
// normal Cursor profile was authenticated, but credentials were deliberately
// neither copied nor imported into the isolated profile. Consequently the
// sessionStart identity field, ACP replay identity fields, and bounded 10s
// flush result remain unobserved. Re-run this gate only after the user explicitly
// authenticates that isolated profile with AO_CURSOR_HANDOFF_DATA_DIR set to an
// absolute scratch root, or supplies CURSOR_API_KEY to the process. Do not enable
// Cursor switching from this evidence.
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
	if got != filepath.Clean(root) {
		t.Fatalf("data dir = %q, want %q", got, filepath.Clean(root))
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

func newCursorHandoffHarness(t *testing.T, plugin *cursor.Plugin) *cursorHandoffHarness {
	t.Helper()
	workspace := t.TempDir()
	dataDir := cursorHandoffDataDir(t)
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
	plugin.AugmentRuntimeEnv(env, dataDir)
	t.Setenv("CURSOR_DATA_DIR", env["CURSOR_DATA_DIR"])
	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		DataDir: dataDir, Env: env, SessionID: "live-cursor-handoff", WorkspacePath: workspace,
	}); err != nil {
		t.Fatalf("Cursor handoff setup failed: install hooks")
	}

	version := cursorHandoffVersion(t, plugin)
	return &cursorHandoffHarness{
		t: t, plugin: plugin, driver: New(plugin, nil), workspace: workspace,
		dataDir: dataDir, hookDir: hookDir, env: env, version: version,
		markers: [3]string{randomCursorHandoffMarker(t), randomCursorHandoffMarker(t), randomCursorHandoffMarker(t)},
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
	return dataDir, nil
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

	firstSessionStarts := len(h.hookRecords("session-start"))
	tui := h.startTUI(ctx, providerID, firstSessionStarts)
	h.assertSessionStartIDs(providerID)
	tui.clearOutput()
	stopCount := len(h.hookRecords("stop"))
	bOpen, bClose := randomCursorHandoffDelimiter(h.t), randomCursorHandoffDelimiter(h.t)
	bRecallSentinel := bOpen + h.markers[0] + bClose
	tui.writePrompt(h.t, "Recall the private code from the previous turn and place it directly between "+bOpen+
		" and "+bClose+", with no spaces. Then include this second code: "+h.markers[1]+".")
	h.waitForHookCount("stop", stopCount+1, cursorHandoffTurnTimeout)
	if !tui.waitForOutput([]string{bRecallSentinel}, 5*time.Second) {
		h.t.Fatalf("Cursor %s TUI turn B did not recall the ACP marker; native id=%s", h.version, providerID)
	}
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

	secondSessionStarts := len(h.hookRecords("session-start"))
	tui = h.startTUI(ctx, providerID, secondSessionStarts)
	h.assertSessionStartIDs(providerID)
	tui.clearOutput()
	stopCount = len(h.hookRecords("stop"))
	var expected []string
	var instruction strings.Builder
	instruction.WriteString("State all three private codes from this conversation. For each code in order, place it directly between its assigned delimiters with no spaces: ")
	for index, marker := range h.markers {
		open, close := randomCursorHandoffDelimiter(h.t), randomCursorHandoffDelimiter(h.t)
		expected = append(expected, open+marker+close)
		if index > 0 {
			instruction.WriteString("; ")
		}
		fmt.Fprintf(&instruction, "code %d between %s and %s", index+1, open, close)
	}
	tui.writePrompt(h.t, instruction.String()+".")
	h.waitForHookCount("stop", stopCount+1, cursorHandoffTurnTimeout)
	if !tui.waitForOutput(expected, 5*time.Second) {
		h.t.Fatalf("Cursor %s final TUI turn omitted a prior marker; native id=%s", h.version, providerID)
	}
	h.stopTUI(tui)
	h.assertSessionStartIDs(providerID)
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
	file *os.File
	cmd  *exec.Cmd
	done chan error
	mu   sync.Mutex
	out  bytes.Buffer
}

func (h *cursorHandoffHarness) startTUI(
	ctx context.Context,
	providerID string,
	previousSessionStarts int,
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
	h.waitForHookCount("session-start", previousSessionStarts+1, 30*time.Second)
	return process
}

func (p *cursorHandoffPTY) capture() {
	buffer := make([]byte, 4096)
	for {
		n, err := p.file.Read(buffer)
		if n > 0 {
			p.mu.Lock()
			_, _ = p.out.Write(buffer[:n])
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
	if _, err := io.WriteString(p.file, prompt+"\r"); err != nil {
		t.Fatal("Cursor TUI prompt submission failed")
	}
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
			records = append(records, cursorHookRecord{fields: fields})
		}
	}
	return records
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

func (h *cursorHandoffHarness) assertSessionStartIDs(want string) {
	h.t.Helper()
	records := h.hookRecords("session-start")
	if len(records) == 0 {
		h.t.Fatalf("Cursor %s sessionStart hook count=0", h.version)
	}
	for index, record := range records {
		field, got := cursorHookNativeID(record.fields)
		if got != want {
			h.t.Fatalf("Cursor %s sessionStart index=%d identity field=%s id=%s want=%s fields=%v",
				h.version, index, field, got, want, cursorHookFieldNames(record.fields))
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

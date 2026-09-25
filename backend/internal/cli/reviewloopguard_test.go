package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNoopProbeCommand(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"true", true},
		{"true\n", true},
		{":", true},
		{"echo", true},
		{"echo ok", true},
		{"echo   marker-7   ", true},
		{"echo composing", true},
		{`printf '%s' '{}'`, true},
		{"", false},
		{"git status", false},
		{"echo a | gh api", false},
		{"printf '%s' '{}' | ao review submit --session w --reviews -", false},
		{"echo a && rm -rf /tmp/x", false},
		{"echo a > /tmp/x", false},
		{"echo a; echo b", false},
		{"true && echo real-work", false},
	}
	for _, tc := range cases {
		if got := noopProbeCommand(tc.command); got != tc.want {
			t.Fatalf("noopProbeCommand(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}

func TestReviewLoopToolKey(t *testing.T) {
	same, _ := reviewLoopToolKey([]byte(`{"tool_name":"Bash","tool_input":{"command":"git diff\n--stat"}}`))
	equivalent, _ := reviewLoopToolKey([]byte(`{"tool_name":"Bash","tool_input":{"command":"git diff --stat"}}`))
	if same != equivalent {
		t.Fatalf("whitespace re-wrap changed the key: %q vs %q", same, equivalent)
	}
	readA, _ := reviewLoopToolKey([]byte(`{"tool_name":"Read","tool_input":{"file_path":"/tmp/x.json","offset":281,"limit":9}}`))
	readB, _ := reviewLoopToolKey([]byte(`{"tool_name":"Read","tool_input":{"file_path":"/tmp/x.json","offset":290,"limit":9}}`))
	if readA == readB {
		t.Fatalf("different Read offsets share a key: %q", readA)
	}
	if _, ok := reviewLoopToolKey([]byte(`{"tool_input":{}}`)); ok {
		t.Fatalf("payload without tool_name produced a key")
	}
}

func probePayload(n int) []byte {
	return []byte(`{"tool_name":"Bash","tool_input":{"command":"echo marker-` + strconv.Itoa(n) + `"}}`)
}

func TestReviewLoopGuardDecisionProbeStorm(t *testing.T) {
	dir := t.TempDir()
	session := "review-7"
	for n := 1; n <= 4; n++ {
		if _, denied := reviewLoopGuardDecision(probePayload(n), dir, session); denied {
			t.Fatalf("probe %d denied before the limit", n)
		}
	}
	out, denied := reviewLoopGuardDecision(probePayload(5), dir, session)
	if !denied {
		t.Fatalf("probe 5 not denied")
	}
	if out.HookSpecificOutput.HookEventName != "PreToolUse" || out.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("bad deny output: %+v", out.HookSpecificOutput)
	}
	if !strings.Contains(out.HookSpecificOutput.PermissionDecisionMessage, "no-op probe") {
		t.Fatalf("deny message missing the probe explanation: %q", out.HookSpecificOutput.PermissionDecisionMessage)
	}
	// The denial resets the streak: the next probes start counting from zero.
	for n := 6; n <= 9; n++ {
		if _, denied := reviewLoopGuardDecision(probePayload(n), dir, session); denied {
			t.Fatalf("probe %d denied before the limit", n)
		}
	}
	if _, denied := reviewLoopGuardDecision(probePayload(10), dir, session); !denied {
		t.Fatalf("probe 10 (5th of the new streak) not denied")
	}
}

func TestReviewLoopGuardDecisionIdenticalRepeats(t *testing.T) {
	dir := t.TempDir()
	session := "review-8"
	read := func(offset int) []byte {
		return []byte(`{"tool_name":"Read","tool_input":{"file_path":"/tmp/review.json","offset":` + strconv.Itoa(offset) + `,"limit":9}}`)
	}
	for n := 1; n <= 4; n++ {
		if _, denied := reviewLoopGuardDecision(read(281), dir, session); denied {
			t.Fatalf("repeat %d denied before the limit", n)
		}
	}
	out, denied := reviewLoopGuardDecision(read(281), dir, session)
	if !denied {
		t.Fatalf("identical repeat 5 not denied")
	}
	if !strings.Contains(out.HookSpecificOutput.PermissionDecisionMessage, "times in a row") {
		t.Fatalf("deny message missing the repeat explanation: %q", out.HookSpecificOutput.PermissionDecisionMessage)
	}
	// Offset paging after the reset is different work, not a repeat.
	if _, denied := reviewLoopGuardDecision(read(290), dir, session); denied {
		t.Fatalf("page turn after reset was denied")
	}
}

func TestReviewLoopGuardDecisionRealWorkResetsStreaks(t *testing.T) {
	dir := t.TempDir()
	session := "review-9"
	for n := 1; n <= 4; n++ {
		if _, denied := reviewLoopGuardDecision(probePayload(n), dir, session); denied {
			t.Fatalf("probe %d denied before the limit", n)
		}
	}
	real := []byte(`{"tool_name":"Bash","tool_input":{"command":"git diff --stat"}}`)
	if _, denied := reviewLoopGuardDecision(real, dir, session); denied {
		t.Fatalf("real work denied")
	}
	for n := 1; n <= 3; n++ {
		if _, denied := reviewLoopGuardDecision(probePayload(n), dir, session); denied {
			t.Fatalf("probe %d of the restarted streak denied before the limit", n)
		}
	}
}

func TestReviewLoopGuardDecisionSurvivesCorruptState(t *testing.T) {
	dir := t.TempDir()
	session := "review-10"
	path := filepath.Join(dir, "review-loops", session+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 5; n++ {
		if _, denied := reviewLoopGuardDecision(probePayload(n), dir, session); denied && n < 5 {
			t.Fatalf("probe %d denied with a corrupt state file", n)
		}
	}
}

func TestReviewLoopGuardReset(t *testing.T) {
	dir := t.TempDir()
	session := "review-11"
	for n := 1; n <= 4; n++ {
		reviewLoopGuardDecision(probePayload(n), dir, session)
	}
	if _, err := os.Stat(filepath.Join(dir, "review-loops", session+".json")); err != nil {
		t.Fatalf("state file missing after probes: %v", err)
	}
	reviewLoopGuardReset(dir, session)
	if _, err := os.Stat(filepath.Join(dir, "review-loops", session+".json")); !os.IsNotExist(err) {
		t.Fatalf("state file survived reset")
	}
	if _, denied := reviewLoopGuardDecision(probePayload(1), dir, session); denied {
		t.Fatalf("probe after reset denied")
	}
}

func TestHooks_ReviewerPreToolUseLoopGuardDeniesProbeStorm(t *testing.T) {
	t.Setenv("AO_REVIEW_SESSION_ID", "review-12")
	t.Setenv("AO_REVIEW_WORKER_SESSION_ID", "worker-12")
	t.Setenv("AO_REVIEW_HARNESS", "claude-code")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	probe := func(n int) string {
		return string(probePayload(n))
	}
	for n := 1; n <= 4; n++ {
		out, errOut, err := executeCLI(t, Deps{In: strings.NewReader(probe(n))}, "hooks", "claude-code", "pre-tool-use")
		if err != nil {
			t.Fatalf("probe %d: unexpected error: %v\nstderr=%s", n, err, errOut)
		}
		if out != "" {
			t.Fatalf("probe %d wrote a decision before the limit: %s", n, out)
		}
	}
	if capture.hits != 4 {
		t.Fatalf("pre-tool-use probes reported activity %d times, want 4", capture.hits)
	}
	out, errOut, err := executeCLI(t, Deps{In: strings.NewReader(probe(5))}, "hooks", "claude-code", "pre-tool-use")
	if err != nil {
		t.Fatalf("probe 5: unexpected error: %v\nstderr=%s", err, errOut)
	}
	var res claudePreToolUseHookOutput
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode guard denial: %v\nout=%s", err, out)
	}
	if res.HookSpecificOutput.HookEventName != "PreToolUse" || res.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("bad guard denial: %+v", res.HookSpecificOutput)
	}
	if capture.hits != 4 {
		t.Fatalf("denied call reported activity anyway: hits=%d", capture.hits)
	}
	if _, err := os.Stat(filepath.Join(cfg.dataDir, "review-loops", "review-12.json")); err != nil {
		t.Fatalf("guard state file missing: %v", err)
	}

	// The turn boundary clears the counters.
	if _, _, err := executeCLI(t, Deps{In: strings.NewReader("")}, "hooks", "claude-code", "stop"); err != nil {
		t.Fatalf("stop: unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.dataDir, "review-loops", "review-12.json")); !os.IsNotExist(err) {
		t.Fatalf("state file survived the turn boundary")
	}
	for n := 1; n <= 4; n++ {
		out, _, err := executeCLI(t, Deps{In: strings.NewReader(probe(n))}, "hooks", "claude-code", "pre-tool-use")
		if err != nil {
			t.Fatalf("post-reset probe %d: unexpected error: %v", n, err)
		}
		if out != "" {
			t.Fatalf("post-reset probe %d denied: %s", n, out)
		}
	}
}

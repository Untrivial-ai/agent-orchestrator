package sessionimport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// A minimal but shape-accurate Claude transcript.
const claudeTranscript = `{"type":"user","message":{"role":"user","content":"Fix the flaky login test"},"uuid":"u1","timestamp":"2026-08-20T10:00:00.000Z","sessionId":"11111111-1111-4111-8111-111111111111","cwd":"/Users/dev/project","gitBranch":"main"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Looking into it."}]},"uuid":"a1","timestamp":"2026-08-20T10:00:05.000Z"}
{"type":"ai-title","aiTitle":"Fix flaky login test","sessionId":"11111111-1111-4111-8111-111111111111"}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},"uuid":"u2","timestamp":"2026-08-20T10:01:00.000Z"}
`

// A minimal but shape-accurate Codex rollout: the root segment of a
// conversation, where the segment id equals the root session_id.
const codexRollout = `{"timestamp":"2026-08-21T09:00:00.000Z","type":"session_meta","payload":{"session_id":"019fbaf8-67a4-79b2-aa80-01283063aab8","id":"019fbaf8-67a4-79b2-aa80-01283063aab8","cwd":"/Users/dev/valence","originator":"codex-cli","git":{"branch":"feature"},"source":"vscode"}}
{"timestamp":"2026-08-21T09:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Add a health endpoint"}]}}
{"timestamp":"2026-08-21T09:00:09.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}}
`

// A later resume segment of the SAME conversation: a new segment id but the
// same root session_id. Discovery must fold this into one importable session
// and adopt this newer segment as the representative transcript.
const codexRolloutResume = `{"timestamp":"2026-08-21T09:30:00.000Z","type":"session_meta","payload":{"session_id":"019fbaf8-67a4-79b2-aa80-01283063aab8","id":"019fbfff-67a4-79b2-aa80-01283063aab8","cwd":"/Users/dev/valence","originator":"codex-cli","git":{"branch":"feature"},"source":"vscode"}}
{"timestamp":"2026-08-21T09:30:05.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Also add readiness"}]}}
`

// A Codex sub-agent rollout that discovery must skip.
const codexSubagentRollout = `{"timestamp":"2026-08-21T09:05:00.000Z","type":"session_meta","payload":{"id":"019fbbbb-67a4-79b2-aa80-01283063aab8","cwd":"/Users/dev/valence","source":{"subagent":{"thread_spawn":{"parent_thread_id":"019fbaf8-67a4-79b2-aa80-01283063aab8","depth":1}}}}}
{"timestamp":"2026-08-21T09:05:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"internal sub-agent prompt"}]}}
`

const codexSessionIndex = `{"id":"019fbaf8-67a4-79b2-aa80-01283063aab8","thread_name":"Valence health endpoint","updated_at":"2026-08-21T09:10:00.000Z"}
`

func buildFakeHome(t *testing.T) (claudeDir, codexHome string) {
	t.Helper()
	root := t.TempDir()
	claudeDir = filepath.Join(root, ".claude")
	codexHome = filepath.Join(root, ".codex")

	// Claude: <config>/projects/<slug>/<uuid>.jsonl
	writeFile(t, filepath.Join(claudeDir, "projects", "-Users-dev-project", "11111111-1111-4111-8111-111111111111.jsonl"), claudeTranscript)
	// A non-transcript file that must be ignored.
	writeFile(t, filepath.Join(claudeDir, "projects", "-Users-dev-project", "notes.txt"), "ignore me")

	// Codex: <home>/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl + index. Two
	// segments of one conversation, plus a sub-agent thread that must be skipped.
	writeFile(t, filepath.Join(codexHome, "sessions", "2026", "08", "21", "rollout-2026-08-21T09-00-00-019fbaf8-67a4-79b2-aa80-01283063aab8.jsonl"), codexRollout)
	writeFile(t, filepath.Join(codexHome, "sessions", "2026", "08", "21", "rollout-2026-08-21T09-30-00-019fbfff-67a4-79b2-aa80-01283063aab8.jsonl"), codexRolloutResume)
	writeFile(t, filepath.Join(codexHome, "sessions", "2026", "08", "21", "rollout-2026-08-21T09-05-00-019fbbbb-67a4-79b2-aa80-01283063aab8.jsonl"), codexSubagentRollout)
	writeFile(t, filepath.Join(codexHome, "session_index.jsonl"), codexSessionIndex)

	return claudeDir, codexHome
}

func TestDiscoverAcrossProviders(t *testing.T) {
	claudeDir, codexHome := buildFakeHome(t)

	svc := NewService(nil,
		NewClaudeSourceAt(claudeDir),
		NewCodexSourceAt(codexHome, true),
	)

	got, err := svc.Discover(context.Background(), DiscoverOptions{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 importable sessions (sub-agent skipped), got %d: %+v", len(got), got)
	}

	// Newest first: Codex (Aug 21) before Claude (Aug 20).
	codex, claude := got[0], got[1]
	if codex.Provider != domain.HarnessCodex || claude.Provider != domain.HarnessClaudeCode {
		t.Fatalf("wrong ordering/providers: %s then %s", codex.Provider, claude.Provider)
	}

	if claude.NativeSessionID != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("claude native id: got %q", claude.NativeSessionID)
	}
	if claude.CWD != "/Users/dev/project" {
		t.Errorf("claude cwd: got %q", claude.CWD)
	}
	if claude.Branch != "main" {
		t.Errorf("claude branch: got %q", claude.Branch)
	}
	if claude.Title != "Fix flaky login test" {
		t.Errorf("claude title (want ai-title): got %q", claude.Title)
	}
	if claude.MessageCount != 3 {
		t.Errorf("claude message count: got %d, want 3", claude.MessageCount)
	}

	// Bound to the root conversation id, not a per-segment id.
	if codex.NativeSessionID != "019fbaf8-67a4-79b2-aa80-01283063aab8" {
		t.Errorf("codex native id (want root session_id): got %q", codex.NativeSessionID)
	}
	if codex.CWD != "/Users/dev/valence" {
		t.Errorf("codex cwd: got %q", codex.CWD)
	}
	if codex.Title != "Valence health endpoint" {
		t.Errorf("codex title (want index name): got %q", codex.Title)
	}
	if codex.MessageCount != 2 {
		t.Errorf("codex message count (max segment): got %d, want 2", codex.MessageCount)
	}
	// The two segments collapse into one importable session, represented by the
	// newer resume segment.
	if !strings.HasSuffix(codex.TranscriptPath, "019fbfff-67a4-79b2-aa80-01283063aab8.jsonl") {
		t.Errorf("codex transcript should be the latest segment: got %s", codex.TranscriptPath)
	}
	if !codex.LastActivity.Equal(parseTime("2026-08-21T09:30:05.000Z")) {
		t.Errorf("codex last activity should be the latest segment's last line: got %s", codex.LastActivity)
	}
}

func TestDiscoverSinceFilter(t *testing.T) {
	claudeDir, codexHome := buildFakeHome(t)
	svc := NewService(nil, NewClaudeSourceAt(claudeDir), NewCodexSourceAt(codexHome, true))

	// Only keep activity on/after Aug 21 -> drops the Claude (Aug 20) session.
	got, err := svc.Discover(context.Background(), DiscoverOptions{Since: parseTime("2026-08-21T00:00:00.000Z")})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 1 || got[0].Provider != domain.HarnessCodex {
		t.Fatalf("since filter should keep only the Codex session, got %+v", got)
	}
}

func TestDiscoverFlagsAlreadyImported(t *testing.T) {
	claudeDir, codexHome := buildFakeHome(t)
	existing := func(context.Context) (map[string]struct{}, error) {
		return map[string]struct{}{"11111111-1111-4111-8111-111111111111": {}}, nil
	}
	svc := NewService(existing, NewClaudeSourceAt(claudeDir), NewCodexSourceAt(codexHome, true))

	got, err := svc.Discover(context.Background(), DiscoverOptions{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	for _, s := range got {
		want := s.NativeSessionID == "11111111-1111-4111-8111-111111111111"
		if s.AlreadyImported != want {
			t.Errorf("%s AlreadyImported=%v, want %v", s.NativeSessionID, s.AlreadyImported, want)
		}
	}
}

func TestLocatePopulatesMetadata(t *testing.T) {
	claudeDir, codexHome := buildFakeHome(t)
	svc := NewService(nil, NewClaudeSourceAt(claudeDir), NewCodexSourceAt(codexHome, true))

	// Locate must normalize scan options; a zero MaxScanBytes would read nothing
	// and leave cwd empty, which breaks project resolution on import.
	got, ok, err := svc.Locate(context.Background(), domain.HarnessClaudeCode, "11111111-1111-4111-8111-111111111111")
	if err != nil || !ok {
		t.Fatalf("locate: ok=%v err=%v", ok, err)
	}
	if got.CWD != "/Users/dev/project" {
		t.Errorf("located cwd empty/wrong: %q", got.CWD)
	}
	if got.Title == "" {
		t.Error("located title should be populated")
	}
}

func TestCodexIDFromFileName(t *testing.T) {
	cases := map[string]string{
		"rollout-2026-08-01T07-07-49-019fbaf8-67a4-79b2-aa80-01283063aab8.jsonl": "019fbaf8-67a4-79b2-aa80-01283063aab8",
		"rollout-2026-08-01T07-07-49-not-a-uuid.jsonl":                           "",
		"random.jsonl": "",
	}
	for name, want := range cases {
		if got := codexIDFromFileName(name); got != want {
			t.Errorf("codexIDFromFileName(%q)=%q, want %q", name, got, want)
		}
	}
}

// TestDiscoverRealHome is a manual, opt-in run against the developer's actual
// ~/.claude and ~/.codex history. It is skipped by default; run with
// AO_IMPORT_SCAN_REAL=1 to see what the importer would list.
func TestDiscoverRealHome(t *testing.T) {
	if os.Getenv("AO_IMPORT_SCAN_REAL") == "" {
		t.Skip("set AO_IMPORT_SCAN_REAL=1 to scan the real home directory")
	}
	svc := NewService(nil, NewClaudeSource(), NewCodexSource())
	opts := DiscoverOptions{Since: time.Now().AddDate(0, 0, -60)}

	// Everything on disk, so the run shows what is withheld as well as kept.
	all, err := svc.Discover(context.Background(), withTrivial(opts))
	if err != nil {
		t.Logf("discover returned partial error: %v", err)
	}
	kept, err := svc.Discover(context.Background(), opts)
	if err != nil {
		t.Logf("discover returned partial error: %v", err)
	}

	counts := map[Meaning]int{}
	for _, s := range all {
		counts[s.Meaning]++
	}
	t.Logf("scanned %d conversations in the last 60 days: %d meaningful, %d ambiguous, %d trivial (withheld)",
		len(all), counts[MeaningMeaningful], counts[MeaningAmbiguous], counts[MeaningTrivial])
	t.Logf("%d would be listed", len(kept))

	for i, s := range kept {
		if i >= 25 {
			t.Logf("... and %d more", len(kept)-25)
			break
		}
		t.Logf("%2d. [%-11s] %-10s msgs=%-4d %s\n      cwd=%s branch=%s\n      title=%s",
			i+1, s.Provider, s.Meaning, s.MessageCount,
			s.LastActivity.Format("2006-01-02 15:04"), s.CWD, s.Branch, s.Title)
	}

	shown := 0
	for _, s := range all {
		if s.Meaning != MeaningTrivial || shown >= 15 {
			continue
		}
		shown++
		t.Logf("withheld: [%s] msgs=%-3d title=%q", s.Provider, s.MessageCount, s.Title)
	}
}

func withTrivial(opts DiscoverOptions) DiscoverOptions {
	opts.IncludeTrivial = true
	return opts
}

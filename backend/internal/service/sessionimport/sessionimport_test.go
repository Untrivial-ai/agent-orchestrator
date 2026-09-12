package sessionimport

import (
	"context"
	"errors"
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
		return map[string]struct{}{NativeKey(domain.HarnessClaudeCode, "11111111-1111-4111-8111-111111111111"): {}}, nil
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

// Manual latency check: AO_IMPORT_SCAN_REAL is an explicit opt-in to local
// metadata reads. Only durations and counts are logged, never conversation text.
func TestDiscoverRealHome(t *testing.T) {
	project := os.Getenv("AO_IMPORT_SCAN_PROJECT")
	if project == "" {
		t.Skip("set AO_IMPORT_SCAN_PROJECT to a project path")
	}
	svc := NewService(nil, NewClaudeSource(), NewCodexSource())
	opts := DiscoverOptions{Since: time.Now().AddDate(0, 0, -15), MinTokens: 15000, IncludeCWD: func(cwd string) bool { return cwd == project }}
	for i := 0; i < 3; i++ {
		start := time.Now()
		found, err := svc.Discover(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("scan %d: %d sessions in %s", i, len(found), time.Since(start))
	}
}

func TestImportedIdentityDoesNotCrossProviders(t *testing.T) {
	svc := NewService(func(context.Context) (map[string]struct{}, error) {
		return map[string]struct{}{"codex:shared": {}}, nil
	})
	sessions := []ImportableSession{
		{Provider: domain.HarnessCodex, NativeSessionID: "shared"},
		{Provider: domain.HarnessClaudeCode, NativeSessionID: "shared"},
	}
	if err := svc.flagImported(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if !sessions[0].AlreadyImported || sessions[1].AlreadyImported {
		t.Fatalf("incorrect provider import markers: %+v", sessions)
	}
}

type coordinatedSource struct {
	provider domain.AgentHarness
	run      func(context.Context) ([]ImportableSession, error)
}

func (s coordinatedSource) Provider() domain.AgentHarness { return s.provider }
func (s coordinatedSource) Discover(ctx context.Context, _ DiscoverOptions) ([]ImportableSession, error) {
	return s.run(ctx)
}
func TestDiscoveryProvidersMakeIndependentProgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	secondStarted := make(chan struct{})
	first := coordinatedSource{domain.HarnessCodex, func(ctx context.Context) ([]ImportableSession, error) {
		select {
		case <-secondStarted:
			return []ImportableSession{{Provider: domain.HarnessCodex, NativeSessionID: "one"}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	second := coordinatedSource{domain.HarnessClaudeCode, func(context.Context) ([]ImportableSession, error) {
		close(secondStarted)
		return []ImportableSession{{Provider: domain.HarnessClaudeCode, NativeSessionID: "two"}}, nil
	}}
	got, err := NewService(nil, first, second).Discover(ctx, DiscoverOptions{})
	if err != nil || len(got) != 2 {
		t.Fatalf("one provider blocked the other: count=%d err=%v", len(got), err)
	}
}

func TestDiscoveryCancellationJoinsProviderScans(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 2)
	exited := make(chan struct{}, 2)
	run := func(ctx context.Context) ([]ImportableSession, error) {
		started <- struct{}{}
		<-ctx.Done()
		exited <- struct{}{}
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, err := NewService(nil, coordinatedSource{domain.HarnessCodex, run}, coordinatedSource{domain.HarnessClaudeCode, run}).Discover(ctx, DiscoverOptions{})
		done <- err
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("provider did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scan did not cancel")
	}
	if len(exited) != 2 {
		t.Fatal("discovery returned before provider cleanup")
	}
}
func TestDiscoveryPreservesOtherProviderResultsOnError(t *testing.T) {
	failed := errors.New("unreadable history")
	svc := NewService(nil, coordinatedSource{domain.HarnessCodex, func(context.Context) ([]ImportableSession, error) { return nil, failed }}, coordinatedSource{domain.HarnessClaudeCode, func(context.Context) ([]ImportableSession, error) {
		return []ImportableSession{{Provider: domain.HarnessClaudeCode, NativeSessionID: "kept"}}, nil
	}})
	got, err := svc.Discover(context.Background(), DiscoverOptions{})
	if !errors.Is(err, failed) || len(got) != 1 || got[0].NativeSessionID != "kept" {
		t.Fatalf("partial results lost: %+v %v", got, err)
	}
}

func TestTranscriptStatFailureIsNotAnEmptyHistory(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	writeFile(t, file, "not a directory")
	path := filepath.Join(file, "session.jsonl")
	if _, _, err := NewClaudeSourceAt(root).readSession(context.Background(), root, path, "session.jsonl", DiscoverOptions{}); err == nil {
		t.Fatal("Claude stat failure was hidden")
	}
	if _, _, err := NewCodexSourceAt(root, false).readSegment(context.Background(), path, DiscoverOptions{}); err == nil {
		t.Fatal("Codex stat failure was hidden")
	}
	missing := filepath.Join(root, "missing.jsonl")
	if _, ok, err := NewClaudeSourceAt(root).readSession(context.Background(), root, missing, "missing.jsonl", DiscoverOptions{}); err != nil || ok {
		t.Fatalf("removed Claude file: %v %v", ok, err)
	}
	if _, ok, err := NewCodexSourceAt(root, false).readSegment(context.Background(), missing, DiscoverOptions{}); err != nil || ok {
		t.Fatalf("removed Codex file: %v %v", ok, err)
	}
}

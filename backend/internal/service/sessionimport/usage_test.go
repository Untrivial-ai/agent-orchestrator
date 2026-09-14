package sessionimport

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageThresholdAndDuplicateCounters(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, count := range []int64{14999, 15000} {
			t.Run(fmt.Sprintf("%s-%d", provider, count), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "usage.jsonl")
				line := fmt.Sprintf(`{"type":"assistant","message":{"id":"msg1","usage":{"input_tokens":1000,"output_tokens":999,"cache_read_input_tokens":%d,"cache_creation_input_tokens":1}}}`, count-2000)
				if provider == "codex" {
					line = fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":10000,"output_tokens":1000,"total_tokens":%d}}}}`, count-1000, count)
				}
				writeFile(t, path, line+"\n"+line+"\n")
				got, _, err := scanUsage(context.Background(), path, provider == "codex", 0)
				if err != nil || got != count {
					t.Fatalf("usage = %d, %v; want %d (do not double count cache or repeated events)", got, err, count)
				}
			})
		}
	}
}

func TestDiscoveryRefreshesChangedUsageAndScope(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "projects", "-project", "11111111-1111-4111-8111-111111111111.jsonl")
	usage := func(n int) string {
		return fmt.Sprintf(`{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":%d}}}`+"\n", n)
	}
	writeFile(t, path, claudeTranscript+usage(14999))
	svc := NewService(nil, NewClaudeSourceAt(home))
	opts := DiscoverOptions{MinTokens: 15000, IncludeCWD: func(cwd string) bool { return cwd == "/Users/dev/project" }}
	rows, err := svc.Discover(context.Background(), opts)
	if err != nil || len(rows) != 0 {
		t.Fatalf("under cutoff: %v, %v", rows, err)
	}
	writeFile(t, path, claudeTranscript+usage(15000)+"\n")
	rows, err = svc.Discover(context.Background(), opts)
	if err != nil || len(rows) != 1 || rows[0].TokenCount != 15000 {
		t.Fatalf("updated usage was not discovered: %v, %v", rows, err)
	}
	opts.IncludeCWD = func(string) bool { return false }
	rows, err = svc.Discover(context.Background(), opts)
	if err != nil || len(rows) != 0 {
		t.Fatalf("cache leaked another project's session: %v, %v", rows, err)
	}
	opts.IncludeCWD = nil
	opts.Since = parseTime("2026-08-21T00:00:00Z")
	rows, err = svc.Discover(context.Background(), opts)
	if err != nil || len(rows) != 0 {
		t.Fatalf("cache bypassed activity cutoff: %v, %v", rows, err)
	}
}

func TestRecentResumeRetainsOlderCumulativeUsage(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions")
	usage := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":15000}}}}` + "\n"
	writeFile(t, filepath.Join(dir, "rollout-old.jsonl"), codexRollout+usage)
	writeFile(t, filepath.Join(dir, "rollout-new.jsonl"), strings.ReplaceAll(codexRolloutResume, "2026-08-21", "2026-09-01"))
	svc := NewService(nil, NewCodexSourceAt(home, false))
	rows, err := svc.Discover(context.Background(), DiscoverOptions{Since: parseTime("2026-08-25T00:00:00Z"), MinTokens: 15000})
	if err != nil || len(rows) != 1 || rows[0].TokenCount != 15000 {
		t.Fatalf("recent resumed conversation lost prior usage: %v, %v", rows, err)
	}
}

func TestUsageReadCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	writeFile(t, path, claudeTranscript)
	if _, _, err := scanUsage(ctx, path, false, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
}

func TestUsageAfterOversizedToolOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	writeFile(t, path, strings.Repeat("x", maxScanLineBytes+1)+"\n"+`{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":15000}}}`)
	got, _, err := scanUsage(context.Background(), path, false, 0)
	if err != nil || got != 15000 {
		t.Fatalf("large output hid later usage: %d, %v", got, err)
	}
}

func TestLocateRefreshesCodexSegmentInventory(t *testing.T) {
	for _, evict := range []bool{false, true} {
		t.Run(fmt.Sprintf("evicted-%t", evict), func(t *testing.T) {
			home := t.TempDir()
			oldPath := filepath.Join(home, "sessions", "rollout-old.jsonl")
			newPath := filepath.Join(home, "sessions", "rollout-new.jsonl")
			usage := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":15000}}}}` + "\n"
			writeFile(t, oldPath, codexRollout+usage)
			source := NewCodexSourceAt(home, false)
			svc := NewService(nil, source)
			rows, err := svc.Discover(context.Background(), DiscoverOptions{})
			if err != nil || len(rows) != 1 {
				t.Fatalf("discover: %v, %v", rows, err)
			}
			writeFile(t, newPath, codexRolloutResume)
			if evict {
				source.cache.entries = nil
				if _, ok, err := source.readSegment(context.Background(), newPath, DiscoverOptions{}.normalized()); err != nil || !ok {
					t.Fatalf("seed: %t, %v", ok, err)
				}
			}
			got, ok, err := svc.Locate(context.Background(), source.Provider(), rows[0].NativeSessionID, DiscoverOptions{})
			if err != nil || !ok || got.TranscriptPath != newPath || got.TokenCount != 15000 {
				t.Fatalf("locate must refresh all segments: %+v, %t, %v", got, ok, err)
			}
		})
	}
}

func TestUsageDoesNotCacheOldMetadataWithNewFingerprint(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "sessions", "rollout-old.jsonl")
	writeFile(t, path, codexRollout)
	source := NewCodexSourceAt(home, false)
	opts := DiscoverOptions{}.normalized()
	seg, ok, err := source.readSegment(context.Background(), path, opts)
	if err != nil || !ok {
		t.Fatalf("metadata: %t, %v", ok, err)
	}
	writeFile(t, path, codexRollout+`{"timestamp":"2026-09-08T10:00:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":15000}}}}`+"\n")
	if _, err := source.segmentUsage(context.Background(), seg, 0); err != nil {
		t.Fatal(err)
	}
	got, ok, err := source.readSegment(context.Background(), path, opts)
	if err != nil || !ok || !got.lastActivity.Equal(parseTime("2026-09-08T10:00:00Z")) {
		t.Fatalf("new fingerprint preserved stale metadata: %+v, %t, %v", got, ok, err)
	}
}

func TestCodexScopeUsesLatestSegmentAndWholeConversationUsage(t *testing.T) {
	home := t.TempDir()
	usage := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":15000}}}}` + "\n"
	writeFile(t, filepath.Join(home, "sessions", "rollout-old.jsonl"), strings.ReplaceAll(codexRollout, "/Users/dev/valence", "/project-a")+usage)
	writeFile(t, filepath.Join(home, "sessions", "rollout-new.jsonl"), strings.ReplaceAll(codexRolloutResume, "/Users/dev/valence", "/project-b"))
	source := NewCodexSourceAt(home, false)
	svc := NewService(nil, source)
	id := "019fbaf8-67a4-79b2-aa80-01283063aab8"
	for _, cwd := range []string{"/project-a", "/project-b"} {
		t.Run(cwd, func(t *testing.T) {
			opts := DiscoverOptions{MinTokens: 15000, IncludeCWD: func(value string) bool { return value == cwd }}
			rows, err := svc.Discover(context.Background(), opts)
			want := 0
			if cwd == "/project-b" {
				want = 1
			}
			if err != nil || len(rows) != want {
				t.Errorf("discovery for %s: %+v, %v; want %d", cwd, rows, err, want)
			}
			got, ok, err := svc.Locate(context.Background(), source.Provider(), id, opts)
			if err != nil || ok != (want == 1) {
				t.Errorf("locate for %s: %+v, %t, %v", cwd, got, ok, err)
			}
			if ok && (got.CWD != "/project-b" || got.TokenCount != 15000) {
				t.Errorf("lost newest scope or prior usage: %+v", got)
			}
		})
	}
}

func TestThresholdUsageFallsBackWhenCodexTailCounterDecreases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	event := func(n int) string {
		return fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":%d}}}}`+"\n", n)
	}
	writeFile(t, path, event(20000)+strings.Repeat("x", int(defaultMaxScanBytes)+1)+"\n"+event(100))
	got, complete, err := scanUsage(context.Background(), path, true, 15000)
	if err != nil || got != 20000 || complete {
		t.Fatalf("low tail lost earlier qualifying usage: %d, %t, %v", got, complete, err)
	}
}

func TestThresholdUsageCodexTailAfterOversizedOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeFile(t, path, strings.Repeat("x", maxScanLineBytes+1)+"\n"+`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":17000}}}}`)
	got, complete, err := scanUsage(context.Background(), path, true, 15000)
	if err != nil || got != 17000 || complete {
		t.Fatalf("tail usage: %d, %t, %v", got, complete, err)
	}
}

func TestThresholdUsageCachesRespectHigherAndExactRequests(t *testing.T) {
	for _, codex := range []bool{false, true} {
		t.Run(fmt.Sprintf("codex-%t", codex), func(t *testing.T) {
			home := t.TempDir()
			var source Source
			if codex {
				event := func(n int) string {
					return fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":%d}}}}`+"\n", n)
				}
				// A lower later counter makes the tail prove 15k while an earlier event
				// remains necessary to establish a higher threshold and exact maximum.
				writeFile(t, filepath.Join(home, "sessions", "rollout-one.jsonl"), codexRollout+event(30000)+strings.Repeat("x", int(defaultMaxScanBytes)+1)+"\n"+event(15000))
				source = NewCodexSourceAt(home, false)
			} else {
				event := func(id string, n int) string {
					return fmt.Sprintf(`{"type":"assistant","message":{"id":%q,"usage":{"input_tokens":%d}}}`+"\n", id, n)
				}
				writeFile(t, filepath.Join(home, "projects", "p", "11111111-1111-4111-8111-111111111111.jsonl"), claudeTranscript+event("first", 15000)+event("second", 15000))
				source = NewClaudeSourceAt(home)
			}
			svc := NewService(nil, source)
			for _, threshold := range []int64{15000, 25000, 0} {
				got, err := svc.Discover(context.Background(), DiscoverOptions{MinTokens: threshold})
				want := int64(30000)
				if threshold == 15000 {
					want = 15000
				}
				if err != nil || len(got) != 1 || got[0].TokenCount != want {
					t.Fatalf("threshold %d: %+v, %v; want %d", threshold, got, err, want)
				}
			}
		})
	}
}

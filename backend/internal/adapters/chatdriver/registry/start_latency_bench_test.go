package registry

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Opt-in Chat driver Start latency bench across every shipped chat harness.
// Skipped in ordinary CI / go test.
//
// Measures only driver.Start (chat-host + provider initialize/session-new).
// Harnesses whose Probe fails (missing binary / auth) are skipped, not failed.
//
//	AO_PERF_BENCH=1 go test ./internal/adapters/chatdriver/registry/ \
//	  -run 'TestBenchChatDriverStart$' -v -count=1 -timeout 45m
//
// Optional: AO_PERF_REPEAT=3, AO_PERF_DIR, AO_PERF_LABEL,
// AO_PERF_HARNESS=cursor,claude-code (comma list; default = all registered).
func TestBenchChatDriverStart(t *testing.T) {
	requirePerfBench(t)

	reg := Build(nil)
	harnesses := selectedHarnesses(t, reg.Harnesses())
	if len(harnesses) == 0 {
		t.Fatal("no chat harnesses selected")
	}

	repeats := perfRepeat()
	summaries := make([]map[string]any, 0, len(harnesses))
	for _, harness := range harnesses {
		driver, err := reg.Driver(harness)
		if err != nil {
			t.Fatalf("driver %s: %v", harness, err)
		}
		summary := benchOneChatDriver(t, harness, driver, repeats)
		if summary != nil {
			summaries = append(summaries, summary)
		}
	}

	writePerfRecord(t, "chat-driver-start-all", map[string]any{
		"metric":      "chat_driver_start_by_harness",
		"description": "driver.Start cold start per shipped chat harness (Probe-gated)",
		"excludes":    []string{"git fetch", "git worktree", "daemon HTTP", "first prompt turn"},
		"harnesses":   summaries,
	})
}

func benchOneChatDriver(t *testing.T, harness domain.AgentHarness, driver ports.ChatDriver, repeats int) map[string]any {
	t.Helper()
	return runNamed(t, string(harness), func(t *testing.T) map[string]any {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := driver.Probe(ctx); err != nil {
			t.Skipf("Probe %s: %v", harness, err)
		}

		samples := make([]time.Duration, 0, repeats)
		for i := 0; i < repeats; i++ {
			elapsed := timeChatDriverStart(t, harness, driver, i)
			samples = append(samples, elapsed)
			t.Logf("%s run %d/%d: start=%.3fs", harness, i+1, repeats, elapsed.Seconds())
		}
		summary := map[string]any{
			"harness":   string(harness),
			"samplesMs": durationsMs(samples),
			"medianMs":  medianMs(samples),
			"minMs":     minMs(samples),
			"maxMs":     maxMs(samples),
		}
		writePerfRecord(t, "chat-driver-start-"+string(harness), map[string]any{
			"metric":      "chat_driver_start_ms",
			"harness":     string(harness),
			"samplesMs":   durationsMs(samples),
			"medianMs":    medianMs(samples),
			"minMs":       minMs(samples),
			"maxMs":       maxMs(samples),
			"description": "driver.Start for " + string(harness),
			"excludes":    []string{"git fetch", "git worktree", "daemon HTTP", "first prompt turn"},
		})
		t.Logf("%s median start=%.3fs (n=%d)", harness, medianMs(samples)/1000, repeats)
		return summary
	})
}

func runNamed(t *testing.T, name string, fn func(*testing.T) map[string]any) map[string]any {
	t.Helper()
	var summary map[string]any
	t.Run(name, func(t *testing.T) {
		summary = fn(t)
	})
	return summary
}

func timeChatDriverStart(t *testing.T, harness domain.AgentHarness, driver ports.ChatDriver, run int) time.Duration {
	t.Helper()
	dataDir := t.TempDir()
	workspace := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sessionID := domain.SessionID("perf-" + string(harness) + "-" + strconv.Itoa(run) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36))
	start := time.Now()
	conv, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID:     sessionID,
		DataDir:       dataDir,
		WorkspacePath: workspace,
		Env:           liveEnvMap(),
		Permissions:   ports.PermissionModeBypassPermissions,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Start %s run %d: %v", harness, run, err)
	}
	if conv.ProviderConversationID() == "" {
		t.Fatalf("Start %s run %d: empty provider conversation id", harness, run)
	}
	if term, ok := conv.(ports.ChatProviderTerminator); ok {
		_ = term.Terminate()
	}
	return elapsed
}

func selectedHarnesses(t *testing.T, all []domain.AgentHarness) []domain.AgentHarness {
	t.Helper()
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	filter := strings.TrimSpace(os.Getenv("AO_PERF_HARNESS"))
	if filter == "" || filter == "all" {
		return all
	}
	want := map[string]struct{}{}
	for _, part := range strings.Split(filter, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			want[part] = struct{}{}
		}
	}
	out := make([]domain.AgentHarness, 0, len(want))
	for _, harness := range all {
		if _, ok := want[string(harness)]; ok {
			out = append(out, harness)
		}
	}
	if len(out) == 0 {
		t.Fatalf("AO_PERF_HARNESS=%q matched none of %v", filter, all)
	}
	return out
}

func liveEnvMap() map[string]string {
	out := make(map[string]string)
	for _, pair := range os.Environ() {
		name, value, ok := strings.Cut(pair, "=")
		if ok {
			out[name] = value
		}
	}
	return out
}

func requirePerfBench(t *testing.T) {
	t.Helper()
	if os.Getenv("AO_PERF_BENCH") != "1" {
		t.Skip("set AO_PERF_BENCH=1 to run spawn/ACP latency benches (opt-in; not CI)")
	}
}

func perfRepeat() int {
	n, err := strconv.Atoi(os.Getenv("AO_PERF_REPEAT"))
	if err != nil || n < 1 {
		return 3
	}
	return n
}

func durationsMs(samples []time.Duration) []float64 {
	out := make([]float64, len(samples))
	for i, sample := range samples {
		out[i] = float64(sample.Milliseconds())
	}
	return out
}

func medianMs(samples []time.Duration) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return float64(sorted[mid].Milliseconds())
	}
	return float64((sorted[mid-1] + sorted[mid]).Milliseconds()) / 2
}

func minMs(samples []time.Duration) float64 {
	if len(samples) == 0 {
		return 0
	}
	best := samples[0]
	for _, sample := range samples[1:] {
		if sample < best {
			best = sample
		}
	}
	return float64(best.Milliseconds())
}

func maxMs(samples []time.Duration) float64 {
	if len(samples) == 0 {
		return 0
	}
	best := samples[0]
	for _, sample := range samples[1:] {
		if sample > best {
			best = sample
		}
	}
	return float64(best.Milliseconds())
}

func writePerfRecord(t *testing.T, workload string, result map[string]any) {
	t.Helper()
	label := os.Getenv("AO_PERF_LABEL")
	if label == "" {
		label = "sample"
	}
	commit := "unknown"
	if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		commit = strings.TrimSpace(string(out))
	}
	record := map[string]any{
		"label":     label,
		"commit":    commit,
		"workload":  workload,
		"suite":     "spawn-latency",
		"go":        runtime.Version(),
		"platform":  runtime.GOOS,
		"arch":      runtime.GOARCH,
		"cpus":      runtime.NumCPU(),
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"result":    result,
	}
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatalf("marshal perf record: %v", err)
	}
	t.Logf("perf record:\n%s", body)

	dir := os.Getenv("AO_PERF_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir AO_PERF_DIR: %v", err)
	}
	path := filepath.Join(dir, label+"-"+workload+".json")
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Logf("wrote %s", path)
}

//go:build !windows

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	chatregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Opt-in spawn latency benches across shipped agent harnesses.
// Skipped in ordinary CI / go test.
//
// Boots a real daemon against a throwaway git fixture (or AO_PERF_REPO) and
// times POST /sessions until the session exists. Missing binaries are skipped.
//
//	AO_PERF_BENCH=1 go test ./e2e/ -run 'TestBenchSpawn' -v -count=1 -timeout 90m
//
// - spawn-chat-empty: every chat-capable harness (chatdriver registry)
// - spawn-tui-empty: every shipped agent harness (agent registry)
//
// Optional: AO_PERF_REPEAT=3, AO_PERF_DIR, AO_PERF_LABEL, AO_PERF_REPO,
// AO_PERF_HARNESS=cursor,claude-code (comma list; default = full matrix).
func TestBenchSpawnChatEmptyPrompt(t *testing.T) {
	requirePerfBenchEnv(t)
	runSpawnLatencyMatrix(t, "spawn-chat-empty", selectedChatHarnesses(t), map[string]any{
		"mode":   "chat",
		"prompt": "",
	})
}

func TestBenchSpawnTUIEmptyPrompt(t *testing.T) {
	requirePerfBenchEnv(t)
	runSpawnLatencyMatrix(t, "spawn-tui-empty", selectedAllHarnesses(t), map[string]any{
		"mode":   "tui",
		"prompt": "",
	})
}

func TestBenchGitWorktreeAdd(t *testing.T) {
	requirePerfBenchEnv(t)

	repeats := spawnPerfRepeat()
	repo, cleanup := resolvePerfRepo(t)
	if cleanup != nil {
		defer cleanup()
	}

	samples := make([]time.Duration, 0, repeats)
	for i := 0; i < repeats; i++ {
		branch := fmt.Sprintf("ao/perf-wt-%d-%d", time.Now().UnixNano(), i)
		path := filepath.Join(t.TempDir(), fmt.Sprintf("wt-%d", i))
		start := time.Now()
		cmd := exec.Command("git", "-C", repo, "worktree", "add", "-b", branch, path, "HEAD")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git worktree add: %v\n%s", err, out)
		}
		elapsed := time.Since(start)
		samples = append(samples, elapsed)
		t.Logf("run %d/%d: git_worktree_add=%.3fs repo=%s", i+1, repeats, elapsed.Seconds(), repo)

		_ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", path).Run()
		_ = exec.Command("git", "-C", repo, "branch", "-D", branch).Run()
	}

	writeSpawnPerfRecord(t, "git-worktree-add", map[string]any{
		"metric":      "git_worktree_add_ms",
		"repo":        repo,
		"samplesMs":   durationsToMs(samples),
		"medianMs":    medianDurationMs(samples),
		"minMs":       minDurationMs(samples),
		"maxMs":       maxDurationMs(samples),
		"description": "git worktree add -b <branch> <path> HEAD (no AO daemon)",
	})
}

func runSpawnLatencyMatrix(t *testing.T, workload string, harnesses []string, spawnFields map[string]any) {
	t.Helper()
	if len(harnesses) == 0 {
		t.Fatal("no harnesses selected")
	}
	dataDir := t.TempDir()
	d := startDaemon(t, dataDir)

	var projectID string
	if repo := os.Getenv("AO_PERF_REPO"); repo != "" {
		projectID = registerPerfRepo(t, d, repo)
	} else {
		projectID = seedProject(t, d, "perf-spawn")
	}
	setPermissions(t, d, projectID, "bypass-permissions")

	summaries := make([]map[string]any, 0, len(harnesses))
	for _, harness := range harnesses {
		summary := runSpawnLatencyBench(t, d, projectID, harness, workload, spawnFields)
		if summary != nil {
			summaries = append(summaries, summary)
		}
	}
	writeSpawnPerfRecord(t, workload+"-all", map[string]any{
		"metric":      workload + "_by_harness",
		"mode":        spawnFields["mode"],
		"promptEmpty": spawnFields["prompt"] == "",
		"repoEnv":     os.Getenv("AO_PERF_REPO"),
		"harnesses":   summaries,
		"description": "POST /api/v1/sessions per harness (missing binaries skipped)",
	})
}

func runSpawnLatencyBench(
	t *testing.T,
	d *daemon,
	projectID, harness, workload string,
	spawnFields map[string]any,
) map[string]any {
	t.Helper()
	var summary map[string]any
	t.Run(harness, func(t *testing.T) {
		if err := requireHarnessBinary(t, harness); err != nil {
			t.Skipf("%s: %v", harness, err)
		}

		repeats := spawnPerfRepeat()
		samples := make([]time.Duration, 0, repeats)
		sessionIDs := make([]string, 0, repeats)
		for i := 0; i < repeats; i++ {
			body := map[string]any{
				"projectId":   projectID,
				"kind":        "worker",
				"harness":     harness,
				"displayName": fmt.Sprintf("perf-%d", i), // API cap is 20 runes
			}
			for k, v := range spawnFields {
				body[k] = v
			}
			start := time.Now()
			var out spawned
			var errBody map[string]any
			status, raw, err := d.callTimedRaw(90*time.Second, "POST", "/sessions", body)
			elapsed := time.Since(start)
			if err == nil && len(raw) > 0 {
				_ = json.Unmarshal(raw, &out)
				_ = json.Unmarshal(raw, &errBody)
			}
			if err != nil || status != http.StatusCreated || out.Session.ID == "" {
				t.Fatalf("spawn %s run %d: status=%d err=%v out=%+v body=%v\n%s",
					harness, i, status, err, out, errBody, d.tailLog())
			}
			samples = append(samples, elapsed)
			sessionIDs = append(sessionIDs, out.Session.ID)
			t.Logf("%s run %d/%d: %s=%.3fs session=%s mode=%s",
				harness, i+1, repeats, workload, elapsed.Seconds(), out.Session.ID, out.Session.Mode)
			_, _ = d.call("POST", "/sessions/"+out.Session.ID+"/kill", nil, nil)
		}

		summary = map[string]any{
			"harness":   harness,
			"samplesMs": durationsToMs(samples),
			"medianMs":  medianDurationMs(samples),
			"minMs":     minDurationMs(samples),
			"maxMs":     maxDurationMs(samples),
			"sessions":  sessionIDs,
		}
		writeSpawnPerfRecord(t, workload+"-"+harness, map[string]any{
			"metric":      workload + "_ms",
			"harness":     harness,
			"mode":        spawnFields["mode"],
			"promptEmpty": spawnFields["prompt"] == "",
			"repoEnv":     os.Getenv("AO_PERF_REPO"),
			"samplesMs":   durationsToMs(samples),
			"medianMs":    medianDurationMs(samples),
			"minMs":       minDurationMs(samples),
			"maxMs":       maxDurationMs(samples),
			"sessions":    sessionIDs,
			"description": "POST /api/v1/sessions until session row is created (full Spawn)",
		})
		t.Logf("%s median %s=%.3fs (n=%d)", harness, workload, medianDurationMs(samples)/1000, repeats)
	})
	return summary
}

func selectedChatHarnesses(t *testing.T) []string {
	t.Helper()
	reg := chatregistry.Build(nil)
	all := make([]string, 0, len(reg.Harnesses()))
	for _, harness := range reg.Harnesses() {
		all = append(all, string(harness))
	}
	return filterHarnesses(t, all)
}

func selectedAllHarnesses(t *testing.T) []string {
	t.Helper()
	all := make([]string, 0, len(agentregistry.Harnessed()))
	for _, ha := range agentregistry.Harnessed() {
		all = append(all, string(ha.Harness))
	}
	return filterHarnesses(t, all)
}

func filterHarnesses(t *testing.T, all []string) []string {
	t.Helper()
	sort.Strings(all)
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
	out := make([]string, 0, len(want))
	for _, harness := range all {
		if _, ok := want[harness]; ok {
			out = append(out, harness)
		}
	}
	if len(out) == 0 {
		t.Fatalf("AO_PERF_HARNESS=%q matched none of %v", filter, all)
	}
	return out
}

func requireHarnessBinary(t *testing.T, harness string) error {
	t.Helper()
	for _, ha := range agentregistry.Harnessed() {
		if string(ha.Harness) != harness {
			continue
		}
		resolver, ok := ha.Agent.(interface {
			ResolveBinary(context.Context) (string, error)
		})
		if !ok {
			return fmt.Errorf("adapter does not implement ResolveBinary")
		}
		_, err := resolver.ResolveBinary(context.Background())
		if err != nil {
			if errors.Is(err, ports.ErrAgentBinaryNotFound) {
				return err
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("unknown harness %q (not in agent registry)", harness)
}

func requirePerfBenchEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("AO_PERF_BENCH") != "1" {
		t.Skip("set AO_PERF_BENCH=1 to run spawn/ACP latency benches (opt-in; not CI)")
	}
}

func spawnPerfRepeat() int {
	n, err := strconv.Atoi(os.Getenv("AO_PERF_REPEAT"))
	if err != nil || n < 1 {
		return 3
	}
	return n
}

func registerPerfRepo(t *testing.T, d *daemon, repo string) string {
	t.Helper()
	abs, err := filepath.Abs(repo)
	if err != nil {
		t.Fatalf("abs AO_PERF_REPO: %v", err)
	}
	if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
		t.Fatalf("AO_PERF_REPO %s is not a git checkout: %v", abs, err)
	}
	var out struct {
		Project struct{ ID string } `json:"project"`
	}
	status, err := d.call("POST", "/projects", map[string]any{"path": abs}, &out)
	if err != nil {
		t.Fatalf("register AO_PERF_REPO: %v", err)
	}
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("register AO_PERF_REPO: status=%d", status)
	}
	if out.Project.ID == "" {
		t.Fatalf("register AO_PERF_REPO returned no id")
	}
	return out.Project.ID
}

func resolvePerfRepo(t *testing.T) (repo string, cleanup func()) {
	t.Helper()
	if env := os.Getenv("AO_PERF_REPO"); env != "" {
		abs, err := filepath.Abs(env)
		if err != nil {
			t.Fatalf("abs AO_PERF_REPO: %v", err)
		}
		return abs, nil
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=ao-perf", "GIT_AUTHOR_EMAIL=perf@example.com",
			"GIT_COMMITTER_NAME=ao-perf", "GIT_COMMITTER_EMAIL=perf@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	run("git", "init", "-b", "main")
	// A few hundred files: enough to exercise checkout without matching a giant monorepo.
	for i := 0; i < 200; i++ {
		path := filepath.Join(dir, fmt.Sprintf("f-%03d.txt", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("file %d\n", i)), 0o644); err != nil {
			t.Fatalf("write fixture file: %v", err)
		}
	}
	run("git", "add", "-A")
	run("git", "commit", "-m", "perf fixture")
	return dir, nil
}

func (d *daemon) callTimedRaw(timeout time.Duration, method, path string, body any) (int, []byte, error) {
	client := &http.Client{Timeout: timeout}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, d.baseURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return res.StatusCode, nil, err
	}
	return res.StatusCode, raw, nil
}

func durationsToMs(samples []time.Duration) []float64 {
	out := make([]float64, len(samples))
	for i, sample := range samples {
		out[i] = float64(sample.Milliseconds())
	}
	return out
}

func medianDurationMs(samples []time.Duration) float64 {
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

func minDurationMs(samples []time.Duration) float64 {
	best := samples[0]
	for _, sample := range samples[1:] {
		if sample < best {
			best = sample
		}
	}
	return float64(best.Milliseconds())
}

func maxDurationMs(samples []time.Duration) float64 {
	best := samples[0]
	for _, sample := range samples[1:] {
		if sample > best {
			best = sample
		}
	}
	return float64(best.Milliseconds())
}

func writeSpawnPerfRecord(t *testing.T, workload string, result map[string]any) {
	t.Helper()
	label := os.Getenv("AO_PERF_LABEL")
	if label == "" {
		label = "sample"
	}
	commit := "unknown"
	if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		commit = string(bytes.TrimSpace(out))
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
		t.Fatalf("marshal: %v", err)
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

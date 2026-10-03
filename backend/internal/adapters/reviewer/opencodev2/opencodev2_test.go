package opencodev2_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/reviewer/opencodev2"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type permissionRule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}

func TestReviewCommandAppliesFinalReadOnlyAgentPermissions(t *testing.T) {
	binary, _ := fakeOpenCode(t, "2.0.0")
	projectConfig := filepath.Join(t.TempDir(), "opencode.json")
	projectOriginal := `{"permissions":[{"action":"*","resource":"*","effect":"allow"}]}`
	if err := os.WriteFile(projectConfig, []byte(projectOriginal), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_CONFIG", projectConfig)
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"providers":{"local":{"name":"mine"}},"permissions":[{"action":"*","resource":"*","effect":"allow"}],"agents":{"user-agent":{"system":"keep"},"ao-review-w1":{"description":"keep this","permissions":[{"action":"*","resource":"*","effect":"allow"}]}}}`)
	promptRoot := filepath.Join(t.TempDir(), "prompts", "reviewer")

	reviewer := opencodev2.New()
	var _ ports.Reviewer = reviewer
	var _ ports.ReviewerCanceller = reviewer
	var _ ports.ReviewerRestorer = reviewer
	if reviewer.Harness() != domain.ReviewerOpenCodeV2 {
		t.Fatalf("Harness = %q, want %q", reviewer.Harness(), domain.ReviewerOpenCodeV2)
	}
	spec, err := reviewer.ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:     "review-w1",
		WorkspacePath:  "/worker",
		Prompt:         "Review this task.",
		SystemPrompt:   "Read only.",
		TaskPromptRoot: promptRoot,
		Config:         domain.AgentConfig{Model: " provider/model "},
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}
	if argv := dropDataHome(spec.Argv); len(argv) < 4 || argv[0] != "env" || argv[2] != binary {
		t.Fatalf("argv = %#v", spec.Argv)
	}
	if slicesContain(spec.Argv, "--auto") || slicesContain(spec.Argv, "--dangerously-skip-permissions") {
		t.Fatalf("reviewer used permissive CLI flag: %#v", spec.Argv)
	}
	if !slicesContain(spec.Argv, "--prompt=Review this task.") {
		t.Fatalf("argv missing task prompt: %#v", spec.Argv)
	}

	content := inlineConfig(t, spec)
	var config struct {
		DefaultAgent string           `json:"default_agent"`
		Providers    map[string]any   `json:"providers"`
		Permissions  []permissionRule `json:"permissions"`
		Agents       map[string]struct {
			System, Mode, Model, Description string
			Permissions                      []permissionRule `json:"permissions"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		t.Fatalf("decode inline config: %v", err)
	}
	agent := config.Agents["ao-review-w1"]
	if config.DefaultAgent != "ao-review-w1" || agent.System != "Read only." || agent.Mode != "primary" || agent.Model != "provider/model" || agent.Description != "keep this" {
		t.Fatalf("selected reviewer agent = %+v, default=%q", agent, config.DefaultAgent)
	}
	if permissionEffect(append(config.Permissions, agent.Permissions...), "edit", "src/main.go") != "deny" {
		t.Fatalf("edit remained allowed after caller and agent rules: global=%#v agent=%#v", config.Permissions, agent.Permissions)
	}
	for _, command := range []string{
		"gh api repos/o/r/pulls/1",
		"git diff HEAD^ HEAD",
		"git log --oneline",
		"git show HEAD",
		"git status --short",
		"ao review submit --run run-1",
		"gh api --method POST repos/o/r/pulls/1/reviews --input -",
		"gh api --method POST repos/o/r/pulls/1/reviews --input - --jq '.id'",
		"printf '%s' '{ \"event\": \"COMMENT\", \"body\": \"a > b\", \"comments\": [] }'",
		"printf '%s' '{ \"reviews\": [] }'",
		"ao review submit --session ao-1 --reviews -",
	} {
		if permissionEffect(agent.Permissions, "shell", command) != "allow" {
			t.Errorf("allowed shell command %q was denied", command)
		}
	}
	for _, command := range []string{"printf '%s' '{}' > README.md", "gh api --method POST repos/o/r/pulls/1/reviews --input - --jq '.id' > f", "gh api --method PUT repos/o/r/pulls/1/merge", "gh api -fbody=x repos/o/r/issues/1/comments", "gh api repos/o/r/issues/1/comments -Fbody=x", "gh api repos/o/r/pulls/1 --method=PUT", "gh api repos/o/r/pulls/1; gh pr merge 1", "gh api graphql", "gh api -XPUT repos/o/r/pulls/1/merge", "gh api repos/o/r/issues/1/comments -f body=x", "printf x | gh api --method PUT repos/o/r/pulls/1/merge --input -", "git diff --output=x.txt HEAD", "git show HEAD > README.md", "gh api repos/o/r/pulls/1 > README.md", "gh api repos/o/r/pulls/1 >README.md", "git diff HEAD >README.md", "git log | tee f", "git show HEAD; rm -rf x", "ao review submit --run r > f", "printf x | ao review submit --reviews - > f", "printf x | gh api --method POST repos/o/r/pulls/1/reviews --input - > f", "git show --output=x HEAD", "git push origin main", "rm -rf /", "gh pr merge 1", "ao session kill worker-1"} {
		if permissionEffect(agent.Permissions, "shell", command) != "deny" {
			t.Errorf("unlisted shell command %q was allowed", command)
		}
	}
	if config.Providers["local"] == nil || config.Agents["user-agent"].System != "keep" || len(config.Permissions) != 1 || config.Permissions[0].Effect != "allow" {
		t.Fatalf("caller config was not preserved: %s", content)
	}
	data, err := os.ReadFile(projectConfig)
	if err != nil || string(data) != projectOriginal {
		t.Fatalf("project config changed: %q %v", data, err)
	}
}

func TestReviewRestoreCommandReappliesReadOnlyPolicy(t *testing.T) {
	binary, _ := fakeOpenCode(t, "2.0.0")
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"agents":{"ao-review-w1":{"permissions":[{"action":"edit","resource":"*","effect":"allow"}]}}}`)
	promptRoot := filepath.Join(t.TempDir(), "reviewer")

	spec, ok, err := opencodev2.New().ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:     "review-w1",
		AgentSessionID: "ses_native",
		RunID:          "run-1",
		Prompt:         "Resume review.",
		SystemPrompt:   "Read only.",
		TaskPromptRoot: promptRoot,
	})
	if err != nil || !ok {
		t.Fatalf("ReviewRestoreCommand = ok %v, err %v", ok, err)
	}
	if !spec.NativeResumed || spec.AgentSessionID != "ses_native" {
		t.Fatalf("restore metadata = %+v", spec)
	}
	wantTail := []string{binary, "--standalone", "--session", "ses_native", "--prompt=Resume review."}
	if !reflect.DeepEqual(spec.Argv[len(spec.Argv)-len(wantTail):], wantTail) {
		t.Fatalf("restore argv = %#v, want tail %#v", spec.Argv, wantTail)
	}
	var config struct {
		DefaultAgent string `json:"default_agent"`
		Agents       map[string]struct {
			Permissions []permissionRule `json:"permissions"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(inlineConfig(t, spec)), &config); err != nil {
		t.Fatal(err)
	}
	rules := config.Agents[config.DefaultAgent].Permissions
	if permissionEffect(rules, "edit", "README.md") != "deny" || permissionEffect(rules, "read", "README.md") != "allow" {
		t.Fatalf("restore policy = %#v", rules)
	}
}

func TestWrongMajorRejectedBeforeLaunchOrRestore(t *testing.T) {
	_, logPath := fakeOpenCode(t, "1.18.33")
	reviewer := opencodev2.New()
	if _, err := reviewer.ReviewCommand(context.Background(), ports.ReviewInvocation{SystemPromptFile: filepath.Join(t.TempDir(), "missing.md")}); err == nil || !strings.Contains(err.Error(), "requires OpenCode 2") {
		t.Fatalf("launch error = %v, want wrong-major error", err)
	}
	if _, ok, err := reviewer.ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{ReviewerID: "review-w1", AgentSessionID: "ses_1", SystemPromptFile: filepath.Join(t.TempDir(), "missing.md")}); err == nil || ok || !strings.Contains(err.Error(), "requires OpenCode 2") {
		t.Fatalf("restore = ok %v err %v, want wrong-major error", ok, err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, invocation := range strings.FieldsFunc(strings.TrimSpace(string(data)), func(r rune) bool { return r == '\n' }) {
		if invocation != "--version" {
			t.Fatalf("wrong-major probe launched OpenCode: log=%q", data)
		}
	}
}

func TestReviewMessageAndCancellation(t *testing.T) {
	reviewer := opencodev2.New()
	message, err := reviewer.ReviewMessage(context.Background(), ports.ReviewInvocation{Prompt: "next review"})
	if err != nil || message != "next review" {
		t.Fatalf("ReviewMessage = %q, %v", message, err)
	}
	spec, err := reviewer.ReviewCancel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if spec.Mode != ports.ReviewCancelInput || !reflect.DeepEqual(spec.Inputs, []string{"\x1b", "\x1b"}) || spec.InputDelay != 150*time.Millisecond {
		t.Fatalf("cancel spec = %+v", spec)
	}
}

func fakeOpenCode(t *testing.T, version string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "opencode")
	logPath := filepath.Join(dir, "invocations.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logPath + "'\nif [ \"$1\" = --version ]; then printf '%s\\n' '" + version + "'; exit; fi\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	return binary, logPath
}

func inlineConfig(t *testing.T, spec ports.ReviewCommandSpec) string {
	t.Helper()
	const prefix = "OPENCODE_CONFIG_CONTENT="
	for _, arg := range spec.Argv {
		if strings.HasPrefix(arg, prefix) {
			content := strings.TrimPrefix(arg, prefix)
			if spec.Env[prefix[:len(prefix)-1]] != content {
				t.Fatalf("reviewer env and argv overlay differ: env=%q argv=%q", spec.Env[prefix[:len(prefix)-1]], content)
			}
			return content
		}
	}
	t.Fatalf("argv has no %s assignment: %#v", prefix, spec.Argv)
	return ""
}

func slicesContain(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func permissionEffect(rules []permissionRule, action, resource string) string {
	effect := ""
	for _, rule := range rules {
		if (rule.Action == "*" || rule.Action == action) && globMatches(rule.Resource, resource) {
			effect = rule.Effect
		}
	}
	return effect
}

func globMatches(pattern, value string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == value
	}
	if !strings.HasPrefix(value, parts[0]) {
		return false
	}
	remaining := value[len(parts[0]):]
	for i, part := range parts[1:] {
		if part == "" {
			continue
		}
		index := strings.Index(remaining, part)
		if index < 0 || i == len(parts)-2 && !strings.HasSuffix(remaining, part) {
			return false
		}
		remaining = remaining[index+len(part):]
	}
	return true
}

func dropDataHome(argv []string) []string {
	out := make([]string, 0, len(argv))
	for _, arg := range argv {
		if !strings.HasPrefix(arg, "XDG_DATA_HOME=") {
			out = append(out, arg)
		}
	}
	return out
}

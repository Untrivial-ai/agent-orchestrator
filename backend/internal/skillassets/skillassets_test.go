package skillassets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

func TestEmbeddedSkillFrontmatterIsValidYAML(t *testing.T) {
	for _, name := range []string{SkillName, TestingSkillName} {
		t.Run(name, func(t *testing.T) { testEmbeddedSkillFrontmatter(t, name) })
	}
}

func testEmbeddedSkillFrontmatter(t *testing.T, name string) {
	t.Helper()
	body, err := files.ReadFile(name + "/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded SKILL.md: %v", err)
	}

	parts := strings.SplitN(string(body), "---", 3)
	if len(parts) != 3 {
		t.Fatal("embedded SKILL.md is missing YAML frontmatter delimiters")
	}

	var frontmatter struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Trigger     string `yaml:"trigger"`
	}
	if err := yaml.Unmarshal([]byte(parts[1]), &frontmatter); err != nil {
		t.Fatalf("parse embedded SKILL.md frontmatter: %v", err)
	}
	if frontmatter.Name != name {
		t.Fatalf("frontmatter name = %q, want %q", frontmatter.Name, name)
	}
	if strings.TrimSpace(frontmatter.Description) == "" {
		t.Fatal("frontmatter description is empty")
	}
	if name == SkillName && strings.TrimSpace(frontmatter.Trigger) == "" {
		t.Fatal("frontmatter trigger is empty")
	}
}

func TestEmbeddedPreviewGuidanceDoesNotScaffoldStaticSites(t *testing.T) {
	previewBody, err := files.ReadFile("using-ao/commands/preview.md")
	if err != nil {
		t.Fatalf("read embedded preview guidance: %v", err)
	}
	previewText := string(previewBody)
	normalizedPreviewText := strings.Join(strings.Fields(previewText), " ")
	for _, required := range []string{
		"Static HTML and Markdown do not need a development server",
		"Never create or modify `package.json`",
		"Do not create `.ao/launch.json` unless the user asks",
		"reuse the repository's existing dev command",
		"without waiting for a separate \"open it\" request",
		"Do not steal the browser from an active application",
		"open the primary requested artifact",
		"relative to the session workspace root, not the shell's current directory",
		"Do not compensate for the current directory with `../README.md`",
		"existing confined loopback preview origin",
		"not a deployment or a new development server",
		"Do not open workspace files with `file://`",
	} {
		if !strings.Contains(normalizedPreviewText, required) {
			t.Fatalf("preview guidance missing %q:\n%s", required, previewText)
		}
	}

	skillBody, err := files.ReadFile("using-ao/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded SKILL.md: %v", err)
	}
	skillText := string(skillBody)
	normalizedSkillText := strings.Join(strings.Fields(skillText), " ")
	if !strings.Contains(normalizedSkillText, "[commands/preview.md](commands/preview.md) before acting") ||
		!strings.Contains(normalizedSkillText, "automatic-handoff rules are load-bearing") ||
		!strings.Contains(normalizedSkillText, "[commands/browser.md](commands/browser.md)") ||
		!strings.Contains(normalizedSkillText, "opt-in network policy") {
		t.Fatalf("skill catalog is missing focused preview/browser routing:\n%s", skillText)
	}
}

func TestEmbeddedBrowserGuidanceKeepsNetworkCaptureOptional(t *testing.T) {
	body, err := files.ReadFile("using-ao/commands/browser.md")
	if err != nil {
		t.Fatalf("read embedded browser guidance: %v", err)
	}
	text := strings.Join(strings.Fields(string(body)), " ")
	for _, required := range []string{
		"Network capture is optional and disabled by default",
		"Do not enable it for routine navigation or interaction",
		"no request or response bodies, credentials, cookies, or query values",
		"`network status` and `network list` never enable capture",
		"The user can select or close these same tabs",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("browser guidance missing %q:\n%s", required, body)
		}
	}
}

func TestEmbeddedSessionGuidanceDocumentsCanonicalInAppLinks(t *testing.T) {
	body, err := files.ReadFile("using-ao/commands/session.md")
	if err != nil {
		t.Fatalf("read embedded session guidance: %v", err)
	}
	text := strings.Join(strings.Fields(string(body)), " ")
	for _, required := range []string{
		"ao://sessions/{project-id}/{session-id}",
		"Never use display names as identity",
		"Encode either ID with URL percent encoding",
		"Do not append query strings, fragments, action names, credentials, authorities, or extra path segments",
		"only inside a currently running AO desktop app",
		"never sends a message, executes a command, or performs another action",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("session-link guidance missing %q:\n%s", required, body)
		}
	}
}

func TestEmbeddedSpawnGuidanceListsOpenCodeV2(t *testing.T) {
	body, err := files.ReadFile("using-ao/commands/spawn.md")
	if err != nil {
		t.Fatalf("read embedded spawn guidance: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "`opencode`, `opencode-v2`") {
		t.Fatalf("spawn guidance does not distinguish OpenCode 1 and 2:\n%s", text)
	}
}

// TestInstall_WritesSkillAndIsIdempotent: Install must lay down the embedded
// skill (SKILL.md plus a commands file) under <dataDir>/skills/using-ao, and a
// second run must clobber cleanly, leaving no stale files. This is the whole
// contract the daemon boot hook relies on.
func TestInstall_WritesSkillAndIsIdempotent(t *testing.T) {
	dataDir := t.TempDir()

	if err := Install(dataDir); err != nil {
		t.Fatalf("Install: %v", err)
	}

	skillFile := filepath.Join(Dir(dataDir), "SKILL.md")
	if b, err := os.ReadFile(skillFile); err != nil {
		t.Fatalf("read %s: %v", skillFile, err)
	} else if len(b) == 0 {
		t.Fatalf("SKILL.md is empty")
	}
	if _, err := os.Stat(filepath.Join(Dir(dataDir), "commands", "spawn.md")); err != nil {
		t.Fatalf("commands/spawn.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(Dir(dataDir), "commands", "cue.md")); err != nil {
		t.Fatalf("commands/cue.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(Dir(dataDir), "commands", "browser.md")); err != nil {
		t.Fatalf("commands/browser.md missing: %v", err)
	}

	// A stale file inside the skill dir must not survive a reinstall (clobber).
	stale := filepath.Join(Dir(dataDir), "stale.md")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed stale file: %v", err)
	}
	if err := Install(dataDir); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale file survived reinstall (err=%v)", err)
	}
	testingFile := filepath.Join(TestingDir(dataDir), "SKILL.md")
	want, err := files.ReadFile(TestingSkillName + "/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testingFile, []byte("stale testing skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Install(dataDir); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(testingFile); err != nil || string(got) != string(want) {
		t.Fatalf("installed testing skill differs from embedded copy: %v", err)
	}
	// Head preparation must work from installed skills even when the target
	// revision predates testing support and base private state has been removed.
	for _, script := range []string{"prepare.py", "prepare.cjs", "bootstrap.cjs"} {
		want, err := files.ReadFile(TestingSkillName + "/scripts/" + script)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(TestingDir(dataDir), "scripts", script))
		if err != nil || string(got) != string(want) {
			t.Fatalf("installed preparation asset %s differs: %v", script, err)
		}
	}
}

// TestMaterialize_WritesIntoArbitraryDest covers the opencode adapter path:
// materialize the skill into .opencode/skills/using-ao (not the data-dir layout).
func TestMaterialize_WritesIntoArbitraryDest(t *testing.T) {
	dest := filepath.Join(t.TempDir(), ".opencode", "skills", SkillName)
	if err := Materialize(dest); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	} else if len(b) == 0 {
		t.Fatal("SKILL.md is empty")
	}
	if _, err := os.Stat(filepath.Join(dest, "commands", "spawn.md")); err != nil {
		t.Fatalf("commands/spawn.md missing: %v", err)
	}
}

func TestEmbeddedReportGuidanceDistinguishesDeliverablesFromDiagnostics(t *testing.T) {
	body, err := files.ReadFile("using-ao/commands/report.md")
	if err != nil {
		t.Fatal(err)
	}
	guidance := strings.Join(strings.Fields(string(body)), " ")
	for _, want := range []string{
		"working material, not deliverables",
		"Do not attach them unless requested or needed to explain an actionable failure",
		"Summarize validation in the report note",
		"not external publishing authorization",
		"In a chat session, an HTML file from the artifact directory that you attach with `--artifact` also shows in the thread.",
	} {
		if !strings.Contains(guidance, want) {
			t.Fatalf("report guidance missing %q", want)
		}
	}
}

func TestEmbeddedRenderGuidanceSeparatesRendersFromArtifacts(t *testing.T) {
	body, err := files.ReadFile("using-ao/commands/render.md")
	if err != nil {
		t.Fatal(err)
	}
	guidance := strings.Join(strings.Fields(string(body)), " ")
	for _, want := range []string{
		"## ao render or a session artifact",
		"when the page answers a question in the thread",
		"attach it with `ao report --artifact <path>`",
		"In a chat session, an HTML file from the artifact directory that you attach with `--artifact` also shows in the thread.",
		"add `--artifact` to `ao render` (or set `artifact: true` in `html_render`). Do this only when the user asks to keep the page.",
	} {
		if !strings.Contains(guidance, want) {
			t.Fatalf("render guidance missing %q", want)
		}
	}
}

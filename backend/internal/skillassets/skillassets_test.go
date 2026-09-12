package skillassets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

func TestEmbeddedSkillFrontmatterIsValidYAML(t *testing.T) {
	for _, skillName := range []string{SkillName, BrowserSkillName} {
		t.Run(skillName, func(t *testing.T) {
			body, err := files.ReadFile(skillName + "/SKILL.md")
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
			if frontmatter.Name != skillName {
				t.Fatalf("frontmatter name = %q, want %q", frontmatter.Name, skillName)
			}
			if strings.TrimSpace(frontmatter.Description) == "" {
				t.Fatal("frontmatter description is empty")
			}
			if strings.TrimSpace(frontmatter.Trigger) == "" {
				t.Fatal("frontmatter trigger is empty")
			}
		})
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
		!strings.Contains(normalizedSkillText, "[`ao-browser`](../ao-browser/SKILL.md)") ||
		!strings.Contains(normalizedSkillText, "Do not load unrelated command guides first") {
		t.Fatalf("skill catalog is missing focused preview/browser routing:\n%s", skillText)
	}
}

func TestEmbeddedBrowserGuidanceKeepsNetworkCaptureOptional(t *testing.T) {
	body, err := files.ReadFile("ao-browser/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded browser guidance: %v", err)
	}
	text := strings.Join(strings.Fields(string(body)), " ")
	for _, required := range []string{
		"Network capture is optional and disabled by default",
		"never enable it for routine navigation or interaction",
		"The user and agent share selection",
		"named persistent profile",
		"references/troubleshooting.md",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("browser guidance missing %q:\n%s", required, body)
		}
	}
	networkBody, err := files.ReadFile("ao-browser/references/network.md")
	if err != nil {
		t.Fatalf("read embedded network guidance: %v", err)
	}
	networkText := strings.Join(strings.Fields(string(networkBody)), " ")
	for _, required := range []string{
		"no request or response bodies, credentials, cookies, or query values",
		"`network status` and `network list` never enable capture",
	} {
		if !strings.Contains(networkText, required) {
			t.Fatalf("network guidance missing %q:\n%s", required, networkBody)
		}
	}
}

func TestEmbeddedBrowserSkillStaysFocusedAndRoutesDeepGuidance(t *testing.T) {
	body, err := files.ReadFile("ao-browser/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded browser skill: %v", err)
	}
	lineCount := strings.Count(strings.TrimSuffix(string(body), "\n"), "\n") + 1
	if lineCount < 180 || lineCount > 230 {
		t.Fatalf("ao-browser SKILL.md has %d lines, want 180..230", lineCount)
	}
	text := strings.Join(strings.Fields(string(body)), " ")
	for _, required := range []string{
		"Choose the cheapest suitable surface",
		"It makes no LLM call",
		"named persistent profile",
		"Consequential actions",
		"[references/lifecycle.md](references/lifecycle.md)",
		"[references/troubleshooting.md](references/troubleshooting.md)",
		"[references/profiles.md](references/profiles.md)",
		"[references/network.md](references/network.md)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("browser skill missing %q:\n%s", required, body)
		}
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
	browserSkillFile := filepath.Join(BrowserDir(dataDir), "SKILL.md")
	if b, err := os.ReadFile(browserSkillFile); err != nil {
		t.Fatalf("read %s: %v", browserSkillFile, err)
	} else if !strings.Contains(string(b), "name: ao-browser") {
		t.Fatalf("ao-browser SKILL.md missing frontmatter: %s", b)
	}
	for _, name := range []string{"lifecycle.md", "troubleshooting.md", "profiles.md", "network.md"} {
		if _, err := os.Stat(filepath.Join(BrowserDir(dataDir), "references", name)); err != nil {
			t.Fatalf("ao-browser references/%s missing: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(Dir(dataDir), "commands", "browser.md")); !os.IsNotExist(err) {
		t.Fatalf("obsolete using-ao commands/browser.md was installed: %v", err)
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

func TestMaterializeBrowser_WritesIntoArbitraryDest(t *testing.T) {
	dest := filepath.Join(t.TempDir(), ".opencode", "skills", BrowserSkillName)
	if err := MaterializeBrowser(dest); err != nil {
		t.Fatalf("MaterializeBrowser: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	} else if !strings.Contains(string(b), "name: ao-browser") {
		t.Fatalf("installed skill missing ao-browser frontmatter: %s", b)
	}
	if _, err := os.Stat(filepath.Join(dest, "references", "troubleshooting.md")); err != nil {
		t.Fatalf("references/troubleshooting.md missing: %v", err)
	}
}

package skillassets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallMaterializesSkillTree(t *testing.T) {
	dataDir := t.TempDir()
	if err := Install(dataDir); err != nil {
		t.Fatalf("Install: %v", err)
	}
	root := Dir(dataDir)
	for _, name := range []string{
		"SKILL.md",
		filepath.Join("commands", "orchestration.md"),
		filepath.Join("commands", "pull-requests.md"),
	} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read installed %s: %v", name, err)
		}
		if len(body) == 0 {
			t.Fatalf("installed %s is empty", name)
		}
	}
	skill, _ := os.ReadFile(filepath.Join(root, "SKILL.md"))
	orchestration, _ := os.ReadFile(filepath.Join(root, "commands", "orchestration.md"))
	// The cloud skill must document the cloud grammar, never the desktop CLI.
	for _, needle := range []string{"`spawn`", "`report`", "`send`", "`kill`"} {
		if !strings.Contains(string(skill), needle) {
			t.Fatalf("SKILL.md does not mention %s", needle)
		}
	}
	for _, needle := range []string{"ao spawn", "ao report", "ao list", "ao send", "ao kill"} {
		if !strings.Contains(string(orchestration), needle) {
			t.Fatalf("orchestration.md does not document %q", needle)
		}
	}
	// Desktop-CLI grammar leaking in would mislead the agent into running
	// commands that do not exist in a sandbox. (SKILL.md deliberately names a
	// few desktop commands as absent; these needles are the desktop *usage*
	// forms that only appear if the desktop docs were copied.)
	combined := string(skill) + string(orchestration)
	for _, forbidden := range []string{"ao session ls", "ao session kill", "--project", "ao spawn --issue"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("skill mentions desktop-only surface %q", forbidden)
		}
	}
}

func TestInstallClobbersPreviousCopy(t *testing.T) {
	dataDir := t.TempDir()
	stale := filepath.Join(Dir(dataDir), "stale.md")
	if err := os.MkdirAll(Dir(dataDir), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Install(dataDir); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale file survived reinstall: %v", err)
	}
}

func TestInstallRequiresDataDir(t *testing.T) {
	if err := Install("  "); err == nil {
		t.Fatal("Install with blank dataDir should fail")
	}
}

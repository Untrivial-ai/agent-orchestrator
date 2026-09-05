package processenv

import (
	"strings"
	"testing"
)

func TestMergeInheritsDaemonEnvironmentAndAppliesOverlay(t *testing.T) {
	t.Setenv("AO_PROCESSENV_INHERITED", "parent")
	t.Setenv("AO_PROCESSENV_REPLACED", "old")

	got := Merge(map[string]string{
		"AO_PROCESSENV_REPLACED": "new",
		"AO_PROCESSENV_SESSION":  "session",
	})
	want := map[string]string{
		"AO_PROCESSENV_INHERITED": "parent",
		"AO_PROCESSENV_REPLACED":  "new",
		"AO_PROCESSENV_SESSION":   "session",
	}
	for _, entry := range got {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			if expected, exists := want[key]; exists {
				if value != expected {
					t.Fatalf("%s = %q, want %q", key, value, expected)
				}
				delete(want, key)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing environment values: %v", want)
	}
}

func TestMergeWithWindowsPathOverlayReplacesInheritedPathCase(t *testing.T) {
	got := mergeWith(
		[]string{"Path=C:\\Users\\me\\AppData\\Roaming\\npm", "AO_KEEP=parent"},
		map[string]string{"PATH": "C:\\Program Files\\Agent Orchestrator\\resources\\daemon"},
		true,
	)

	want := map[string]string{
		"AO_KEEP": "parent",
		"PATH":    "C:\\Program Files\\Agent Orchestrator\\resources\\daemon",
	}
	for _, entry := range got {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if expected, exists := want[key]; exists {
			if value != expected {
				t.Fatalf("%s = %q, want %q", key, value, expected)
			}
			delete(want, key)
		}
		if key == "Path" {
			t.Fatalf("inherited Path survived alongside PATH: %v", got)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing environment values: %v in %v", want, got)
	}
}

func TestMergeWithWindowsPinnedPATHWinsOverProjectPath(t *testing.T) {
	got := mergeWith(
		[]string{"Path=C:\\Users\\me\\AppData\\Roaming\\npm"},
		map[string]string{
			"Path": "C:\\Project\\node_modules\\.bin",
			"PATH": "C:\\Program Files\\Agent Orchestrator\\resources\\daemon;C:\\Project\\node_modules\\.bin",
		},
		true,
	)

	seen := map[string]string{}
	for _, entry := range got {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			seen[key] = value
		}
	}
	if len(seen) != 1 {
		t.Fatalf("merged env = %v, want one PATH entry", got)
	}
	if seen["PATH"] != "C:\\Program Files\\Agent Orchestrator\\resources\\daemon;C:\\Project\\node_modules\\.bin" {
		t.Fatalf("PATH = %q, want AO-pinned PATH in %v", seen["PATH"], got)
	}
}

func TestMergeWithCaseSensitiveModeKeepsDistinctPathKeys(t *testing.T) {
	got := mergeWith(
		[]string{"Path=/parent"},
		map[string]string{"PATH": "/overlay"},
		false,
	)

	seen := map[string]string{}
	for _, entry := range got {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			seen[key] = value
		}
	}
	if seen["Path"] != "/parent" || seen["PATH"] != "/overlay" {
		t.Fatalf("case-sensitive merge = %v, want both Path and PATH", got)
	}
}

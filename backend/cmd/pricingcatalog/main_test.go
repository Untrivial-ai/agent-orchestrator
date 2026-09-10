package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Break caught: the operational command accepting an unpinned source or
// failing to expose the generated catalog to the repository workflow.
func TestRunSyncThenValidate(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "model_prices_and_context_window.json")
	if err := os.WriteFile(sourcePath, []byte(`{
"anthropic/a":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"openai/o":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1},
"zai/z":{"litellm_provider":"zai","mode":"chat","input_cost_per_token":1,"output_cost_per_token":1}
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{"sync", "-root", root, "-source", sourcePath, "-revision", "0123456789abcdef0123456789abcdef01234567"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("sync: %v (%s)", err, stderr.String())
	}
	if got := stdout.String(); got != "changed\n" {
		t.Fatalf("sync output = %q, want %q", got, "changed\\n")
	}
	stdout.Reset()
	if err := run([]string{"validate", "-root", root}, &stdout, &stderr); err != nil {
		t.Fatalf("validate: %v (%s)", err, stderr.String())
	}
	if got := stdout.String(); got != "valid\n" {
		t.Fatalf("validate output = %q, want %q", got, "valid\\n")
	}

	if err := run([]string{"sync", "-root", root, "-source", sourcePath, "-revision", "not-a-sha"}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "exact 40-character") {
		t.Fatalf("invalid revision error = %v, want exact revision validation", err)
	}
}

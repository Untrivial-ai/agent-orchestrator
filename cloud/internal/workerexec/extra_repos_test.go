package workerexec

import (
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestExtraReposPromptNoteEmptyWithoutRepos(t *testing.T) {
	if note := extraReposPromptNote("/workspace/repository", nil); note != "" {
		t.Fatalf("expected empty note, got %q", note)
	}
	if note := extraReposPromptNote("/workspace/repository", []worker.RepoRef{{URL: "   "}}); note != "" {
		t.Fatalf("expected empty note for blank repo, got %q", note)
	}
}

func TestExtraReposPromptNoteListsSiblingPaths(t *testing.T) {
	note := extraReposPromptNote("/workspace/repository", []worker.RepoRef{
		{URL: "https://github.com/Pritom14/ChartSnip"},
		{URL: "https://github.com/owner/tools", Branch: "dev"},
	})
	for _, want := range []string{
		"ChartSnip",
		"/workspace/ChartSnip",
		"../ChartSnip",
		"tools",
		"/workspace/tools",
		"branch dev",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note missing %q\n---\n%s", want, note)
		}
	}
}

package acp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestLineDeltaCountsActualEditsNotWholeFiles(t *testing.T) {
	// A one-line rewrite inside a large file must not report +N −N for N=len(file).
	const n = 1415
	var oldBuilder, newBuilder strings.Builder
	for i := 0; i < n; i++ {
		oldBuilder.WriteString("line\n")
		if i == 100 {
			newBuilder.WriteString("changed\n")
		} else {
			newBuilder.WriteString("line\n")
		}
	}

	additions, deletions := lineDelta(oldBuilder.String(), newBuilder.String())
	if additions != 1 || deletions != 1 {
		t.Fatalf("lineDelta = +%d −%d, want +1 −1", additions, deletions)
	}
}

func TestLineDeltaAddedAndDeletedFiles(t *testing.T) {
	additions, deletions := lineDelta("", "one\ntwo\nthree")
	if additions != 3 || deletions != 0 {
		t.Fatalf("added file = +%d −%d, want +3 −0", additions, deletions)
	}

	additions, deletions = lineDelta("one\ntwo\nthree", "")
	if additions != 0 || deletions != 3 {
		t.Fatalf("deleted file = +%d −%d, want +0 −3", additions, deletions)
	}
}

func TestLineDeltaIdenticalSnapshots(t *testing.T) {
	additions, deletions := lineDelta("same\nfile\n", "same\nfile\n")
	if additions != 0 || deletions != 0 {
		t.Fatalf("identical = +%d −%d, want +0 −0", additions, deletions)
	}
}

func TestLineDeltaAppendOnly(t *testing.T) {
	additions, deletions := lineDelta("a\nb\n", "a\nb\nc\n")
	if additions != 1 || deletions != 0 {
		t.Fatalf("append = +%d −%d, want +1 −0", additions, deletions)
	}
}

func TestTurnDiffReplacesExpandedContextForSameTool(t *testing.T) {
	// Claude ACP often re-sends the same edit with a wider old/new window.
	// Combining the first narrow oldText with the later expanded newText inflates
	// the count (live: +1000 then +1002 / −2). Replacement keeps +1000.
	const appended = 1000
	prefix := lines("keep", 5)
	extraContext := lines("ctx", 2)
	suffix := lines("line", appended)

	tightOld := prefix
	tightNew := prefix + suffix
	expandedOld := extraContext + prefix
	expandedNew := extraContext + prefix + suffix

	add, del := lineDelta(tightOld, tightNew)
	if add != appended || del != 0 {
		t.Fatalf("tight update = +%d −%d, want +%d −0", add, del, appended)
	}
	add, del = lineDelta(expandedOld, expandedNew)
	if add != appended || del != 0 {
		t.Fatalf("expanded update = +%d −%d, want +%d −0", add, del, appended)
	}

	// The buggy first-old + latest-new merge that this regression guards against.
	wrongAdd, wrongDel := lineDelta(tightOld, expandedNew)
	if wrongAdd == appended && wrongDel == 0 {
		t.Fatal("fixture no longer reproduces the expanded-context inflation")
	}

	var acc turnDiffAccumulator
	path := "big.txt"
	toolID := "tool-edit-1"
	acc.replaceTool(toolID, []ports.ChatDiffFile{diffFileFromSnapshot(path, strPtr(tightOld), tightNew)})
	got := acc.aggregate()
	if len(got) != 1 || got[0].Additions != appended || got[0].Deletions != 0 {
		t.Fatalf("after tight update: %+v, want +%d −0", got, appended)
	}

	acc.replaceTool(toolID, []ports.ChatDiffFile{diffFileFromSnapshot(path, strPtr(expandedOld), expandedNew)})
	got = acc.aggregate()
	if len(got) != 1 || got[0].Additions != appended || got[0].Deletions != 0 {
		t.Fatalf("after expanded replace: +%d −%d (wrong merge was +%d −%d), want +%d −0",
			got[0].Additions, got[0].Deletions, wrongAdd, wrongDel, appended)
	}
}

func TestTurnDiffSumsDistinctToolCallsOnSamePath(t *testing.T) {
	var acc turnDiffAccumulator
	path := "f.txt"
	acc.replaceTool("t1", []ports.ChatDiffFile{diffFileFromSnapshot(path, strPtr("a\n"), "a\nb\n")})
	acc.replaceTool("t2", []ports.ChatDiffFile{diffFileFromSnapshot(path, strPtr("a\nb\n"), "a\nb\nc\n")})
	got := acc.aggregate()
	if len(got) != 1 || got[0].Additions != 2 || got[0].Deletions != 0 {
		t.Fatalf("aggregate = %+v, want +2 −0", got)
	}
}

func lines(prefix string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%s-%d\n", prefix, i)
	}
	return b.String()
}

func strPtr(value string) *string { return &value }

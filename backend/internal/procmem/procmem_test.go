package procmem

import (
	"strings"
	"testing"
)

const table = `
    1     0  1200 systemd
  100     1   900 tmux: server
  200   100  3000 bash
  300   200 1600000 claude
  310   300 410000 go
  320   300 88000 node
  400   100  2500 bash
  500     1 50000 firefox
`

func TestTreeSumsDescendantsOnly(t *testing.T) {
	tbl, err := Parse(table)
	if err != nil {
		t.Fatal(err)
	}
	tree := tbl.Tree(200)
	wantKiB := uint64(3000 + 1600000 + 410000 + 88000)
	if tree.RSSBytes != wantKiB*1024 {
		t.Fatalf("rss = %d, want %d", tree.RSSBytes, wantKiB*1024)
	}
	if len(tree.Processes) != 4 {
		t.Fatalf("processes = %d, want 4", len(tree.Processes))
	}
	if tree.Processes[1].Command != "claude" {
		t.Fatalf("second process = %q, want claude", tree.Processes[1].Command)
	}
}

func TestTreeCountsSharedDescendantsOnce(t *testing.T) {
	tbl, _ := Parse(table)
	if got, want := tbl.Tree(200, 300).RSSBytes, tbl.Tree(200).RSSBytes; got != want {
		t.Fatalf("shared roots rss = %d, want %d", got, want)
	}
}

func TestTreeMissingRootIsEmpty(t *testing.T) {
	tbl, _ := Parse(table)
	if tree := tbl.Tree(9999); tree.RSSBytes != 0 || len(tree.Processes) != 0 {
		t.Fatalf("missing root = %+v, want empty", tree)
	}
}

func TestParseRejectsMalformedRow(t *testing.T) {
	if _, err := Parse("abc def ghi\n"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestParseReadsOptionalCPUTime(t *testing.T) {
	tbl, err := Parse(`
  300   200 1600000 01:02:03 claude
  310   300 410000 2-00:00:30.50 go build
  320   300 88000 node
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.byPID[300]; got.CPUSeconds != 3723 || got.Command != "claude" {
		t.Fatalf("300 = %+v", got)
	}
	if got := tbl.byPID[310]; got.CPUSeconds != 2*86400+30.5 || got.Command != "go build" {
		t.Fatalf("310 = %+v", got)
	}
	if got := tbl.byPID[320]; got.CPUSeconds != 0 || got.Command != "node" {
		t.Fatalf("320 = %+v", got)
	}
	if tree := tbl.Tree(300); tree.CPUSeconds != 3723+2*86400+30.5 {
		t.Fatalf("tree cpu = %v", tree.CPUSeconds)
	}
}

func TestCPUPercentIsRateBetweenSnapshots(t *testing.T) {
	prev, _ := Parse("300 200 100 00:00:10 claude\n")
	cur, _ := Parse("300 200 100 00:00:14 claude\n310 300 100 00:00:01 node\n")
	// 4s of claude plus 1s of a brand-new node over a 5s gap: one full core.
	if got := CPUPercent(cur.Tree(300).Processes, prev, 5); got != 100 {
		t.Fatalf("cpu = %v, want 100", got)
	}
	if got := CPUPercent(cur.Tree(300).Processes, nil, 5); got != 0 {
		t.Fatalf("first sample cpu = %v, want 0", got)
	}
}

func TestParseDropsZombies(t *testing.T) {
	tbl, err := Parse("300 200 1000 00:00:01 claude\n301 300 0 00:00:00 ao <defunct>\n")
	if err != nil {
		t.Fatal(err)
	}
	if tree := tbl.Tree(300); len(tree.Processes) != 1 {
		t.Fatalf("zombie listed: %+v", tree.Processes)
	}
}

func TestParseKeepsFullCommandLineAndCapsIt(t *testing.T) {
	long := strings.Repeat("x", 500)
	tbl, err := Parse("300 200 100 00:00:01 sh -c go test ./internal/httpd/...\n310 300 100 00:00:01 node " + long + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.byPID[300].Command; got != "sh -c go test ./internal/httpd/..." {
		t.Fatalf("command = %q", got)
	}
	if got := tbl.byPID[310].Command; len(got) != maxCommandLen || !strings.HasPrefix(got, "node x") {
		t.Fatalf("capped command = %d chars", len(got))
	}
}

// TestParseHandlesRowsPastTheDefaultScannerLimit guards against bufio.Scanner's
// default 64 KiB token limit silently truncating a long electron/node argv
// line: the process it belongs to, and every row after it, must still parse.
func TestParseHandlesRowsPastTheDefaultScannerLimit(t *testing.T) {
	longArgs := strings.Repeat("x", 70*1024)
	tbl, err := Parse("300 200 100 00:00:01 node " + longArgs + "\n301 300 200 00:00:01 claude\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tbl.byPID[300]; !ok {
		t.Fatal("process with an oversized row went missing")
	}
	if _, ok := tbl.byPID[301]; !ok {
		t.Fatal("row after the oversized one went missing")
	}
}

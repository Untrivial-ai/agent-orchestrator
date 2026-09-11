package postgres

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// canonicalWorkerRequestKinds is every request kind the shipped code
// dispatches through ao_worker_requests: the workspace transport, the browser
// proxy, and the interface handoff commands.
var canonicalWorkerRequestKinds = []string{
	"workspace.list", "workspace.read", "workspace.write", "workspace.diff",
	"terminal.open", "terminal.input", "terminal.resize", "terminal.close",
	"browser.fetch",
	"interface.inspect", "interface.interrupt", "interface.stop",
	"interface.native-id", "interface.start",
}

// The kind allowlist lives in a SQL CHECK constraint that migrations rewrite
// wholesale. 00035 dropped 'browser.fetch' while re-adding the interface
// kinds, which 422'd every Cloud browser request on a fresh database. Guard
// the final schema state: the highest-numbered migration that rewrites
// ao_worker_requests_kind_check must allow every dispatched kind.
func TestWorkerRequestKindConstraintCoversDispatchedKinds(t *testing.T) {
	entries, err := filepath.Glob(filepath.Join("migrations", "*.sql"))
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no migrations found; run from the cloud module root")
	}

	type rewrite struct {
		version int
		kinds   map[string]bool
	}
	var rewrites []rewrite
	for _, entry := range entries {
		base := filepath.Base(entry)
		version, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			t.Fatalf("parse migration version from %s: %v", base, err)
		}
		contents, err := os.ReadFile(entry)
		if err != nil {
			t.Fatalf("read migration %s: %v", base, err)
		}
		up, _, _ := strings.Cut(string(contents), "-- +goose Down")
		if !strings.Contains(up, "ao_worker_requests_kind_check") {
			continue
		}
		kinds := workerRequestKindsFromUp(up)
		if len(kinds) == 0 {
			continue
		}
		rewrites = append(rewrites, rewrite{version: version, kinds: kinds})
	}
	if len(rewrites) == 0 {
		t.Fatal("no migration rewrites ao_worker_requests_kind_check")
	}
	sort.Slice(rewrites, func(i, j int) bool { return rewrites[i].version < rewrites[j].version })
	final := rewrites[len(rewrites)-1]

	var missing []string
	for _, kind := range canonicalWorkerRequestKinds {
		if !final.kinds[kind] {
			missing = append(missing, kind)
		}
	}
	if len(missing) > 0 {
		t.Fatalf(
			"final ao_worker_requests_kind_check (migration 000%03d) omits dispatched kinds %v; "+
				"the highest-numbered migration rewrites the allowlist wholesale and must not drop live request kinds",
			final.version, missing,
		)
	}
}

var workerRequestKindToken = regexp.MustCompile(`'([a-z][a-z0-9.-]*)'`)

func workerRequestKindsFromUp(up string) map[string]bool {
	kinds := map[string]bool{}
	for _, line := range strings.Split(up, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "'") {
			continue
		}
		for _, match := range workerRequestKindToken.FindAllStringSubmatch(line, -1) {
			kinds[match[1]] = true
		}
	}
	return kinds
}

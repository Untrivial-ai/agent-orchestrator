package cli

import (
	"strings"
	"testing"
)

func TestMCPCommandHelp(t *testing.T) {
	out, errOut, err := executeCLI(t, Deps{}, "mcp", "--help")
	if err != nil {
		t.Fatalf("ao mcp --help: %v\nstderr=%s", err, errOut)
	}
	combined := out + errOut
	for _, want := range []string{"MCP server", "--http", "stdio"} {
		if !strings.Contains(combined, want) {
			t.Fatalf("help missing %q:\n%s", want, combined)
		}
	}
}

func TestRootHelpListsMCP(t *testing.T) {
	out, errOut, err := executeCLI(t, Deps{}, "--help")
	if err != nil {
		t.Fatalf("ao --help: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out+errOut, "mcp") {
		t.Fatalf("root help missing mcp:\n%s", out+errOut)
	}
}

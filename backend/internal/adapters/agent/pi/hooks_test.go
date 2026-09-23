package pi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPiExtensionOnlyEndsSessionOnQuit(t *testing.T) {
	source := piActivityExtensionSource(true)
	for _, reason := range []string{"reload", "new", "resume", "fork"} {
		if strings.Contains(source, `event.reason === "`+reason+`"`) {
			t.Fatalf("session shutdown reason %q must not end the AO session", reason)
		}
	}
	if !strings.Contains(source, `event.reason === "quit"`) {
		t.Fatal("extension must end the AO session only for quit")
	}
}

func TestParsePiVersion(t *testing.T) {
	for _, tc := range []struct {
		output string
		wantOK bool
		want   piVersion
	}{
		{output: "pi 0.80.6\n", wantOK: true, want: piVersion{0, 80, 6}},
		{output: "@earendil-works/pi-coding-agent 1.2.3", wantOK: true, want: piVersion{1, 2, 3}},
		{output: "dev", wantOK: false},
	} {
		got, ok := parsePiVersion(tc.output)
		if ok != tc.wantOK || got != tc.want {
			t.Fatalf("parsePiVersion(%q) = %v, %v; want %v, %v", tc.output, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestPiAgentSettledSupportedFromBinaryVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{version: "0.80.5", want: false},
		{version: "0.80.6", want: true},
		{version: "0.81.0", want: true},
	} {
		t.Run(tc.version, func(t *testing.T) {
			p := &Plugin{resolvedBinary: fakePiBinary(t, tc.version)}
			got, err := p.piAgentSettledSupported(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("settled support for %s = %v, want %v", tc.version, got, tc.want)
			}
		})
	}
}

func TestPiExtensionStopEventOrderingFollowsProbedSupport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ao executable fixture uses a Unix shebang")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the Pi extension fixture")
	}

	for _, tc := range []struct {
		name             string
		settledSupported bool
		wantStopFrom     string
	}{
		{name: "legacy", settledSupported: false, wantStopFrom: "agent_end"},
		{name: "settled", settledSupported: true, wantStopFrom: "agent_settled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixtureDir := t.TempDir()
			modulePath := filepath.Join(fixtureDir, "ao-activity.mjs")
			source := strings.NewReplacer(
				`import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";`+"\n", "",
				`function callHookSync(hookName: string, payload: Record<string, unknown>)`, `function callHookSync(hookName, payload)`,
				`function sessionID(ctx: any): string`, `function sessionID(ctx)`,
				`export default function (pi: ExtensionAPI)`, `export default function (pi)`,
			).Replace(piActivityExtensionSource(tc.settledSupported))
			if err := os.WriteFile(modulePath, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			capturePath := filepath.Join(fixtureDir, "calls.jsonl")
			if err := os.WriteFile(filepath.Join(fixtureDir, "ao"), []byte(`#!/usr/bin/env node
const fs = require("node:fs");
let input = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", chunk => { input += chunk; });
process.stdin.on("end", () => {
  fs.appendFileSync(process.env.AO_TEST_CAPTURE, JSON.stringify({args: process.argv.slice(2), source: process.env.PI_EVENT_SOURCE, input}) + "\n");
});
`), 0o755); err != nil {
				t.Fatal(err)
			}
			harnessPath := filepath.Join(fixtureDir, "harness.mjs")
			if err := os.WriteFile(harnessPath, []byte(`import { pathToFileURL } from "node:url";
const handlers = new Map();
const loaded = await import(pathToFileURL(process.argv[2]).href);
loaded.default({ on(name, handler) { handlers.set(name, handler); } });
const ctx = { sessionManager: { getSessionId() { return "pi-session-1"; } } };
process.env.PI_EVENT_SOURCE = "session_start";
await handlers.get("session_start")({}, ctx);
process.env.PI_EVENT_SOURCE = "before_agent_start";
await handlers.get("before_agent_start")({ prompt: "fix bug" }, ctx);
process.env.PI_EVENT_SOURCE = "agent_end";
await handlers.get("agent_end")({}, ctx);
process.env.PI_EVENT_SOURCE = "agent_settled";
await handlers.get("agent_settled")({}, ctx);
process.env.PI_EVENT_SOURCE = "session_shutdown";
await handlers.get("session_shutdown")({ reason: "reload" }, ctx);
process.env.PI_EVENT_SOURCE = "session_shutdown";
await handlers.get("session_shutdown")({ reason: "quit" }, ctx);
`), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(context.Background(), node, harnessPath, modulePath)
			cmd.Env = append(os.Environ(), "PATH="+fixtureDir+string(os.PathListSeparator)+os.Getenv("PATH"), "AO_TEST_CAPTURE="+capturePath)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Pi extension harness failed: %v\n%s", err, output)
			}
			data, err := os.ReadFile(capturePath)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			if got := strings.Count(text, `["hooks","pi","stop"]`); got != 1 {
				t.Fatalf("stop hook count = %d, want 1:\n%s", got, text)
			}
			if !strings.Contains(text, `"source":"`+tc.wantStopFrom+`"`) {
				t.Fatalf("stop hook did not come from %s:\n%s", tc.wantStopFrom, text)
			}
			if !strings.Contains(text, `["hooks","pi","session-start"]`) || !strings.Contains(text, `["hooks","pi","user-prompt-submit"]`) || !strings.Contains(text, `["hooks","pi","session-end"]`) {
				t.Fatalf("expected lifecycle hook calls missing:\n%s", text)
			}
		})
	}
}

func fakePiBinary(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pi")
	source := `#!/usr/bin/env sh
if [ "$1" = "--version" ]; then
  echo "pi ` + version + `"
  exit 0
fi
exit 2
`
	if err := os.WriteFile(path, []byte(source), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

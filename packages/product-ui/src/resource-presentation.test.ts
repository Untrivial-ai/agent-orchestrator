import { describe, expect, it } from "vitest";
import {
	chipTone,
	formatResourceBytes,
	largestSession,
	pressureState,
	ownProcessCategories,
	processCategories,
	processKind,
	processList,
	stableResourceOrder,
	type ResourceProcess,
	type ResourceSessionFacts,
} from "./resource-presentation";

const GB = 1024 ** 3;
const MB = 1024 ** 2;

function machine(availableGiB: number, pressureRaw: number, pressureSource = "psi") {
	return { totalBytes: 16 * GB, availableBytes: availableGiB * GB, pressureRaw, pressureSource };
}

function session(over: Partial<ResourceSessionFacts> & { id: string }): ResourceSessionFacts {
	return { title: over.id, rssBytes: 200 * MB, working: false, idleSeconds: 0, ...over };
}

describe("pressureState", () => {
	it("reads PSI stall time when the kernel offers it", () => {
		expect(pressureState(machine(1, 0.5))).toBe("fine");
		expect(pressureState(machine(1, 5))).toBe("tight_soon");
		expect(pressureState(machine(1, 20.1))).toBe("tight");
	});

	it("falls back to available percent elsewhere", () => {
		expect(pressureState(machine(8, 50, "available_pct"))).toBe("fine");
		expect(pressureState(machine(3, 81, "available_pct"))).toBe("tight_soon");
		expect(pressureState(machine(1, 94, "available_pct"))).toBe("tight");
	});

	it("reads macOS's own kernel pressure level rather than a derived free-memory percent", () => {
		// A Mac with plenty of "Available" memory still keeps very little
		// literally "Free" by design, so the available-percent fallback would
		// misread it as tight; the kernel's own 1/2/4 verdict must win instead.
		expect(pressureState(machine(1, 1, "memorystatus"))).toBe("fine");
		expect(pressureState(machine(1, 2, "memorystatus"))).toBe("tight_soon");
		expect(pressureState(machine(1, 4, "memorystatus"))).toBe("tight");
	});
});

describe("chipTone", () => {
	const idle = session({ id: "idle" });
	const busy = session({ id: "busy", working: true, rssBytes: 3 * GB });

	it("is grey unless the card is part of the fix", () => {
		expect(chipTone("fine", idle, "busy")).toBe("neutral");
		expect(chipTone("tight_soon", busy, "busy")).toBe("neutral");
		expect(chipTone("tight_soon", idle, "busy")).toBe("warning");
		expect(chipTone("tight", busy, "busy")).toBe("critical");
	});

	it("names the largest session", () => {
		expect(largestSession([idle, busy])).toBe("busy");
	});
});

describe("formatResourceBytes", () => {
	it("shows whole megabytes and switches to GB at a thousand", () => {
		expect(formatResourceBytes(0.3 * MB)).toBe("307 KB");
		expect(formatResourceBytes(20 * 1024)).toBe("20 KB");
		expect(formatResourceBytes(4.5 * MB)).toBe("4.5 MB");
		expect(formatResourceBytes(238.4 * MB)).toBe("238 MB");
		expect(formatResourceBytes(994 * MB)).toBe("994 MB");
		expect(formatResourceBytes(999.6 * MB)).toBe("1.0 GB");
		expect(formatResourceBytes(2.25 * GB)).toBe("2.3 GB");
	});

	it("counts in 1024s like the OS's own tools, so a 16 GB machine reads 16.0 GB", () => {
		expect(formatResourceBytes(16 * GB)).toBe("16.0 GB");
		expect(formatResourceBytes(7.04 * GB)).toBe("7.0 GB");
	});
});

describe("stableResourceOrder", () => {
	it("sorts fresh rows by size but keeps a near-tie in its old order", () => {
		const rows = [
			{ id: "a", rssBytes: 500 },
			{ id: "b", rssBytes: 520 },
		];
		expect(stableResourceOrder([], rows).map((r) => r.id)).toEqual(["b", "a"]);
		expect(stableResourceOrder(["b", "a"], [{ id: "a", rssBytes: 530 }, { id: "b", rssBytes: 520 }]).map((r) => r.id)).toEqual(["b", "a"]);
		expect(stableResourceOrder(["b", "a"], [{ id: "a", rssBytes: 900 }, { id: "b", rssBytes: 520 }]).map((r) => r.id)).toEqual(["a", "b"]);
	});

	it("drops rows that vanished and appends newcomers", () => {
		expect(stableResourceOrder(["gone", "b"], [{ id: "b", rssBytes: 5 }, { id: "new", rssBytes: 1 }]).map((r) => r.id)).toEqual(["b", "new"]);
	});
});

const proc = (pid: number, ppid: number, mb: number, command: string, cpuPercent = 0): ResourceProcess => ({ pid, ppid, rssBytes: mb * MB, cpuPercent, command });

describe("processKind", () => {
	it("names the program and keeps AO's own subcommand", () => {
		expect(processKind("/tmp/.mount_x/resources/daemon/ao chat-host s1 /home/u")).toBe("ao chat-host");
		expect(processKind("/usr/bin/git status --short")).toBe("git");
		expect(processKind("")).toBe("?");
	});

	it("reads through the space in a macOS app bundle path", () => {
		expect(processKind("/Applications/Agent Orchestrator.app/Contents/Resources/daemon/ao chat-host s1")).toBe("ao chat-host");
		expect(
			Object.fromEntries(
				processCategories([
					proc(1, 0, 25, "/Applications/Agent Orchestrator.app/Contents/Resources/daemon/ao chat-host s1"),
					proc(2, 1, 300, "/opt/homebrew/bin/codex app-server"),
				]),
			),
		).toEqual({ 1: "ao", 2: "agent" });
	});
});

describe("processList", () => {
	// One Codex session as a Windows tester reported it: 16 processes, most of
	// them console hosts and the cmd/npx chain that starts an MCP server.
	const windowsCodex = [
		proc(1, 0, 30, "ao.exe"),
		proc(2, 1, 152, "codex.exe"),
		proc(3, 2, 59, "node.exe"),
		proc(4, 3, 8, "conhost.exe"),
		proc(5, 2, 42, "node.exe"),
		proc(6, 5, 10, "node_repl.exe"),
		proc(7, 5, 8, "conhost.exe"),
		proc(8, 2, 10, "node_repl.exe"),
		proc(9, 8, 8, "conhost.exe"),
		proc(10, 2, 9, "cmd.exe"),
		proc(11, 10, 121, "node.exe"),
		proc(12, 11, 9, "cmd.exe"),
		proc(13, 12, 52, "node.exe"),
		proc(14, 13, 98, "azmcp.exe"),
		proc(15, 10, 8, "conhost.exe"),
		proc(16, 2, 8, "conhost.exe"),
	];
	const sum = ({ rows, other }: ReturnType<typeof processList>) => rows.reduce((total, row) => total + row.bytes, 0) + (other?.bytes ?? 0);

	it("folds plumbing, keeps each program on its own line, and still adds up to the session", () => {
		const list = processList(windowsCodex);
		expect(list.rows.map((row) => `${row.kind} ${row.pid}`)).toEqual([
			"codex.exe 2",
			"node.exe 11",
			"azmcp.exe 14",
			"node.exe 3",
			"node.exe 13",
			"node.exe 5",
			"ao.exe 1",
			"node_repl.exe 8",
			"node_repl.exe 6",
		]);
		// codex.exe carries its own console host; node 11 carries the cmd and console host above it.
		expect(list.rows[0].bytes).toBe(160 * MB);
		expect(list.rows[1].bytes).toBe((121 + 9 + 8) * MB);
		expect(list.rows.map((row) => row.kind)).not.toContain("conhost.exe");
		expect(list.rows.map((row) => row.kind)).not.toContain("cmd.exe");
		expect(list.other).toBeUndefined();
		expect(sum(list)).toBe(632 * MB);
	});

	it("shows ten processes and folds the rest into one line", () => {
		const many = [proc(1, 0, 100, "codex"), ...Array.from({ length: 12 }, (_, i) => proc(10 + i, 1, 50 - i, `tool${i}`))];
		const list = processList(many);
		expect(list.rows).toHaveLength(10);
		expect(list.other?.count).toBe(3);
		// Nothing is lost: the tail is there for "show more".
		expect(list.other?.rows).toHaveLength(3);
		expect(sum(list)).toBe((100 + 12 * 50 - 66) * MB);
	});

	it("lists a small session in full, and names a single leftover past ten", () => {
		const list = processList([
			proc(1, 0, 29, "/path/ao chat-host s1"),
			proc(2, 1, 322, "/path/codex app-server"),
			proc(3, 2, 59, "/usr/lib/cua_node/bin/node cua-repl.mjs"),
			proc(4, 3, 9, "/usr/lib/cua_node/bin/node_repl"),
			proc(5, 2, 12, "/usr/lib/cua_node/bin/node_repl"),
		]);
		expect(list.rows.map((row) => `${row.kind} ${row.pid}`)).toEqual(["codex 2", "node 3", "ao chat-host 1", "node_repl 5", "node_repl 4"]);
		expect(list.other).toBeUndefined();
		expect(sum(list)).toBe(431 * MB);
		// Eleven: the last one stays named rather than becoming "1 other process".
		const eleven = processList([proc(1, 0, 300, "codex"), ...Array.from({ length: 10 }, (_, i) => proc(10 + i, 1, 5, `tool${i}`))]);
		expect(eleven.rows).toHaveLength(11);
		expect(eleven.other).toBeUndefined();
	});

	it("folds `sh -c` into its command but keeps a shell that runs a script", () => {
		const list = processList([
			proc(1, 0, 300, "/usr/bin/claude"),
			proc(2, 1, 3, "sh -c go test ./..."),
			proc(3, 2, 80, "/usr/local/go/bin/go test ./..."),
			proc(4, 1, 40, "/bin/bash /home/u/.local/bin/codex app-server"),
		]);
		expect(list.rows.map((row) => row.kind)).toEqual(["claude", "go", "bash"]);
		expect(list.rows[1].bytes).toBe(83 * MB);
		expect(list.rows[1].commands[0]).toBe("/usr/local/go/bin/go test ./...");
	});
});

describe("processCategories", () => {
	const of = (processes: ResourceProcess[]) => Object.fromEntries(processCategories(processes));

	it("labels a Codex chat session: AO host, the agent, and its MCP servers", () => {
		expect(
			of([
				proc(1, 0, 29, "/path/ao chat-host s1"),
				proc(2, 1, 322, "/path/codex app-server"),
				proc(3, 2, 59, "/usr/lib/cua_node/bin/node cua-repl.mjs"),
				proc(4, 3, 9, "/usr/lib/cua_node/bin/node_repl"),
				proc(5, 2, 12, "/usr/lib/cua_node/bin/node_repl"),
			]),
		).toEqual({ 1: "ao", 2: "agent", 3: "mcp", 4: "mcp", 5: "mcp" });
	});

	it("labels what a Claude terminal session runs: shells and CLI tools are commands", () => {
		expect(
			of([
				proc(1, 0, 20, "/opt/ao pty-host s1"),
				proc(2, 1, 10, "/opt/ao agent-process supervise --session s1"),
				proc(3, 2, 300, "/usr/bin/claude --permission-mode auto"),
				proc(4, 3, 3, "/usr/bin/zsh -c source snapshot && go test ./..."),
				proc(5, 4, 80, "/usr/local/go/bin/go test ./..."),
				proc(6, 3, 12, "/usr/lib/claude/vendor/rg --json foo"),
				proc(7, 3, 40, "node /home/u/.npm/_npx/x/node_modules/.bin/playwright-mcp"),
			]),
		).toEqual({ 1: "ao", 2: "ao", 3: "agent", 4: "command", 5: "command", 6: "command", 7: "mcp" });
	});

	it("treats AO's bundled ACP adapter as AO, so the agent under it is still the agent", () => {
		expect(
			of([
				proc(1, 0, 25, "/tmp/m/resources/daemon/ao chat-host s1"),
				proc(2, 1, 12, "/tmp/m/resources/acp-runtime/node/bin/node /tmp/m/resources/acp-runtime/node_modules/x"),
				proc(3, 2, 140, "/usr/bin/claude --output-format stream-json"),
			]),
		).toEqual({ 1: "ao", 2: "ao", 3: "agent" });
	});

	it("labels the Windows Codex tree, where cmd.exe hides an MCP server (known gap)", () => {
		const categories = processCategories([
			proc(1, 0, 30, "ao.exe"),
			proc(2, 1, 152, "codex.exe"),
			proc(3, 2, 59, "node.exe"),
			proc(8, 2, 10, "node_repl.exe"),
			proc(10, 2, 9, "cmd.exe"),
			proc(11, 10, 121, "node.exe"),
			proc(14, 11, 98, "azmcp.exe"),
		]);
		// cmd.exe and its node read as commands; the server's own name still says MCP.
		expect(Object.fromEntries(categories)).toEqual({ 1: "ao", 2: "agent", 3: "mcp", 8: "mcp", 10: "command", 11: "command", 14: "mcp" });
	});

	it("carries the category onto each flat row", () => {
		const list = processList([
			proc(1, 0, 29, "/path/ao chat-host s1"),
			proc(2, 1, 322, "/path/codex app-server"),
			proc(3, 2, 59, "/usr/lib/cua_node/bin/node cua-repl.mjs"),
		]);
		expect(list.rows.map((row) => `${row.kind} ${row.category}`)).toEqual(["codex agent", "node mcp", "ao chat-host ao"]);
	});
});

describe("ownProcessCategories", () => {
	it("labels AO's own row: the daemon, the desktop app, and what the daemon ran", () => {
		expect(
			Object.fromEntries(
				ownProcessCategories([
					proc(1, 0, 194, "/home/u/agent-orchestrator/frontend/node_modules/electron/dist/electron ."),
					proc(2, 1, 163, "/home/u/agent-orchestrator/frontend/node_modules/electron/dist/electron --type=renderer"),
					proc(3, 1, 35, "go run ./cmd/ao daemon"),
					proc(4, 3, 62, "/home/u/.cache/go-build/x/ao daemon"),
					proc(5, 4, 4, "ps -axww -o pid=,ppid=,rss=,time=,args="),
					proc(6, 4, 4, "/bin/bash /home/u/.local/bin/gh auth token"),
					proc(7, 0, 300, "/Applications/Agent Orchestrator.app/Contents/MacOS/Agent Orchestrator"),
				]),
			),
		).toEqual({ 1: "app", 2: "app", 3: "command", 4: "ao", 5: "command", 6: "command", 7: "app" });
	});
});

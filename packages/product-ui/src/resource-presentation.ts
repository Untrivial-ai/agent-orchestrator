/**
 * Every rule behind the memory monitor, as pure functions: which pressure
 * state the machine is in, which single suggestion to make, what colour a
 * card chip gets, how bytes read. Components hold no thresholds of their
 * own, so the product logic is testable without rendering anything, the
 * same way session-presentation.ts works for status.
 *
 * The one idea underneath: colour means "do something", not "here is a
 * number". When nothing needs doing everything is grey.
 */

/** Fine: ignore it. Tight soon: worth a glance. Tight: the machine is struggling. */
export type PressureState = "fine" | "tight_soon" | "tight";

export type MachineReading = {
	totalBytes: number;
	availableBytes: number;
	/** PSI some avg10 on Linux (percent of the last 10 s a task stalled on memory); 100 minus available percent elsewhere. */
	pressureRaw: number;
	pressureSource: string;
};

/**
 * Thresholds are a first guess, to be tuned against real readings. On the
 * PSI path a few percent of stall time already feels sluggish; the
 * available-percent fallback maps the classic 25% / 10% free cut-offs.
 */
export function pressureState(machine: MachineReading): PressureState {
	return pressureStateFromRaw(machine.pressureRaw, machine.pressureSource);
}

/** The same rule from a bare reading, for colouring one bar of the graph. */
export function pressureStateFromRaw(pressureRaw: number, pressureSource: string): PressureState {
	if (pressureSource === "psi") {
		if (pressureRaw > 20) return "tight";
		if (pressureRaw >= 5) return "tight_soon";
		return "fine";
	}
	if (pressureSource === "memorystatus") {
		// macOS's own kernel verdict: 1 normal, 2 warn, 4 critical. A direct
		// kernel signal like PSI, not a derived free-memory percentage.
		if (pressureRaw >= 4) return "tight";
		if (pressureRaw >= 2) return "tight_soon";
		return "fine";
	}
	// The fallback is 100 minus the available percent, so the classic
	// 25% / 10% free cut-offs sit at 75 and 90.
	if (pressureRaw > 90) return "tight";
	if (pressureRaw > 75) return "tight_soon";
	return "fine";
}

/** What the monitor knows about one session when it decides what to suggest. */
export type ResourceSessionFacts = {
	id: string;
	title: string;
	rssBytes: number;
	/** True while the agent is mid-turn. */
	working: boolean;
	/** Seconds since the agent last did anything; undefined when unknown. */
	idleSeconds?: number;
};

export type ChipTone = "neutral" | "warning" | "critical";

/**
 * A card chip is grey unless this card is part of the fix: yellow when the
 * machine is tight-ish and the session sits idle, red when it is tight and
 * this is the single largest session.
 */
export function chipTone(state: PressureState, session: ResourceSessionFacts, largestSessionId: string | undefined): ChipTone {
	if (state === "tight" && session.id === largestSessionId) return "critical";
	if (state !== "fine" && !session.working) return "warning";
	return "neutral";
}

/** The biggest live session by memory, for the red-chip rule. */
export function largestSession(sessions: ResourceSessionFacts[]): string | undefined {
	let best: ResourceSessionFacts | undefined;
	for (const s of sessions) {
		if (!best || s.rssBytes > best.rssBytes) best = s;
	}
	return best?.id;
}

const MB = 1024 ** 2;
const GB = 1024 ** 3;

/**
 * Whole megabytes under a gigabyte, one decimal GB above. Binary units under
 * the familiar labels, the way Activity Monitor, Task Manager and btop count
 * memory: a 16 GB machine reads 16.0 GB, not 17.2. Whole MB rather than 10 MB
 * steps: rows are summed into the AO total, and coarse rounding on each row
 * made 123 + 157 read as 120 + 160 against a 280 total.
 */
export function formatResourceBytes(bytes: number): string {
	if (bytes >= GB) return `${(bytes / GB).toFixed(1)} GB`;
	// Below a megabyte, kilobytes: a swapped-out process holds a few KB, and
	// calling that "1 MB" made a session's lines add up to more than its total.
	if (bytes < MB) return `${Math.max(1, Math.round(bytes / 1024))} KB`;
	// Below ten, one decimal, so a 4.5 MB line does not read as 5.
	if (bytes < 10 * MB) return `${(bytes / MB).toFixed(1)} MB`;
	const mb = Math.round(bytes / MB);
	if (mb >= 1000) return `${(mb / 1024).toFixed(1)} GB`;
	return `${mb} MB`;
}

/** Whole percent of one core; CPU is only shown while working, so zero never appears. */
export function formatResourceCPU(percent: number): string {
	return `${Math.round(percent)}%`;
}

/** Sort by memory, descending, but only move rows that changed by a real margin. */
const RESORT_MARGIN = 0.1;

/**
 * Keeps the previous order unless a session's size changed enough to
 * justify moving it, so two near-equal sessions do not swap every sample.
 */
export function stableResourceOrder<T extends { id: string; rssBytes: number }>(previous: string[], rows: T[]): T[] {
	const sorted = [...rows].sort((a, b) => b.rssBytes - a.rssBytes);
	if (previous.length === 0) return sorted;
	const byId = new Map(rows.map((r) => [r.id, r] as const));
	const kept = previous.map((id) => byId.get(id)).filter((r): r is T => r !== undefined);
	const known = new Set(kept.map((r) => r.id));
	const fresh = sorted.filter((r) => !known.has(r.id));
	const candidate = [...kept, ...fresh];
	for (let i = 1; i < candidate.length; i++) {
		const above = candidate[i - 1].rssBytes;
		const here = candidate[i].rssBytes;
		if (here > above * (1 + RESORT_MARGIN)) return sorted;
	}
	return candidate;
}

/** One process as the memory sample reports it. */
export type ResourceProcess = { pid: number; ppid: number; rssBytes: number; cpuPercent: number; command: string };

/**
 * What kind of process, nothing more: "git", "go", "claude". AO's own hosts
 * keep their subcommand ("ao pty-host") since that is the whole story. The
 * full command line stays in the tooltip.
 */
export function processKind(command: string): string {
	// A macOS bundle path has a space in it ("Agent Orchestrator.app"); start
	// inside the bundle so the space is not read as the end of the program.
	const bundle = command.indexOf(".app/Contents/");
	const [head, sub] = (bundle >= 0 ? command.slice(bundle) : command).split(" ");
	const name = head?.split("/").pop() || "?";
	return name === "ao" && sub && !sub.startsWith("-") ? `ao ${sub}` : name;
}

/**
 * One row of the process list: a program and the plumbing folded into it.
 * Rows add up to the session.
 */
export type ProcessRow = { kind: string; pid: number; category: ProcessCategory; commands: string[]; bytes: number; cpu: number };

/**
 * Where a process comes from: AO's own host, the agent (harness) itself, a
 * tool server the agent started from its config, or a command it ran. A
 * guess from the tree: nothing in a process table says "MCP server".
 */
export type ProcessCategory = "ao" | "app" | "agent" | "mcp" | "command";

/** The tail of small rows, summed into one so the list still adds up; rows is
 * the tail itself, for whoever asks to see it. */
export type ProcessOther = { count: number; bytes: number; cpu: number; commands: string[]; rows: ProcessRow[] };

const processListMaxRows = 10;

const bareName = (command: string) => processKind(command).toLowerCase().replace(/\.exe$/, "");

/** Windows' console host: one per console program, never what the user ran. */
function isConsoleHost(command: string): boolean {
	return bareName(command) === "conhost";
}

/**
 * A process that only exists to start another: `cmd.exe`, `sh -c …`, npx.
 * A shell running a script file is not one; the script is real work.
 */
function isLauncher(command: string): boolean {
	const name = bareName(command);
	const second = command.split(" ")[1];
	if (name === "cmd" || name === "npx") return true;
	if (["sh", "bash", "zsh", "dash"].includes(name)) return second === "-c" || second === "-lc";
	if (name === "npm") return second === "exec" || second === "x";
	return command.includes("npx-cli.js");
}

const shells = new Set(["sh", "bash", "zsh", "dash", "fish", "cmd", "powershell", "pwsh"]);
const commandTools = new Set(["rg", "git", "fd", "grep", "find", "codex-linux-sandbox", "sandbox-exec"]);
const runtimes = new Set(["npx", "uvx", "uv", "deno", "bun", "docker"]);

function isAOProcess(command: string): boolean {
	const kind = processKind(command);
	return kind === "ao" || kind === "ao.exe" || kind.startsWith("ao ") || command.includes("/acp-runtime/");
}

/** What a process the agent started directly says about its whole branch. */
function branchCategory(command: string): ProcessCategory {
	const name = bareName(command);
	if (/mcp/i.test(command)) return "mcp";
	if (shells.has(name) || commandTools.has(name)) return "command";
	if (runtimes.has(name) || name.startsWith("node") || name.startsWith("python")) return "mcp";
	return "command";
}

/** The desktop app: Electron in development, the packaged app otherwise. */
function isAppProcess(command: string): boolean {
	return /electron|agent[- ]orchestrator|\.app\/Contents\//i.test(command);
}

/**
 * AO's own processes have no agent to hang from: the daemon is AO, the
 * desktop shell is the app, and anything else is something the daemon ran,
 * like its own `ps` sample or a harness login check.
 */
export function ownProcessCategories(processes: ResourceProcess[]): Map<number, ProcessCategory> {
	return new Map(
		processes.map((p) => [p.pid, isAOProcess(p.command) ? "ao" : isAppProcess(p.command) ? "app" : "command"] as const),
	);
}

/**
 * A category for every process: AO's hosts by name, the agent as the first
 * process under them, and everything below the agent by the branch it hangs
 * from, decided by the process the agent started directly. A shell or a CLI
 * tool means the agent ran a command; a long-running runtime or an "mcp"
 * name means a tool server.
 */
export function processCategories(processes: ResourceProcess[]): Map<number, ProcessCategory> {
	const byPid = new Map(processes.map((p) => [p.pid, p] as const));
	const out = new Map<number, ProcessCategory>();
	const categoryOf = (p: ResourceProcess, hops = 0): ProcessCategory => {
		const known = out.get(p.pid);
		if (known) return known;
		const parent = p.ppid !== p.pid ? byPid.get(p.ppid) : undefined;
		let category: ProcessCategory;
		if (isAOProcess(p.command)) category = "ao";
		else if (!parent || hops > processes.length) category = "agent";
		else {
			const above = categoryOf(parent, hops + 1);
			category = above === "ao" ? "agent" : above === "agent" ? branchCategory(p.command) : above;
			// A server that names itself wins over a launcher that hid it: azmcp.exe under cmd.exe.
			if (category === "command" && /mcp/i.test(processKind(p.command))) category = "mcp";
		}
		out.set(p.pid, category);
		return category;
	};
	for (const p of processes) categoryOf(p);
	return out;
}

/**
 * A short flat list for the screen: console hosts and launchers folded into
 * the program they serve, one row per program, largest first, and anything
 * past the first ten summed into one "other" row. Every byte lands in exactly one
 * row, so the rows add up to the session. The copied report keeps the raw
 * tree instead.
 */
export function processList(processes: ResourceProcess[], { own = false }: { own?: boolean } = {}): { rows: ProcessRow[]; other?: ProcessOther } {
	const byPid = new Map(processes.map((p) => [p.pid, p] as const));
	const kids = new Map<number, ResourceProcess[]>();
	for (const p of processes) {
		if (p.ppid !== p.pid && byPid.has(p.ppid)) kids.set(p.ppid, [...(kids.get(p.ppid) ?? []), p]);
	}
	// The process whose row a given process is counted in.
	const owner = (p: ResourceProcess, hops = 0): ResourceProcess => {
		if (hops > processes.length) return p;
		const parent = byPid.get(p.ppid);
		if (isConsoleHost(p.command) && parent && parent !== p) return owner(parent, hops + 1);
		const real = (kids.get(p.pid) ?? []).filter((k) => !isConsoleHost(k.command));
		if (isLauncher(p.command) && real.length > 0) {
			return owner(real.reduce((a, b) => (b.rssBytes > a.rssBytes ? b : a)), hops + 1);
		}
		return p;
	};
	const categories = own ? ownProcessCategories(processes) : processCategories(processes);
	const rows = new Map<number, ProcessRow>();
	for (const p of processes) {
		const own = owner(p);
		let row = rows.get(own.pid);
		if (!row) {
			row = { kind: processKind(own.command), pid: own.pid, category: categories.get(own.pid) ?? "command", commands: [own.command], bytes: 0, cpu: 0 };
			rows.set(own.pid, row);
		}
		row.bytes += p.rssBytes;
		row.cpu += p.cpuPercent;
		if (own !== p) row.commands.push(p.command);
	}
	const sorted = [...rows.values()].sort((a, b) => b.bytes - a.bytes);
	const shown = processListMaxRows;
	// One leftover row is better named than hidden behind "1 other".
	if (sorted.length - shown <= 1) return { rows: sorted };
	const tail = sorted.slice(shown);
	return {
		rows: sorted.slice(0, shown),
		other: {
			count: tail.length,
			bytes: tail.reduce((sum, row) => sum + row.bytes, 0),
			cpu: tail.reduce((sum, row) => sum + row.cpu, 0),
			commands: tail.flatMap((row) => row.commands),
			rows: tail,
		},
	};
}

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { toKanbanColumn } from "@aoagents/product-ui";
import type { WorkspaceSession, WorkspaceSummary } from "../types/workspace";
import type { SessionMemoryReading } from "../hooks/useSessionMemory";
import { AppMemoryIndicator } from "./SessionMemoryPanel";
import { TooltipProvider } from "./ui/tooltip";

const { appMemoryMock, clipboardMock, memoryQueryMock, postMock, usageQueryMock, workspaceQueryMock } = vi.hoisted(() => ({
	appMemoryMock: vi.fn(),
	clipboardMock: vi.fn(),
	usageQueryMock: vi.fn(),
	memoryQueryMock: vi.fn(),
	postMock: vi.fn(),
	workspaceQueryMock: vi.fn(),
}));

vi.mock("../hooks/useSessionMemory", async (importOriginal) => ({
	...(await importOriginal<typeof import("../hooks/useSessionMemory")>()),
	useSessionMemory: memoryQueryMock,
	useAppMemory: appMemoryMock,
}));

vi.mock("../hooks/useSessionUsageSummaries", () => ({ useSessionUsageSummaries: usageQueryMock }));

vi.mock("../hooks/useWorkspaceQuery", () => ({
	workspaceQueryKey: ["workspaces"],
	useWorkspaceQuery: workspaceQueryMock,
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { POST: (...args: unknown[]) => postMock(...args) },
	apiErrorCode: (error: unknown) => (error as { code?: string } | null)?.code,
	apiErrorMessage: (_error: unknown, fallback: string) => fallback,
}));

vi.mock("../lib/telemetry", () => ({ captureRendererEvent: vi.fn() }));

vi.mock("../lib/bridge", () => ({ aoBridge: { clipboard: { writeText: clipboardMock } } }));

const GIB = 1024 ** 3;

const LONG_AGO = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString();

function session(id: string, title: string, activityState = "idle", lastActivityAt = LONG_AGO): WorkspaceSession {
	return {
		id,
		title,
		workspaceId: "p1",
		workspaceName: "radic",
		provider: "claude-code",
		status: "idle",
		kanbanColumn: toKanbanColumn(undefined, "idle"),
		updatedAt: "2026-09-18T00:00:00Z",
		activity: { state: activityState as "idle", lastActivityAt },
		prs: [],
	};
}

function reading(
	sessionId: string,
	rssBytes: number,
	processCount: number,
	processes: { pid: number; ppid: number; rssBytes: number; cpuPercent: number; command: string }[] = [],
	cpuPercent = 0,
) {
	return { sessionId, rssBytes, processCount, cpuPercent, sampledAt: "2026-09-18T00:00:00Z", processes };
}

/** A 32 GB host with the given amount free and PSI stall percentage. `load1`
 * is -1 on a platform with no load average (Windows), never otherwise negative. */
function host(availableGiB: number, pressureRaw = 0, cpuPercent = 0, load1 = 0.5) {
	return {
		cpuPercent,
		totalBytes: 32 * GIB,
		availableBytes: availableGiB * GIB,
		swapTotalBytes: 8 * GIB,
		swapUsedBytes: 0,
		swapBytesPerSec: 0,
		cpuCount: 8,
		load1,
		pressureRaw,
		pressureSource: "psi",
	};
}

/** AO holding `aoGiB` on a host with `availableGiB` free at the given pressure. */
function appReading(availableGiB: number, pressureRaw = 0, aoGiB = 2, hostCPUPercent = 0, aoCPUPercent = 12, load1 = 0.5) {
	return {
		isError: false,
		data: {
			app: {
				rssBytes: aoGiB * GIB,
				processCount: 20,
				cpuPercent: aoCPUPercent,
				own: reading("ao", 300 * 1024 ** 2, 3, [{ pid: 7, ppid: 1, rssBytes: 300 * 1024 ** 2, cpuPercent: 1, command: "ao daemon" }]),
			},
			system: host(availableGiB, pressureRaw, hostCPUPercent, load1),
			liveCount: 2,
		},
	};
}

function renderButton() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const tree = () => (
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<AppMemoryIndicator />
			</TooltipProvider>
		</QueryClientProvider>
	);
	const result = render(tree());
	return { ...result, rerender: () => result.rerender(tree()) };
}

beforeEach(() => {
	postMock.mockReset().mockResolvedValue({ data: {} });
	clipboardMock.mockReset().mockResolvedValue(undefined);
	usageQueryMock.mockReset().mockReturnValue({ data: undefined });
	const workspace: WorkspaceSummary = {
		id: "p1",
		name: "radic",
		sessions: [session("s-small", "small worker"), session("s-big", "big worker"), session("s-none", "unsampled worker")],
	} as WorkspaceSummary;
	workspaceQueryMock.mockReset().mockReturnValue({ data: [workspace], isError: false, isSuccess: true });
	appMemoryMock.mockReset().mockReturnValue(appReading(20));
	memoryQueryMock.mockReset().mockReturnValue({
		isError: false,
		data: new Map([
			["s-small", reading("s-small", 641_728_512, 5)],
			[
				"s-big",
				reading("s-big", 2_254_857_830, 9, [
					{ pid: 111, ppid: 1, rssBytes: 1_800_000_000, cpuPercent: 80.4, command: "claude" },
					{ pid: 222, ppid: 111, rssBytes: 454_857_830, cpuPercent: 1.6, command: "go test" },
				], 82),
			],
		]),
	});
});

describe("AppMemoryIndicator", () => {
	it("hides itself when nothing was sampled or the daemon cannot measure", () => {
		appMemoryMock.mockReturnValue({ isError: true, data: undefined });
		renderButton();
		expect(screen.queryByTestId("app-memory-indicator")).not.toBeInTheDocument();
	});

	it("reads a dot and AO's size, plus the fix; grey means nothing to do", () => {
		const { rerender } = renderButton();
		const button = screen.getByTestId("app-memory-indicator");
		expect(button).toHaveTextContent("2.1 GB");
		expect(button).not.toHaveTextContent("Fine");
		expect(button).toHaveAttribute("data-memory-state", "fine");
		expect(button).toHaveAttribute("aria-label", "Fine · 21.5 GB free of 34.4 GB · AO holds 2.1 GB · pressure 0.0");

		// Stalling on memory: the light colours, the phrase stays AO's own size.
		appMemoryMock.mockReturnValue(appReading(3, 12, 20));
		rerender();
		expect(screen.getByTestId("app-memory-indicator")).toHaveAttribute("data-memory-state", "tight_soon");
		expect(screen.getByTestId("app-memory-indicator")).toHaveTextContent("21.5 GB");
		expect(screen.getByTestId("app-memory-indicator")).not.toHaveTextContent("·");

		// Tight while AO is a sliver of what is in use: still only AO's own size, never a verdict on other apps.
		appMemoryMock.mockReturnValue(appReading(1, 40, 2));
		rerender();
		const tight = screen.getByTestId("app-memory-indicator");
		expect(tight).toHaveAttribute("data-memory-state", "tight");
		expect(tight).toHaveTextContent("2.1 GB");
		expect(tight.textContent).not.toMatch(/apps|AO is only|not AO/i);
	});

	it("goes grey and falls back to AO's own size where the host cannot be read", () => {
		appMemoryMock.mockReturnValue({ isError: false, data: { app: { rssBytes: 4 * GIB, processCount: 20, cpuPercent: 0 }, liveCount: 1 } });
		renderButton();
		const button = screen.getByTestId("app-memory-indicator");
		expect(button).toHaveTextContent("4.3 GB");
		expect(button).toHaveAttribute("data-memory-state", "unknown");
	});

	it("opens a window: stacked bar, rows largest first with no actions, AO pinned last", async () => {
		appMemoryMock.mockReturnValue(appReading(20, 0, 2, 40, 160));
		renderButton();
		await userEvent.click(screen.getByTestId("app-memory-indicator"));
		const table = await screen.findByTestId("session-memory-table");
		expect(within(table).getAllByRole("columnheader").map((th) => th.textContent)).toEqual(["Name", "PID", "Memory", "CPU"]);
		// A session is not a process: its PID cell says what is under it and that the row opens.
		expect(within(within(table).getAllByTestId("session-memory-row")[0]).getByTestId("session-memory-process-count")).toHaveTextContent("2 processes ›");
		expect(screen.getByTestId("session-memory-stacked")).toHaveTextContent("AO 2.1 GB");
		expect(screen.getByTestId("session-memory-stacked")).toHaveTextContent("Available 21.5 GB");
		expect(screen.getByTestId("session-memory-stacked")).not.toHaveTextContent("In use");
		expect(screen.getByTestId("session-memory-stacked")).not.toHaveTextContent("Other");
		// CPU graph at the bottom: host 40% busy on 8 cores, AO's 160% of one core is 20% of the machine.
		const cpu = screen.getByTestId("session-cpu-graph");
		expect(cpu).toHaveTextContent("CPU · 8 cores");
		expect(cpu).toHaveTextContent("load 0.50");
		// Each line is named, so a colour is never the only clue.
		expect(cpu).toHaveTextContent("Machine 40%");
		expect(cpu).toHaveTextContent("AO 20%");
		// Two lines, no bars: the machine and AO's share of it.
		expect(cpu.querySelectorAll("path")).toHaveLength(2);
		expect(within(cpu).queryByTestId("session-cpu-cores")).not.toBeInTheDocument();
		// Fine: no suggestion, every row grey.
		expect(screen.queryByTestId("session-memory-suggestion")).not.toBeInTheDocument();
		const rows = within(table).getAllByTestId("session-memory-row");
		expect(rows.map((row) => row.textContent)).toEqual([expect.stringContaining("big worker"), expect.stringContaining("small worker")]);
		expect(rows.every((row) => row.getAttribute("data-chip-tone") === "neutral")).toBe(true);
		expect(within(rows[0]).getByText("2.3 GB")).toBeInTheDocument();
		// An unsampled session is not a row: never "0 MB".
		expect(within(table).queryByText("unsampled worker")).not.toBeInTheDocument();
		const own = within(table).getByTestId("session-memory-own-row");
		expect(own).toHaveTextContent("Daemon and app");
		expect(own).toHaveTextContent("315 MB");
		expect(within(own).queryByRole("button")).not.toBeInTheDocument();

		// The window only measures: its rows copy, they never end a session.
		expect(within(table).queryByRole("button", { name: /terminate|kill|pause/i })).not.toBeInTheDocument();
		expect(within(table).queryAllByRole("button")).toHaveLength(0);
		expect(postMock).not.toHaveBeenCalled();
	});

	it("hides the load figure on a platform with no load average, in the graph and in the copied report", async () => {
		appMemoryMock.mockReturnValue(appReading(20, 0, 2, 40, 160, -1));
		renderButton();
		await userEvent.click(screen.getByTestId("app-memory-indicator"));
		const cpu = await screen.findByTestId("session-cpu-graph");
		expect(cpu).toHaveTextContent("CPU · 8 cores");
		expect(cpu).not.toHaveTextContent("load");
		await userEvent.click(screen.getByRole("button", { name: "Copy report" }));
		const report = clipboardMock.mock.calls[0][0] as string;
		expect(report).not.toContain("load");
	});

	it("expands any number of rows into their process trees, and collapses each on a second click", async () => {
		renderButton();
		await userEvent.click(screen.getByTestId("app-memory-indicator"));
		const table = await screen.findByTestId("session-memory-table");
		const bigRow = within(table).getAllByTestId("session-memory-row")[0];
		const own = within(table).getByTestId("session-memory-own-row");

		expect(screen.queryByTestId("session-memory-process-row")).not.toBeInTheDocument();
		await userEvent.click(bigRow);
		const children = await screen.findAllByTestId("session-memory-process-row");
		// A real tree: the child sits under its parent, indented, not in a flat list by size.
		expect(children[0]).toHaveTextContent("└─ claude");
		expect(children[0]).toHaveTextContent("1.8 GB");
		expect(children[0]).toHaveAttribute("data-process-depth", "0");
		// The PID sits in its own column, and a click copies it without toggling the row.
		const pid = within(children[0]).getByTestId("session-memory-pid");
		expect(pid).toHaveTextContent("111");
		await userEvent.click(pid);
		expect(clipboardMock).toHaveBeenCalledWith("111");
		expect(await within(children[0]).findByRole("button", { name: "Copied PID 111" })).toBeInTheDocument();
		expect(screen.getAllByTestId("session-memory-process-row")).toHaveLength(2);
		expect(children[1]).toHaveTextContent("└─ go");
		expect(children[1]).toHaveTextContent("222");
		expect(children[1]).not.toHaveTextContent("go test");
		expect(children[1]).toHaveAttribute("data-process-depth", "1");

		// A second row opens alongside, not instead.
		await userEvent.click(own);
		expect(screen.getAllByTestId("session-memory-process-row")).toHaveLength(3);

		await userEvent.click(bigRow);
		expect(screen.getAllByTestId("session-memory-process-row")).toHaveLength(1);
	});

	it("opens a row from the keyboard alone: Tab to reach it, Enter or Space to open it", async () => {
		renderButton();
		await userEvent.click(screen.getByTestId("app-memory-indicator"));
		const table = await screen.findByTestId("session-memory-table");
		const bigRow = within(table).getAllByTestId("session-memory-row")[0];
		const own = within(table).getByTestId("session-memory-own-row");

		bigRow.focus();
		expect(bigRow).toHaveFocus();
		await userEvent.keyboard("{Enter}");
		expect(await screen.findAllByTestId("session-memory-process-row")).toHaveLength(2);
		await userEvent.keyboard(" ");
		expect(screen.queryByTestId("session-memory-process-row")).not.toBeInTheDocument();

		own.focus();
		await userEvent.keyboard(" ");
		expect(await screen.findAllByTestId("session-memory-process-row")).toHaveLength(1);
	});

	it("shows a session's measured CPU even while it looks idle", async () => {
		const workspace: WorkspaceSummary = {
			id: "p1",
			name: "radic",
			sessions: [session("s-big", "big worker", "active"), session("s-small", "small worker")],
		} as WorkspaceSummary;
		workspaceQueryMock.mockReturnValue({ data: [workspace], isError: false, isSuccess: true });
		memoryQueryMock.mockReturnValue({
			isError: false,
			data: new Map([
				["s-small", reading("s-small", 641_728_512, 5, [], 7)],
				["s-big", reading("s-big", 2_254_857_830, 9, [], 82)],
			]),
		});
		renderButton();
		await userEvent.click(screen.getByTestId("app-memory-indicator"));
		const rows = within(await screen.findByTestId("session-memory-table")).getAllByTestId("session-memory-row");
		expect(rows[0]).toHaveTextContent("82%");
		// s-small is idle, not "working" — its measured CPU still shows, not hidden.
		expect(rows[1]).toHaveTextContent("7%");
	});

	it("colours idle rows as part of the fix while the machine is getting tight", async () => {
		appMemoryMock.mockReturnValue(appReading(3, 12, 20));
		renderButton();
		await userEvent.click(screen.getByTestId("app-memory-indicator"));
		const table = await screen.findByTestId("session-memory-table");
		expect(screen.getByTestId("session-memory-suggestion")).toHaveTextContent("big worker is using the most memory");
		const rows = within(table).getAllByTestId("session-memory-row");
		expect(rows.every((row) => row.getAttribute("data-chip-tone") === "warning")).toBe(true);
		expect(screen.queryByTestId("session-memory-fix")).not.toBeInTheDocument();
	});

	it("never names an unmeasured session as the one using the most memory", async () => {
		const workspace: WorkspaceSummary = {
			id: "p1",
			name: "radic",
			sessions: [session("s-none", "unsampled worker")],
		} as WorkspaceSummary;
		workspaceQueryMock.mockReturnValue({ data: [workspace], isError: false, isSuccess: true });
		memoryQueryMock.mockReturnValue({ isError: false, data: new Map() });
		appMemoryMock.mockReturnValue(appReading(3, 12, 20));
		renderButton();
		await userEvent.click(screen.getByTestId("app-memory-indicator"));
		await screen.findByTestId("session-memory-table");
		expect(screen.queryByTestId("session-memory-suggestion")).not.toBeInTheDocument();
	});

	it("marks the single largest session red when the machine is tight and everything is busy", async () => {
		const workspace: WorkspaceSummary = {
			id: "p1",
			name: "radic",
			sessions: [session("s-big", "big worker", "active"), session("s-small", "small worker", "active")],
		} as WorkspaceSummary;
		workspaceQueryMock.mockReturnValue({ data: [workspace], isError: false, isSuccess: true });
		appMemoryMock.mockReturnValue(appReading(1, 40, 20));
		renderButton();
		await userEvent.click(screen.getByTestId("app-memory-indicator"));
		const rows = within(await screen.findByTestId("session-memory-table")).getAllByTestId("session-memory-row");
		expect(rows[0]).toHaveAttribute("data-chip-tone", "critical");
		expect(rows[1]).toHaveAttribute("data-chip-tone", "neutral");
		expect(screen.getByTestId("session-memory-suggestion")).toHaveTextContent("big worker is using the most memory");
	});

	it("says what the agent is doing on one line, and lists its last steps when expanded", async () => {
		const workspace: WorkspaceSummary = {
			id: "p1",
			name: "radic",
			sessions: [session("s-big", "big worker", "active"), session("s-small", "small worker")],
		} as WorkspaceSummary;
		workspaceQueryMock.mockReturnValue({ data: [workspace], isError: false, isSuccess: true });
		const startedAt = new Date(Date.now() - 38_000).toISOString();
		const step = (i: number, tool: string, failed = false) => ({
			tool,
			startedAt: new Date(Date.now() - (i + 2) * 60_000).toISOString(),
			endedAt: new Date(Date.now() - (i + 2) * 60_000 + 4_000).toISOString(),
			failed,
		});
		memoryQueryMock.mockReturnValue({
			isError: false,
			data: new Map<string, SessionMemoryReading>([
				["s-big", {
					...reading("s-big", 2_254_857_830, 9),
					activity: {
						current: { tool: "Bash", startedAt, failed: false },
						recent: [step(0, "Edit"), step(1, "Read"), step(2, "Bash", true)],
					},
				}],
				["s-small", reading("s-small", 641_728_512, 5)],
			]),
		});
		renderButton();
		await userEvent.click(screen.getByTestId("app-memory-indicator"));
		const table = await screen.findByTestId("session-memory-table");
		const rows = within(table).getAllByTestId("session-memory-row");
		expect(within(rows[0]).getByTestId("session-memory-status")).toHaveTextContent("Bash · 38s");
		// Idle for two hours reads as such; a session with no steps expands to nothing.
		expect(within(rows[1]).getByTestId("session-memory-status")).toHaveTextContent("idle · 2h");
		expect(rows[1]).not.toHaveAttribute("aria-expanded");

		await userEvent.click(rows[0]);
		const steps = await screen.findAllByTestId("session-memory-step-row");
		expect(steps).toHaveLength(3);
		expect(steps[0]).toHaveTextContent("Edit");
		expect(steps[0]).toHaveTextContent("4s");
		expect(steps[2]).toHaveTextContent("Bash");
		expect(steps[2]).toHaveTextContent("failed");
	});

	it("copies the whole window as a report worth pasting into a bug", async () => {
		const workspace: WorkspaceSummary = {
			id: "p1",
			name: "radic",
			sessions: [
				{
					...session("s-big", "big worker", "active"),
					kind: "worker",
					branch: "ao/big-worker",
					displayStatus: "Working",
					prs: [{ number: 5137, url: "https://github.com/org/repo/pull/5137", state: "open", ci: "passing", review: "", mergeability: "", reviewComments: false, updatedAt: "" }],
				},
				session("s-small", "small worker"),
			],
		} as WorkspaceSummary;
		workspaceQueryMock.mockReturnValue({ data: [workspace], isError: false, isSuccess: true });
		usageQueryMock.mockReturnValue({
			data: new Map([["s-big", { sessionId: "s-big", estimatedCost: { totalNanos: 5_460_000_000, coverage: "complete", providerAttribution: "observed", inputNanos: null, outputNanos: null, cachedInputNanos: null }, processedTokens: 412_000, totalTokens: 412_000, incomplete: false }]]),
		});
		const startedAt = new Date(Date.now() - 38_000).toISOString();
		memoryQueryMock.mockReturnValue({
			isError: false,
			data: new Map<string, SessionMemoryReading>([
				["s-big", {
					...reading("s-big", 2_254_857_830, 2, [
						{ pid: 111, ppid: 1, rssBytes: 1_800_000_000, cpuPercent: 80.4, command: "/usr/bin/claude --flag" },
						{ pid: 222, ppid: 111, rssBytes: 454_857_830, cpuPercent: 1.6, command: "sh -c go test ./..." },
					], 82),
					activity: {
						current: { tool: "Bash", startedAt, failed: false },
						recent: [{ tool: "Edit", startedAt: "2026-09-18T00:00:00.000Z", endedAt: "2026-09-18T00:00:04.000Z", failed: false }],
					},
				}],
				["s-small", reading("s-small", 641_728_512, 1, [{ pid: 333, ppid: 1, rssBytes: 641_728_512, cpuPercent: 0, command: "/usr/bin/claude" }])],
			]),
		});
		renderButton();
		await userEvent.click(screen.getByTestId("app-memory-indicator"));
		await screen.findByTestId("session-memory-table");
		await userEvent.click(screen.getByRole("button", { name: "Copy report" }));
		const report = clipboardMock.mock.calls[0][0] as string;
		// The machine first, then every session on screen — not just one.
		expect(report).toContain("Memory   AO 2.1 GB · 21.5 GB free of 34.4 GB");
		expect(report).toContain("Sessions 2");
		expect(report).toContain("big worker");
		expect(report).toContain("small worker");
		expect(report).toContain("radic · worker · claude-code");
		expect(report).toContain("Status   Working · Bash · 38s");
		expect(report).toContain("Branch   ao/big-worker");
		expect(report).toContain("PR       #5137 · open · CI passing");
		expect(report).toContain("https://github.com/org/repo/pull/5137");
		expect(report).toContain("Usage    $5.46 · 412K tok");
		expect(report).toContain("Memory   2.3 GB · CPU 82%");
		expect(report).toMatch(/claude\s+1.8 GB\s+80%/);
		// The child keeps its indent, and the tree is not truncated the way the screen truncates it.
		expect(report).toMatch(/ {2}sh\s+455 MB\s+2%/);
		// A process id means nothing to whoever reads the report; a tool's arguments may carry a path.
		expect(report).not.toContain("111");
		expect(report).not.toContain("go test");
		expect(report).toContain("Edit");
		expect(await screen.findByRole("button", { name: "Report copied" })).toBeInTheDocument();
	});

	it("keeps the plain working/idle line for a harness that reports no steps", async () => {
		renderButton();
		await userEvent.click(screen.getByTestId("app-memory-indicator"));
		const rows = within(await screen.findByTestId("session-memory-table")).getAllByTestId("session-memory-row");
		expect(within(rows[0]).getByTestId("session-memory-status")).toHaveTextContent("idle · 2h");
		expect(screen.queryByText("Recent")).not.toBeInTheDocument();
	});
});

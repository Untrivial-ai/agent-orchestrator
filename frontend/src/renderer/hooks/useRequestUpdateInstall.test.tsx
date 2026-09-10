import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useRequestUpdateInstall } from "./useRequestUpdateInstall";
import { workspaceQueryOptions } from "./useWorkspaceQuery";

const { install, openPrompt, fetchWorkspaces } = vi.hoisted(() => ({ install: vi.fn(), openPrompt: vi.fn(), fetchWorkspaces: vi.fn() }));

vi.mock("../lib/bridge", () => ({ aoBridge: { updates: { install } } }));
vi.mock("../stores/ui-store", () => ({
	useUiStore: (select: (state: { openUpdateInstallPrompt: () => void }) => unknown) =>
		select({ openUpdateInstallPrompt: openPrompt }),
}));

vi.mock("./useWorkspaceQuery", () => ({ workspaceQueryOptions: { queryKey: ["workspaces"], queryFn: fetchWorkspaces, staleTime: 10_000 } }));

// Which sessions count as at-risk is update-install-risk.test.ts's job (6 cases
// there). These only need one of each shape to drive the branch below: a chat
// session mid-turn on a daemon-owned driver is at risk, a TUI session is not.
const session = (mode: "chat" | "tui") => ({
	id: mode,
	title: "Session",
	workspaceName: "repo",
	provider: "claude-code",
	mode,
	status: "working",
});

function renderTrigger(seed?: unknown, configure?: (client: QueryClient) => void) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	if (seed !== undefined) { client.setQueryData(workspaceQueryOptions.queryKey, seed); fetchWorkspaces.mockResolvedValue(seed); }
	configure?.(client);
	let trigger: () => void = () => undefined;
	function Probe() {
		trigger = useRequestUpdateInstall();
		return null;
	}
	const wrapper = ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={client}>{children}</QueryClientProvider>
	);
	render(<Probe />, { wrapper });
	return async () => { await act(async () => { trigger(); await Promise.resolve(); }); };
}

beforeEach(() => {
	install.mockReset();
	openPrompt.mockReset();
	fetchWorkspaces.mockReset().mockRejectedValue(new Error("daemon unavailable"));
});

describe("useRequestUpdateInstall", () => {
	it("installs directly when nothing would lose a turn", async () => {
		// The confirmation existed to warn about lost work. With nothing to warn
		// about it is a modal on top of the Settings modal saying nothing, and the
		// build installs on the next quit anyway.
		await renderTrigger([{ sessions: [session("tui")] }])();
		expect(install).toHaveBeenCalledTimes(1);
		expect(openPrompt).not.toHaveBeenCalled();
	});

	it("confirms when a session would lose an in-flight turn", async () => {
		await renderTrigger([{ sessions: [session("tui"), session("chat")] }])();
		expect(openPrompt).toHaveBeenCalledTimes(1);
		expect(install).not.toHaveBeenCalled();
	});

	it("confirms when the workspace list has not resolved", async () => {
		// Unknown is not the same as safe: AO cannot rule out a live turn, so it
		// asks rather than quitting out from under one.
		await renderTrigger()();
		expect(openPrompt).toHaveBeenCalledTimes(1);
		expect(install).not.toHaveBeenCalled();
	});
});

describe("restart safety with uncertain workspace data", () => {
	it("confirms for a chat turn waiting for approval", async () => {
		await renderTrigger([{ sessions: [{ ...session("chat"), status: "needs_input" }] }])();
		expect(openPrompt).toHaveBeenCalledTimes(1);
		expect(install).not.toHaveBeenCalled();
	});
	it("refreshes a stale safe snapshot before installing", async () => {
		await renderTrigger([], (client) => {
			client.setQueryData(workspaceQueryOptions.queryKey, [], { updatedAt: Date.now() - 60_000 });
		})();
		expect(openPrompt).not.toHaveBeenCalled();
		expect(install).toHaveBeenCalledTimes(1);
		expect(fetchWorkspaces).toHaveBeenCalledTimes(1);
	});
	it("refreshes invalidated safe data before installing", async () => {
		await renderTrigger([], (client) => {
			void client.invalidateQueries({ queryKey: workspaceQueryOptions.queryKey, refetchType: "none" });
		})();
		expect(openPrompt).not.toHaveBeenCalled();
		expect(install).toHaveBeenCalledTimes(1);
		expect(fetchWorkspaces).toHaveBeenCalledTimes(1);
	});
	it("waits for a pending fresh snapshot", async () => {
		let resolve!: (value: never[]) => void;
		let pending!: Promise<never[]>;
		const trigger = renderTrigger([], (client) => {
			pending = client.fetchQuery({
				queryKey: workspaceQueryOptions.queryKey,
				queryFn: () => new Promise<never[]>((done) => { resolve = done; }),
				staleTime: 0,
			});
		});
		const clicked = trigger();
		expect(install).not.toHaveBeenCalled();
		resolve([]);
		await pending;
		await clicked;
		expect(openPrompt).not.toHaveBeenCalled();
		expect(install).toHaveBeenCalledTimes(1);
	});
	it("confirms when a refresh failed but previous data remains", async () => {
		let client!: QueryClient;
		const trigger = renderTrigger([], (current) => { client = current; });
		await client.fetchQuery({
			queryKey: workspaceQueryOptions.queryKey,
			queryFn: async () => { throw new Error("daemon unavailable"); },
			staleTime: 0,
		}).catch(() => undefined);
		fetchWorkspaces.mockRejectedValue(new Error("still unavailable"));
		await trigger();
		expect(openPrompt).toHaveBeenCalledTimes(1);
		expect(install).not.toHaveBeenCalled();
	});
	it("installs directly with a fresh empty snapshot", async () => {
		await renderTrigger([])();
		expect(install).toHaveBeenCalledTimes(1);
		expect(openPrompt).not.toHaveBeenCalled();
	});
});

it("refreshes even a fresh safe cache and warns about newly working chat", async () => {
	const trigger = renderTrigger([]);
	fetchWorkspaces.mockResolvedValue([{ sessions: [session("chat")] }]);
	await trigger();
	expect(fetchWorkspaces).toHaveBeenCalledTimes(1);
	expect(openPrompt).toHaveBeenCalledTimes(1);
	expect(install).not.toHaveBeenCalled();
});

it("installs when a previously risky worker has finished", async () => {
	const trigger = renderTrigger([{ sessions: [session("chat")] }]);
	fetchWorkspaces.mockResolvedValue([]);
	await trigger();
	expect(install).toHaveBeenCalledTimes(1);
	expect(openPrompt).not.toHaveBeenCalled();
});

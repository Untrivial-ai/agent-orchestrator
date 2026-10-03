import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const { localGet, remoteConnect } = vi.hoisted(() => ({ localGet: vi.fn(), remoteConnect: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: localGet }, hasTrustedApiBaseUrl: () => true }));
vi.mock("../lib/bridge", () => ({ aoBridge: { remotes: { connect: remoteConnect, disconnect: vi.fn() } } }));
vi.mock("../lib/telemetry", () => ({ captureRendererEvent: vi.fn() }));
vi.mock("../lib/agent-switch-visibility", () => ({ agentSwitchVisibility: { setQueryHealthy: vi.fn() } }));
vi.mock("./useCloudCp", () => ({ useCloudCp: () => ({ ready: false, baseUrl: "", client: {} }) }));
vi.mock("./useCloudOrg", () => ({ useCloudOrg: () => ({ org: undefined, ready: false }) }));

import { connectHost, connectedHosts, disconnectHost } from "../lib/host-clients";
import { useUiStore } from "../stores/ui-store";
import { remoteWorkspaceQueryKey, useRemoteWorkspaces, useWorkspaceQuery, useWorkspaceSession } from "./useWorkspaceQuery";

function wrapper({ children }: { children: ReactNode }) {
	return <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{children}</QueryClientProvider>;
}

beforeEach(() => useUiStore.setState({ developerMode: true, remoteHosts: true }));

afterEach(async () => {
	useUiStore.setState({ developerMode: false, remoteHosts: false });
	for (const hostId of connectedHosts()) await disconnectHost(hostId);
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

async function prepareTwoHosts() {
	localGet.mockImplementation(async (path: string) => path === "/api/v1/projects"
		? { data: { projects: [{ id: "project-1", name: "Local", path: "/local" }] } }
		: { data: { sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "working", prs: [] }] } });
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const url = input instanceof Request ? input.url : String(input);
		return new Response(JSON.stringify(url.endsWith("/projects")
			? { projects: [{ id: "project-1", name: "Remote", path: "/remote" }] }
			: { sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "working", prs: [] }] }),
		{ status: 200, headers: { "content-type": "application/json" } });
	}));
	await connectHost("http://box-a:3001");
}

it("lists remote projects separately so local actions cannot target a same-named remote session", async () => {
	await prepareTwoHosts();
	const { result } = renderHook(() => ({ local: useWorkspaceQuery(), remote: useRemoteWorkspaces() }), { wrapper });
	await waitFor(() => expect(result.current.remote.data).toHaveLength(1));
	expect(result.current.local.data?.map((project) => project.name)).toEqual(["Local"]);
	expect(result.current.remote.data?.map((project) => [project.name, project.sessions[0]?.hostId])).toEqual([["Remote", "box-a"]]);
});

it("reports a failed remote query instead of treating the host as empty and healthy", async () => {
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	vi.stubGlobal("fetch", vi.fn(async () => new Response('{"error":"unavailable"}', {
		status: 500,
		headers: { "content-type": "application/json" },
	})));
	await connectHost("http://box-a:3001");
	const { result } = renderHook(() => useRemoteWorkspaces(), { wrapper });
	await waitFor(() => expect(result.current.failedHostIds).toEqual(["box-a"]), { timeout: 3000 });
	expect(result.current.data).toEqual([]);
	expect(result.current.loadedProjectHostIds).toEqual([]);
});

it.each([
	[401, true],
	[426, true],
	[502, true],
	[500, false],
])("rechecks a connected host after a remote HTTP %i response only when connection health is in doubt", async (status, shouldRecheck) => {
	const hostId = `box-${status}`;
	remoteConnect.mockResolvedValue({ hostId, label: "Box", url: "http://box:3001", base: "http://127.0.0.1:4000" });
	vi.stubGlobal("fetch", vi.fn(async () => Response.json({ error: "failed" }, { status })));
	await connectHost("http://box:3001");
	const dispatched = vi.spyOn(window, "dispatchEvent");
	const { result } = renderHook(() => useWorkspaceSession("session-1", hostId), { wrapper });
	await waitFor(() => expect(result.current.isError).toBe(true));
	expect(dispatched.mock.calls.some(([event]) => event.type === "ao:remote-hosts-changed")).toBe(shouldRecheck);
});

it("coalesces simultaneous and repeated remote request failures into one host recheck", async () => {
	remoteConnect.mockResolvedValue({ hostId: "box-gate", label: "Box", url: "http://box:3001", base: "http://127.0.0.1:4000" });
	vi.stubGlobal("fetch", vi.fn(async () => Response.json({ error: "unauthorized", code: "BAD_PASSWORD" }, { status: 401 })));
	await connectHost("http://box:3001");
	const now = vi.spyOn(Date, "now").mockReturnValue(1_000_000);
	const dispatched = vi.spyOn(window, "dispatchEvent");
	const { result } = renderHook(() => useRemoteWorkspaces(), { wrapper });
	await waitFor(() => expect(result.current.failedHostIds).toEqual(["box-gate"]), { timeout: 3000 });
	const refreshCount = () => dispatched.mock.calls.filter(([event]) => event.type === "ao:remote-hosts-changed").length;
	expect(refreshCount()).toBe(1);
	await act(async () => { await result.current.refetch(); });
	expect(refreshCount()).toBe(1);
	now.mockReturnValue(1_015_000);
	await act(async () => { await result.current.refetch(); });
	expect(refreshCount()).toBe(2);
});

it("keeps a host's registered projects visible when its sessions request fails", async () => {
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	let sessionsFail = true;
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const url = input instanceof Request ? input.url : String(input);
		const isProjects = url.endsWith("/projects");
		return new Response(JSON.stringify(isProjects
			? { projects: [{ id: "project-1", name: "Remote", path: "/remote" }] }
			: sessionsFail ? { error: "unavailable" } : { sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "working", prs: [] }] }), {
			status: !isProjects && sessionsFail ? 500 : 200,
			headers: { "content-type": "application/json" },
		});
	}));
	await connectHost("http://box-a:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const { result } = renderHook(() => useRemoteWorkspaces(), {
		wrapper: ({ children }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>,
	});

	await waitFor(() => expect(result.current.failedHostIds).toEqual(["box-a"]), { timeout: 3000 });
	expect(result.current.loadedProjectHostIds).toEqual(["box-a"]);
	expect(result.current.data).toEqual([expect.objectContaining({
		hostId: "box-a", id: "project-1", name: "Remote", sessions: [],
	})]);

	sessionsFail = false;
	await act(async () => { await queryClient.invalidateQueries({ queryKey: remoteWorkspaceQueryKey("box-a") }); });
	await waitFor(() => expect(result.current.failedHostIds).toEqual([]));
	expect(result.current.data[0]?.sessions).toEqual([expect.objectContaining({ id: "session-1", hostId: "box-a" })]);

	sessionsFail = true;
	await act(async () => { await queryClient.invalidateQueries({ queryKey: remoteWorkspaceQueryKey("box-a") }); });
	await waitFor(() => expect(result.current.failedHostIds).toEqual(["box-a"]), { timeout: 3000 });
	expect(result.current.data[0]).toEqual(expect.objectContaining({ hostId: "box-a", id: "project-1" }));
});

import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, expect, it, vi } from "vitest";
import { TooltipProvider } from "./ui/tooltip";

const mocks = vi.hoisted(() => ({
	connected: [] as string[],
	listeners: new Set<() => void>(),
	get: vi.fn(),
	post: vi.fn(),
	put: vi.fn(),
	localGet: vi.fn(),
	localPost: vi.fn(),
	localPut: vi.fn(),
}));

vi.mock("../lib/host-clients", () => {
	const clientForHost = (hostId: string) => {
		if (!mocks.connected.includes(hostId)) throw new Error(`Host ${hostId} is not connected`);
		return {
			GET: (path: string, options?: unknown) => mocks.get(hostId, path, options),
			POST: (path: string, options?: unknown) => mocks.post(hostId, path, options),
			PUT: (path: string, options?: unknown) => mocks.put(hostId, path, options),
		};
	};
	return {
		connectedHosts: () => mocks.connected,
		subscribeConnectedHosts: (listener: () => void) => {
			mocks.listeners.add(listener);
			return () => mocks.listeners.delete(listener);
		},
		clientForHost,
		clientForSessionHost: (hostId?: string) => hostId ? clientForHost(hostId) : { GET: mocks.localGet, POST: mocks.localPost, PUT: mocks.localPut },
	};
});
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: mocks.localGet, POST: mocks.localPost, PUT: mocks.localPut },
	apiErrorCode: () => undefined,
	apiErrorDetails: () => undefined,
	apiErrorMessage: (error: { message?: string }) => error.message ?? "Request failed",
	apiErrorRequestId: () => undefined,
	hasTrustedApiBaseUrl: () => true,
}));
vi.mock("../lib/telemetry", () => ({ captureRendererEvent: vi.fn() }));
vi.mock("../lib/orchestrator-replacement-telemetry", () => ({ captureOrchestratorReplacementFailure: vi.fn() }));
vi.mock("../stores/ui-store", () => ({
	useUiStore: (selector: (state: Record<string, unknown>) => unknown) => selector({
		developerMode: true,
		remoteHosts: true,
		setOrchestratorReplacementError: vi.fn(),
		openGlobalSettings: vi.fn(),
	}),
}));

import { ProjectSettingsForm, type ProjectSettingsSaveState } from "./ProjectSettingsForm";

const projects = new Map([
	["box-a", { id: "shared", name: "Alpha", kind: "single_repo", path: "/srv/alpha", repo: "/srv/alpha.git", config: { worker: { agent: "codex" }, orchestrator: { agent: "claude-code" } } }],
	["box-b", { id: "shared", name: "Beta", kind: "single_repo", path: "/srv/beta", repo: "", config: { worker: { agent: "codex" }, orchestrator: { agent: "claude-code" } } }],
]);
const agents = { agents: [
	{ id: "claude-code", label: "Claude Code", effectiveReadiness: "ready", installation: { state: "installed", freshness: "fresh" }, authentication: { state: "authorized", freshness: "fresh" }, usageCount: 0 },
	{ id: "codex", label: "Codex", effectiveReadiness: "ready", installation: { state: "installed", freshness: "fresh" }, authentication: { state: "authorized", freshness: "fresh" }, usageCount: 0 },
] };

function connect(...hostIds: string[]) {
	act(() => {
		mocks.connected = hostIds;
		for (const listener of mocks.listeners) listener();
	});
}

function renderForm(hostId: string, section: "general" | "agents" = "general", onSaveState = vi.fn()) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	const view = (host: string) => <QueryClientProvider client={queryClient}><TooltipProvider>
		<ProjectSettingsForm hostId={host} projectId="shared" section={section} onSaveState={onSaveState} />
	</TooltipProvider></QueryClientProvider>;
	const result = render(view(hostId));
	return { ...result, queryClient, rerenderHost: (host: string) => result.rerender(view(host)) };
}

beforeEach(() => {
	mocks.connected = [];
	mocks.listeners.clear();
	for (const mock of [mocks.get, mocks.post, mocks.put, mocks.localGet, mocks.localPost, mocks.localPut]) mock.mockReset();
	mocks.get.mockImplementation(async (hostId: string, path: string) => {
		const project = projects.get(hostId);
		if (!project) throw new Error(`Unknown host ${hostId}`);
		if (path === "/api/v1/projects/{id}") return { data: { status: "ok", project } };
		if (path === "/api/v1/projects") return { data: { projects: [{ id: project.id, name: project.name, path: project.path, kind: project.kind, orchestratorAgent: project.config.orchestrator.agent }] } };
		if (path === "/api/v1/sessions") return { data: { sessions: [] } };
		if (path === "/api/v1/agents/readiness") return { data: agents };
		if (path === "/api/v1/agents/{agent}/models") return { data: { agentId: "codex", selectionMode: "text", models: [], source: "manual", fetchedAt: "2026-09-28T00:00:00Z", stale: false } };
		throw new Error(`Unexpected GET ${path}`);
	});
	mocks.post.mockImplementation(async (_hostId: string, path: string) => path === "/api/v1/agents/readiness/ensure"
		? { data: agents }
		: { data: { orchestrator: { id: "new-orchestrator" } } });
	mocks.put.mockResolvedValue({ data: { project: {} } });
});

it("keeps same-ID projects and their paths on separate hosts", async () => {
	connect("box-a", "box-b");
	const { queryClient, rerenderHost } = renderForm("box-a");
	expect(await screen.findByText("Alpha")).toBeVisible();
	await waitFor(() => expect(mocks.post).toHaveBeenCalledWith("box-a", "/api/v1/agents/readiness/ensure", {
		body: { agentIds: [], purpose: "display" },
	}));
	expect(screen.getByText("/srv/alpha").closest("a")).toBeNull();
	expect(screen.getByText("/srv/alpha.git").closest("a")).toBeNull();
	rerenderHost("box-b");
	expect(await screen.findByText("Beta")).toBeVisible();
	expect(screen.getByText("/srv/beta").closest("a")).toBeNull();
	expect(queryClient.getQueryData(["project", "box-a", "shared"])).toMatchObject({ name: "Alpha" });
	expect(queryClient.getQueryData(["project", "box-b", "shared"])).toMatchObject({ name: "Beta" });
	expect(mocks.localGet).not.toHaveBeenCalled();
});

it("saves the full Project form on its owning host", async () => {
	connect("box-b");
	const { queryClient } = renderForm("box-b");
	const invalidate = vi.spyOn(queryClient, "invalidateQueries");
	await userEvent.click(await screen.findByRole("button", { name: "Edit Project name" }));
	const name = screen.getByRole("textbox", { name: "Project name" });
	await userEvent.clear(name);
	await userEvent.type(name, "Renamed Beta");
	fireEvent.submit(document.getElementById("project-settings-form")!);
	await waitFor(() => expect(mocks.put).toHaveBeenCalledWith("box-b", "/api/v1/projects/{id}", {
		params: { path: { id: "shared" } },
		body: { displayName: "Renamed Beta", config: expect.objectContaining({ worker: { agent: "codex" }, orchestrator: { agent: "claude-code" } }) },
	}));
	expect(mocks.localPut).not.toHaveBeenCalled();
	await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: ["project-config", "box-b", "shared"] }));
});

it("replaces and retries the orchestrator on the selected host", async () => {
	connect("box-b");
	let attempts = 0;
	mocks.post.mockImplementation(async (_hostId: string, path: string) => {
		if (path === "/api/v1/agents/readiness/ensure") return { data: agents };
		attempts += 1;
		return attempts === 1 ? { error: { message: "startup failed" }, response: { status: 500 } } : { data: { orchestrator: { id: "new-orchestrator" } } };
	});
	const onSaveState = vi.fn<(state: ProjectSettingsSaveState) => void>();
	renderForm("box-b", "agents", onSaveState);
	await userEvent.click(await screen.findByRole("button", { name: "Orchestrator agent" }));
	await userEvent.click(await screen.findByRole("menuitem", { name: /^Codex/ }));
	fireEvent.submit(document.getElementById("project-settings-form")!);
	await waitFor(() => expect(onSaveState.mock.lastCall?.[0].replacementError).toBe("startup failed"));
	expect(mocks.post).toHaveBeenCalledWith("box-b", "/api/v1/orchestrators", { body: { projectId: "shared", clean: true } });
	fireEvent.submit(document.getElementById("project-settings-form")!);
	await waitFor(() => expect(attempts).toBe(2));
	expect(mocks.localPost).not.toHaveBeenCalled();
});

it("shows offline state without contacting the local daemon", () => {
	renderForm("box-b");
	expect(screen.getByRole("alert")).toHaveTextContent("Host is offline");
	expect(mocks.get).not.toHaveBeenCalled();
	expect(mocks.put).not.toHaveBeenCalled();
	expect(mocks.localGet).not.toHaveBeenCalled();
});

it("keeps an unsaved remote draft through disconnect and saves after reconnect", async () => {
	connect("box-b");
	renderForm("box-b");
	await userEvent.click(await screen.findByRole("button", { name: "Edit Project name" }));
	const name = screen.getByRole("textbox", { name: "Project name" });
	await userEvent.clear(name);
	await userEvent.type(name, "Draft Beta");
	connect();
	expect(screen.getByRole("alert")).toHaveTextContent("Host is offline");
	expect(mocks.put).not.toHaveBeenCalled();
	connect("box-b");
	expect(screen.getByRole("textbox", { name: "Project name" })).toHaveValue("Draft Beta");
	await waitFor(() => expect(mocks.put).toHaveBeenCalledWith("box-b", "/api/v1/projects/{id}", expect.objectContaining({ body: expect.objectContaining({ displayName: "Draft Beta" }) })));
});

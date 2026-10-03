import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { agentReadinessQueryKey, agentReadinessQueryKeyForHost, type AgentReadiness } from "../../hooks/useAgentReadinessQuery";
import { shellTerminalsQueryKeyForHost, type ShellTerminal } from "../../hooks/useShellTerminals";
import { appI18n } from "../../i18n";
import { apiClient } from "../../lib/api-client";
import { aoBridge } from "../../lib/bridge";
import { connectHost, disconnectHost } from "../../lib/host-clients";
import { useUiStore } from "../../stores/ui-store";
import { HarnessSettingsSection } from "./HarnessSettingsSection";

const terminal = vi.hoisted(() => ({ createMux: undefined as undefined | (() => { dispose: () => void }), target: undefined as unknown }));
class FakeWebSocket extends EventTarget {
	static readonly OPEN = 1;
	static urls: string[] = [];
	readyState = 0;
	constructor(readonly url: string) {
		super();
		FakeWebSocket.urls.push(url);
	}
	send(): void {}
	close(): void { this.readyState = 3; }
}
vi.mock("../TerminalPane", () => ({
	TerminalPane: ({ createMux, terminalTarget }: { createMux?: () => { dispose: () => void }; terminalTarget: unknown }) => {
		terminal.createMux = createMux;
		terminal.target = terminalTarget;
		return <div data-testid="remote-auth-terminal" />;
	},
}));

const readiness = (installed: string[], authorized = false) => ({
	agents: ["claude-code", "codex"].map((id) => ({
		id, label: id, installation: { state: installed.includes(id) ? "installed" : "not_installed", freshness: "fresh" },
		authentication: { state: id === "claude-code" && authorized ? "authorized" : "unauthorized", freshness: "fresh" },
		effectiveReadiness: installed.includes(id) ? "ready" : "not_ready", usageCount: 0,
	})),
});

beforeEach(() => useUiStore.setState({ developerMode: true, remoteHosts: true }));

afterEach(async () => {
	useUiStore.setState({ developerMode: false, remoteHosts: false });
	await disconnectHost("box-a");
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
	terminal.createMux = undefined;
	terminal.target = undefined;
	FakeWebSocket.urls = [];
});

it("routes installer, login, readiness, terminal, and cleanup to the selected host without falling back on disconnect", async () => {
	await appI18n.changeLanguage("en");
	const localReadiness = readiness(["claude-code", "codex"]);
	const remoteReadiness = readiness(["claude-code"]);
	const remoteAuthorized = readiness(["claude-code"], true);
	const plan = { agentId: "codex", available: true, method: "npm", methods: [{ id: "npm", label: "npm", available: true, recommended: true }] };
	vi.spyOn(apiClient, "GET").mockImplementation(async (path) => {
		if (path === "/api/v1/agents/readiness") return { data: localReadiness } as never;
		if (path === "/api/v1/agents/installers") return { data: { agents: [plan] } } as never;
		if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
		if (path === "/api/v1/agents/auth-plans") return { data: { plans: [] } } as never;
		return { data: {} } as never;
	});
	vi.spyOn(apiClient, "POST").mockResolvedValue({ data: localReadiness } as never);
	const localDelete = vi.spyOn(apiClient, "DELETE").mockResolvedValue({ data: undefined } as never);
	vi.spyOn(aoBridge.remotes, "connect").mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4500/token-a" });
	vi.spyOn(aoBridge.remotes, "disconnect").mockResolvedValue(undefined);
	vi.stubGlobal("WebSocket", FakeWebSocket);
	const calls: string[] = [];
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const path = new URL(request.url).pathname;
		calls.push(`${request.method} ${path}`);
		if (path.endsWith("/agents/readiness")) return Response.json(remoteReadiness);
		if (path.endsWith("/agents/readiness/ensure")) return Response.json(remoteAuthorized);
		if (path.endsWith("/agents/installers")) return Response.json({ agents: [plan] });
		if (path.endsWith("/agents/install-jobs")) return Response.json({ jobs: [] });
		if (path.endsWith("/agents/auth-plans")) return Response.json({ plans: [{ agentId: "claude-code", action: "login", launchMode: "terminal", available: true }] });
		if (path.endsWith("/agents/codex/install")) return Response.json({ target: "codex", status: "failed", error: "install failed" }, { status: 202 });
		if (path.endsWith("/agents/claude-code/auth")) return Response.json({ agentId: "claude-code", action: "login", terminal: { handleId: "auth-a", workingDir: "/host-a", title: "Log in", createdAt: "2026-09-29T00:00:00Z" } }, { status: 201 });
		if (path.endsWith("/agents/claude-code/probe")) return Response.json({ agent: { id: "claude-code", authStatus: "authorized" } });
		if (request.method === "DELETE" && path.endsWith("/shell-terminals/auth-a")) return new Response(null, { status: 204 });
		return Response.json(remoteReadiness);
	}));
	await connectHost("http://box-a:3001");
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const view = render(<QueryClientProvider client={client}><HarnessSettingsSection hostId="box-a" /></QueryClientProvider>);
	expect(screen.getByRole("button", { name: "Host" })).toHaveTextContent("Box A");
	const codex = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;
	await userEvent.click(await within(codex).findByRole("button", { name: "Install" }));
	await waitFor(() => expect(calls).toContain("POST /token-a/api/v1/agents/codex/install"));
	const claude = (await screen.findByText("Claude Code")).closest('[data-agent="claude-code"]') as HTMLElement;
	await userEvent.click(await within(claude).findByRole("button", { name: "Login" }));
	await within(claude).findByTestId("remote-auth-terminal");
	expect(terminal.target).toMatchObject({ kind: "shell", handleId: "auth-a", generation: "2026-09-29T00:00:00Z" });
	expect(terminal.createMux).toBeTypeOf("function");
	const mux = terminal.createMux?.();
	expect(FakeWebSocket.urls).toEqual(["ws://127.0.0.1:4500/token-a/mux"]);
	mux?.dispose();
	await userEvent.click(within(claude).getByRole("button", { name: "Close settings" }));
	await waitFor(() => expect(calls).toContain("DELETE /token-a/api/v1/shell-terminals/auth-a"));
	await waitFor(() => expect(calls).toContain("POST /token-a/api/v1/agents/claude-code/probe"));
	expect(localDelete).not.toHaveBeenCalled();
	expect(apiClient.POST).not.toHaveBeenCalled();
	expect(client.getQueryData(agentReadinessQueryKey)).toBeUndefined();
	expect(client.getQueryData<AgentReadiness>(agentReadinessQueryKeyForHost("box-a"))?.agents[1].installation.state).toBe("not_installed");
	const localGetCount = vi.mocked(apiClient.GET).mock.calls.length;
	const localPostCount = vi.mocked(apiClient.POST).mock.calls.length;
	await act(async () => { await disconnectHost("box-a"); });
	expect(screen.getByRole("alert")).toHaveTextContent("Host is offline");
	expect(screen.queryByText("Codex")).not.toBeInTheDocument();
	expect(vi.mocked(apiClient.GET).mock.calls).toHaveLength(localGetCount);
	expect(vi.mocked(apiClient.POST).mock.calls).toHaveLength(localPostCount);
	view.unmount();
});

it("closes a remote login terminal returned after switching away from its host", async () => {
	await appI18n.changeLanguage("en");
	const catalog = readiness(["claude-code"]);
	vi.spyOn(apiClient, "GET").mockImplementation(async (path) => {
		if (path === "/api/v1/agents/readiness") return { data: catalog } as never;
		if (path === "/api/v1/agents/installers") return { data: { agents: [] } } as never;
		if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
		if (path === "/api/v1/agents/auth-plans") return { data: { plans: [] } } as never;
		return { data: {} } as never;
	});
	vi.spyOn(apiClient, "POST").mockResolvedValue({ data: catalog } as never);
	const localDelete = vi.spyOn(apiClient, "DELETE").mockResolvedValue({ data: undefined } as never);
	vi.spyOn(aoBridge.remotes, "connect").mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4500/token-a" });
	vi.spyOn(aoBridge.remotes, "disconnect").mockResolvedValue(undefined);
	let finishAuth!: (response: Response) => void;
	const pendingAuth = new Promise<Response>((resolve) => { finishAuth = resolve; });
	const calls: string[] = [];
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const path = new URL(request.url).pathname;
		calls.push(`${request.method} ${path}`);
		if (path.endsWith("/agents/claude-code/auth")) return pendingAuth;
		if (request.method === "DELETE" && path.endsWith("/shell-terminals/late-auth")) return new Response(null, { status: 204 });
		if (path.endsWith("/agents/readiness")) return Response.json(catalog);
		if (path.endsWith("/agents/installers")) return Response.json({ agents: [] });
		if (path.endsWith("/agents/install-jobs")) return Response.json({ jobs: [] });
		if (path.endsWith("/agents/auth-plans")) return Response.json({ plans: [{ agentId: "claude-code", action: "login", launchMode: "terminal", available: true }] });
		return Response.json(catalog);
	}));
	await connectHost("http://box-a:3001");
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const view = render(<QueryClientProvider client={client}><HarnessSettingsSection /></QueryClientProvider>);
	await userEvent.click(screen.getByRole("button", { name: "Host" }));
	await userEvent.click(screen.getByRole("menuitem", { name: "Box A" }));
	const claude = (await screen.findByText("Claude Code")).closest('[data-agent="claude-code"]') as HTMLElement;
	await userEvent.click(await within(claude).findByRole("button", { name: "Login" }));
	await waitFor(() => expect(calls).toContain("POST /token-a/api/v1/agents/claude-code/auth"));
	await userEvent.click(screen.getByRole("button", { name: "Host" }));
	await userEvent.click(screen.getByRole("menuitem", { name: "This computer" }));
	await act(async () => finishAuth(Response.json({
		agentId: "claude-code", action: "login",
		terminal: { handleId: "late-auth", workingDir: "/host-a", title: "Log in", createdAt: "2026-09-29T00:00:00Z" },
	}, { status: 201 })));
	await waitFor(() => expect(calls).toContain("DELETE /token-a/api/v1/shell-terminals/late-auth"));
	expect(client.getQueryData<ShellTerminal[]>(shellTerminalsQueryKeyForHost("box-a"))).toEqual([]);
	expect(localDelete).not.toHaveBeenCalled();
	expect(screen.queryByTestId("remote-auth-terminal")).not.toBeInTheDocument();
	view.unmount();
});

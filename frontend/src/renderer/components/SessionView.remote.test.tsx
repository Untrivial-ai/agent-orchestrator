import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import { typeInLexicalEditor } from "../test/lexical";
import { setChatDraftBoundary } from "../lib/chat-draft-boundary";
import { sessionUiKey } from "../lib/hosts";
import { aoBridge } from "../lib/bridge";
import { useUiStore } from "../stores/ui-store";

const { localGet, localPost, remoteConnect } = vi.hoisted(() => ({ localGet: vi.fn(), localPost: vi.fn(), remoteConnect: vi.fn() }));
vi.mock("../lib/api-client", async (importOriginal) => ({
	...await importOriginal<typeof import("../lib/api-client")>(),
	apiClient: { GET: localGet, POST: localPost },
	hasTrustedApiBaseUrl: () => true,
}));
vi.mock("../lib/bridge", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../lib/bridge")>();
	return { ...actual, aoBridge: { ...actual.aoBridge, remotes: { ...actual.aoBridge.remotes, connect: remoteConnect, disconnect: vi.fn() } } };
});
vi.mock("../lib/telemetry", () => ({ captureRendererEvent: vi.fn() }));
vi.mock("@tanstack/react-router", async (importOriginal) => ({
	...await importOriginal<typeof import("@tanstack/react-router")>(),
	useNavigate: () => vi.fn(),
	useBlocker: () => undefined,
}));
vi.mock("../hooks/useCloudCp", () => ({ useCloudCp: () => ({ ready: false, baseUrl: "", client: {} }) }));
vi.mock("../hooks/useCloudOrg", () => ({ useCloudOrg: () => ({ org: undefined, ready: false }) }));
vi.mock("../lib/shell-context", () => ({ useShell: () => ({ daemonStatus: { state: "ready" } }) }));
vi.mock("./ShellTopbar", () => ({ ShellTopbar: () => <div data-testid="shared-shell-topbar" /> }));
vi.mock("./NotificationCenter", () => ({ NotificationCenter: () => <button aria-label="Notifications" type="button" /> }));
vi.mock("./TerminalPane", () => ({ TerminalPane: ({ session, terminalTarget, inputDisabled, createMux }: { session?: { hostId?: string; terminalHandleId?: string }; terminalTarget?: { kind: string; handleId?: string }; inputDisabled?: boolean; createMux?: () => unknown }) => <div data-testid="remote-terminal-base" data-host-id={session?.hostId ?? ""} data-terminal-handle={terminalTarget?.handleId ?? session?.terminalHandleId ?? ""} data-input-disabled={inputDisabled ? "true" : "false"} data-remote-mux={createMux ? "true" : "false"}>{session?.hostId ? baseUrlForHost(session.hostId) : "local"}</div> }));

import { baseUrlForHost, connectHost, disconnectHost } from "../lib/host-clients";
import { conversationQueryKey } from "../hooks/useConversation";
import { reviewerConversationQueryKey } from "../hooks/useReviewerConversation";
import { remoteWorkspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { shellTerminalsQueryKey, shellTerminalsQueryKeyForHost } from "../hooks/useShellTerminals";
import { agentReadiness } from "../test/agent-readiness-fixtures";
import { SessionView } from "./SessionView";
import { SessionTopbarProvider } from "./SessionTopbarPortal";
import { TooltipProvider } from "./ui/tooltip";

function renderRemoteSession(queryClient: QueryClient, hostId = "box-a") {
	return render(<QueryClientProvider client={queryClient}>
		<TooltipProvider><SessionTopbarProvider><SessionView hostId={hostId} sessionId="session-1" /></SessionTopbarProvider></TooltipProvider>
	</QueryClientProvider>);
}

function conversationBody(body: Record<string, unknown> = {}) {
	return { conversationId: "conversation-1", sessionId: "session-1", harness: "codex", mode: "chat", controller: "ready", latestSequence: 1, activeBranchId: "branch-root", messages: [], activities: [], ...body };
}

afterEach(async () => {
	setChatDraftBoundary(sessionUiKey("session-1", "box-a"), "queued-edit", undefined);
	useUiStore.setState({ inspectorSessions: {} });
	await disconnectHost("box-a");
	await disconnectHost("box-b");
	vi.unstubAllGlobals();
});

it("keeps equal local, Box A, and Box B session IDs isolated through chat sends", async () => {
	localGet.mockReset();
	localPost.mockReset();
	remoteConnect.mockImplementation(async (url: string) => url.includes("box-b")
		? { hostId: "box-b", label: "Box B", url, base: "http://127.0.0.1:4001" }
		: { hostId: "box-a", label: "Box A", url, base: "http://127.0.0.1:4000" });
	const posts: string[] = [];
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const path = new URL(request.url).pathname;
		const host = new URL(request.url).port === "4001" ? "Box B" : "Box A";
		if (path.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: host, path: "/remote" }] });
		if (path.endsWith("/sessions")) return Response.json({ sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "working", mode: "chat", prs: [] }] });
		if (path.endsWith("/conversation")) return Response.json(conversationBody({ messages: [{ id: "message-1", role: "assistant", text: `Reply from ${host}`, sequence: 1 }] }));
		if (path.endsWith("/conversation/messages")) {
			posts.push(request.url);
			return Response.json({ state: "accepted", turnId: `turn-${host}` }, { status: 202 });
		}
		return Response.json({});
	}));
	await connectHost("http://box-a:3001");
	await connectHost("http://box-b:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	queryClient.setQueryData(conversationQueryKey("session-1"), "local-sentinel");
	const boxA = renderRemoteSession(queryClient);
	await screen.findByText("Reply from Box A");
	await typeInLexicalEditor(screen.getByRole("combobox", { name: "Message the agent" }), "A only");
	await userEvent.click(screen.getByRole("button", { name: "Send message" }));
	await waitFor(() => expect(posts).toContain("http://127.0.0.1:4000/api/v1/sessions/session-1/conversation/messages"));
	boxA.unmount();
	const boxB = renderRemoteSession(queryClient, "box-b");
	await screen.findByText("Reply from Box B");
	expect(screen.queryByText("Reply from Box A")).not.toBeInTheDocument();
	await typeInLexicalEditor(screen.getByRole("combobox", { name: "Message the agent" }), "B only");
	await userEvent.click(screen.getByRole("button", { name: "Send message" }));
	await waitFor(() => expect(posts).toContain("http://127.0.0.1:4001/api/v1/sessions/session-1/conversation/messages"));
	expect(queryClient.getQueryData(conversationQueryKey("session-1"))).toBe("local-sentinel");
	expect(queryClient.getQueryData(conversationQueryKey("session-1", "box-a"))).toBeDefined();
	expect(queryClient.getQueryData(conversationQueryKey("session-1", "box-b"))).toBeDefined();
	expect(localGet).not.toHaveBeenCalled();
	expect(localPost).not.toHaveBeenCalled();
	boxB.unmount();
});

it("removes remote chat actions when its host disconnects, without falling back to local", async () => {
	localGet.mockReset();
	localPost.mockReset();
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const path = new URL(request.url).pathname;
		if (path.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: "Remote", path: "/remote" }] });
		if (path.endsWith("/sessions")) return Response.json({ sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "working", mode: "chat", prs: [] }] });
		if (path.endsWith("/conversation")) return Response.json(conversationBody());
		return Response.json({});
	}));
	await connectHost("http://box-a:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	renderRemoteSession(queryClient);
	await screen.findByRole("combobox", { name: "Message the agent" });
	await act(async () => { await disconnectHost("box-a"); });
	expect(queryClient.getQueryData(conversationQueryKey("session-1", "box-a"))).toBeDefined();
	expect(screen.queryByRole("combobox", { name: "Message the agent" })).not.toBeInTheDocument();
	expect(screen.getByRole("alert")).toHaveTextContent("Host is offline");
	expect(localGet).not.toHaveBeenCalled();
	expect(localPost).not.toHaveBeenCalled();
});

it("hides stale chat controls when the upstream daemon fails but its proxy remains connected", async () => {
	localGet.mockReset();
	localPost.mockReset();
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	let upstreamFailed = false;
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const path = new URL(request.url).pathname;
		if (path.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: "Remote", path: "/remote" }] });
		if (path.endsWith("/sessions")) {
			return upstreamFailed
				? Response.json({ error: "unavailable", code: "UPSTREAM_UNAVAILABLE", message: "The host could not be reached" }, { status: 502 })
				: Response.json({ sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "working", mode: "chat", prs: [] }] });
		}
		if (path.endsWith("/conversation")) return Response.json(conversationBody());
		return Response.json({});
	}));
	await connectHost("http://box-a:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	renderRemoteSession(queryClient);
	await screen.findByRole("combobox", { name: "Message the agent" });
	upstreamFailed = true;
	await act(async () => { await queryClient.invalidateQueries({ queryKey: remoteWorkspaceQueryKey("box-a") }); });
	expect(queryClient.getQueryData(remoteWorkspaceQueryKey("box-a"))).toBeDefined();
	expect(screen.queryByTestId("session-detail")).not.toBeInTheDocument();
	expect(screen.getByRole("alert")).toHaveTextContent("Host is offline");
	expect(screen.queryByRole("combobox", { name: "Message the agent" })).not.toBeInTheDocument();
	expect(screen.queryByRole("complementary", { name: "Session inspector" })).not.toBeInTheDocument();
	expect(localGet).not.toHaveBeenCalled();
	expect(localPost).not.toHaveBeenCalled();
});

it.each([
	[401, { error: "unauthorized", code: "BAD_PASSWORD" }, "Host rejected the password"],
	[426, { error: "incompatible", code: "HOST_API_INCOMPATIBLE" }, "AO versions are incompatible"],
	[502, { error: "unavailable", code: "HOST_IDENTITY_UNVERIFIED", message: "Host identity changed" }, "Host is offline"],
])("explains remote session HTTP %i failures while its proxy is still connected", async (status, body, message) => {
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		return Response.json(new URL(request.url).pathname.endsWith("/projects")
			? { projects: [{ id: "project-1", name: "Remote", path: "/remote" }] }
			: body, { status: new URL(request.url).pathname.endsWith("/projects") ? 200 : status });
	}));
	await connectHost("http://box-a:3001");
	renderRemoteSession(new QueryClient({ defaultOptions: { queries: { retry: false } } }));
	await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(message));
});

it("resolves an approval and interrupts Box B despite equal IDs on Box A and local", async () => {
	localGet.mockReset();
	localPost.mockReset();
	remoteConnect.mockImplementation(async (url: string) => url.includes("box-b")
		? { hostId: "box-b", label: "Box B", url, base: "http://127.0.0.1:4001" }
		: { hostId: "box-a", label: "Box A", url, base: "http://127.0.0.1:4000" });
	const posts: string[] = [];
	let conversationReads = 0;
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const path = new URL(request.url).pathname;
		if (path.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: "Remote", path: "/remote" }] });
		if (path.endsWith("/sessions")) return Response.json({ sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "working", mode: "chat", prs: [] }] });
		if (path.endsWith("/conversation")) {
			conversationReads++;
			return Response.json(conversationBody({ controller: "busy", turns: [{ id: "turn-1", state: "running", requestedAt: "2026-09-28T00:00:00Z" }], activities: conversationReads === 1 ? [{
				id: "approval-1", turnId: "turn-1", sequence: 1, revision: 1, activityKind: "approval", status: "pending", summary: "Run command", requestId: "acp:host:1",
				detail: { command: "npm test", decisions: [{ id: "allow-once", label: "Allow once", kind: "allow_once" }] }, createdAt: "2026-09-28T00:00:00Z",
			}] : [] }));
		}
		if (request.method === "POST") {
			posts.push(request.url);
			return new Response(null, { status: 204 });
		}
		return Response.json({});
	}));
	await connectHost("http://box-a:3001");
	await connectHost("http://box-b:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	queryClient.setQueryData(conversationQueryKey("session-1"), "local-sentinel");
	renderRemoteSession(queryClient, "box-b");
	const approval = await screen.findByRole("group", { name: "Approval request acp:host:1" });
	await userEvent.click(within(approval).getByRole("button", { name: /Allow once/ }));
	await waitFor(() => expect(posts.some((url) => url.endsWith("/conversation/approvals/acp%3Ahost%3A1/resolve"))).toBe(true));
	await waitFor(() => expect(screen.queryByRole("group", { name: "Approval request acp:host:1" })).not.toBeInTheDocument());
	await userEvent.click(screen.getByRole("button", { name: "Stop turn" }));
	await waitFor(() => expect(posts).toContain("http://127.0.0.1:4001/api/v1/sessions/session-1/conversation/interrupt"));
	expect(posts.every((url) => url.startsWith("http://127.0.0.1:4001/"))).toBe(true);
	expect(queryClient.getQueryData(conversationQueryKey("session-1"))).toBe("local-sentinel");
	expect(localGet).not.toHaveBeenCalled();
	expect(localPost).not.toHaveBeenCalled();
});

it("shows a normal inspector and reads its changed files from the remote host only", async () => {
	HTMLElement.prototype.scrollTo = vi.fn();
	const requests: string[] = [];
	localGet.mockReset();
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const path = new URL(request.url).pathname;
		requests.push(request.url);
		if (path.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: "Remote", path: "/remote" }] });
		if (path.endsWith("/sessions")) return Response.json({ sessions: [{ id: "session-1", projectId: "project-1", displayName: "Fix login", harness: "codex", status: "working", mode: "chat", branch: "fix/login", prs: [{ url: "https://github.com/acme/app/pull/42", number: 42, state: "open", ci: "passing", review: "none", mergeability: "mergeable", reviewComments: false, updatedAt: "2026-09-28T00:00:00Z" }] }] });
		if (path.endsWith("/conversation")) return Response.json(conversationBody());
		if (path.endsWith("/workspace/files")) return Response.json({
			sessionId: "session-1", workspaceVersion: "version-1",
			files: [{ path: "app/page.tsx", status: "modified", additions: 1, deletions: 1, size: 6, binary: false }],
			sections: { committed: [], staged: [], unstaged: [{ path: "app/page.tsx", status: "modified", additions: 1, deletions: 1, size: 6, binary: false }], untracked: [] },
			commits: [], summary: { additions: 1, deletions: 1, files: 1 }, truncated: false,
		});
		if (path.endsWith("/workspace/diffs")) return Response.json({
			sessionId: "session-1", workspaceVersion: "version-1",
			groups: [{ repository: "", patch: "diff --git a/app/page.tsx b/app/page.tsx\n--- a/app/page.tsx\n+++ b/app/page.tsx\n@@ -1 +1 @@\n-old\n+new\n", truncated: false, includedPaths: ["app/page.tsx"], deferred: [] }],
		});
		if (path.endsWith("/workspace/file")) return Response.json({ path: "app/page.tsx", diff: "@@ -1 +1 @@\n-old\n+new", content: "new", binary: false, contentTruncated: false, diffTruncated: false });
		throw new Error(`Unexpected request ${request.url}`);
	}));
	await connectHost("http://box-a:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	renderRemoteSession(queryClient);
	await userEvent.click(await screen.findByRole("button", { name: "Open inspector panel" }));
	expect(await screen.findByRole("complementary", { name: "Session inspector" })).toBeInTheDocument();
	const resizeHandle = screen.getByTestId("inspector-resize-handle");
	const inspector = screen.getByTestId("panel-inspector");
	const initialWidth = Number.parseInt(inspector.style.getPropertyValue("--ao-inspector-w"), 10);
	fireEvent.pointerDown(resizeHandle, { pointerId: 1, clientX: 1000 });
	fireEvent.pointerMove(window, { pointerId: 1, clientX: 900 });
	fireEvent.pointerUp(window, { pointerId: 1, clientX: 900 });
	expect(inspector.style.getPropertyValue("--ao-inspector-w")).toBe(`${initialWidth + 100}px`);
	await screen.findByRole("combobox", { name: "Message the agent" });
	expect(screen.getByText("Box A")).toBeInTheDocument();
	expect(screen.getByRole("link", { name: "Open PR #42" })).toHaveAttribute("href", "https://github.com/acme/app/pull/42");
	await userEvent.click(screen.getByRole("tab", { name: "Files" }));
	expect(await screen.findByRole("region", { name: "Session files" })).toBeInTheDocument();
	await userEvent.click(await screen.findByRole("button", { name: "Open full file" }));
	expect(screen.getByRole("tab", { name: "page.tsx" })).toBeInTheDocument();
	expect(screen.getByTestId("session-file-workspace")).toBeInTheDocument();
	await userEvent.click(screen.getByRole("button", { name: "Maximize files" }));
	expect(screen.getByTestId("files-popout-topbar")).toBeInTheDocument();
	await userEvent.click(screen.getByRole("button", { name: "Minimize files" }));
	expect(requests).toContain("http://127.0.0.1:4000/api/v1/sessions/session-1/workspace/files");
	expect(requests).toContain("http://127.0.0.1:4000/api/v1/sessions/session-1/workspace/diffs");
	await waitFor(() => expect(requests.some((url) => url.startsWith("http://127.0.0.1:4000/api/v1/sessions/session-1/workspace/file?path="))).toBe(true));
	expect(localGet).not.toHaveBeenCalled();
	await userEvent.click(screen.getByRole("button", { name: "Close inspector panel" }));
	expect(screen.getByTestId("panel-inspector")).toHaveAttribute("data-state", "collapsed");
	await userEvent.click(screen.getByRole("button", { name: "Open inspector panel" }));
	expect(screen.getByRole("complementary", { name: "Session inspector" })).toBeInTheDocument();
});

it("opens the frontend file from an absolute remote turn diff without a cwd", async () => {
	HTMLElement.prototype.scrollTo = vi.fn();
	localGet.mockReset();
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	const expectedPath = "frontend/src/index.ts";
	const files = ["backend", "frontend"].map((name) => ({ path: `${name}/src/index.ts`, status: "added", additions: 1, deletions: 0, size: 4, binary: false }));
	const fileRequests: string[] = [];
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const url = new URL(request.url);
		if (url.pathname.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: "Remote", path: "/remote" }] });
		if (url.pathname.endsWith("/sessions")) return Response.json({ sessions: [{ id: "session-1", projectId: "project-1", displayName: "Edit index", harness: "codex", status: "working", mode: "chat", prs: [] }] });
		if (url.pathname.endsWith("/conversation")) return Response.json(conversationBody({
			turns: [{ id: "turn-1", state: "completed", requestedAt: "2026-09-28T00:00:00Z", diff: { files: [{ path: `/home/ao/.ao/data/worktrees/demo/session-1/${expectedPath}`, status: "added", additions: 1, deletions: 0 }] } }],
			messages: [{ id: "message-1", turnId: "turn-1", role: "assistant", origin: "provider", text: "Changed index.", sequence: 1, revision: 0, streaming: false, createdAt: "2026-09-28T00:00:01Z" }],
		}));
		if (url.pathname.endsWith("/workspace/files")) return Response.json({ sessionId: "session-1", workspaceVersion: "version-1", files, sections: { committed: [], staged: [], unstaged: [], untracked: files }, commits: [], summary: { additions: 2, deletions: 0, files: 2 }, truncated: false });
		if (url.pathname.endsWith("/workspace/file")) {
			fileRequests.push(url.searchParams.get("path") ?? "");
			return url.searchParams.get("path") === expectedPath
				? Response.json({ path: expectedPath, diff: "", content: "edit", binary: false, contentTruncated: false, diffTruncated: false })
				: Response.json({ error: "Workspace file not found" }, { status: 404 });
		}
		throw new Error(`Unexpected request ${request.url}`);
	}));
	await connectHost("http://box-a:3001");
	renderRemoteSession(new QueryClient({ defaultOptions: { queries: { retry: false } } }));
	await userEvent.click(await screen.findByRole("button", { name: "Open src/index.ts in Files" }));
	await waitFor(() => expect(fileRequests).toContain(expectedPath));
	expect(fileRequests.every((path) => path === expectedPath)).toBe(true);
	expect(localGet).not.toHaveBeenCalled();
});

it("shows a preview tab after remote session data finishes loading", async () => {
	HTMLElement.prototype.scrollTo = vi.fn();
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	let previewStarted = false;
	let sessionReads = 0;
	let releaseInitialSessions!: () => void;
	const initialSessions = new Promise<void>((resolve) => { releaseInitialSessions = resolve; });
	let emitTabs: ((state: { viewId: string; activeTabId: string; tabs: { id: string; url: string; title: string; active: boolean }[] }) => void) | undefined;
	const resolvePreview = vi.spyOn(aoBridge.remotes, "previewUrl").mockResolvedValue("http://ao-preview.localhost/");
	const ensure = vi.spyOn(window.ao!.browser, "ensure");
	const onTabsState = vi.spyOn(window.ao!.browser, "onTabsState").mockImplementation((listener) => {
		emitTabs = listener;
		return () => { emitTabs = undefined; };
	});
	const navigate = vi.spyOn(window.ao!.browser, "navigate").mockImplementation(async ({ viewId, url }) => {
		emitTabs?.({ viewId, activeTabId: "t1", tabs: [{ id: "t1", url, title: "QA preview", active: true }] });
		return { viewId, url, title: "AO preview", canGoBack: false, canGoForward: false, isLoading: false };
	});
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const path = new URL(input instanceof Request ? input.url : String(input)).pathname;
		if (path.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: "Remote", path: "/remote" }] });
		if (path.endsWith("/sessions")) {
			sessionReads++;
			if (sessionReads === 1) await initialSessions;
			return Response.json({ sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "working", mode: "chat", previewUrl: previewStarted ? "http://127.0.0.1:4600/" : "", previewRevision: previewStarted ? 1 : 0, prs: [] }] });
		}
		if (path.endsWith("/conversation")) return Response.json(conversationBody());
		return Response.json({});
	}));
	await connectHost("http://box-a:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	renderRemoteSession(queryClient);
	await waitFor(() => expect(ensure).toHaveBeenCalledWith("remote:box-a:session-1"));
	await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
	releaseInitialSessions();
	await screen.findByRole("combobox", { name: "Message the agent" });
	expect(sessionReads).toBe(1);
	previewStarted = true;
	await act(async () => { await queryClient.invalidateQueries({ queryKey: remoteWorkspaceQueryKey("box-a") }); });
	await waitFor(() => expect(navigate).toHaveBeenCalledWith({ viewId: "test:remote:box-a:session-1", url: "http://ao-preview.localhost/" }));
	await userEvent.click(screen.getByRole("button", { name: "Open inspector panel" }));
	await userEvent.click(screen.getByRole("tab", { name: "Browser" }));
	await waitFor(() => expect(resolvePreview).toHaveBeenCalledWith("box-a", "session-1", "http://127.0.0.1:4600/"));
	await waitFor(() => expect(screen.getByRole("textbox", { name: "Browser URL" })).toHaveValue("ao-preview.localhost"));
	await waitFor(() => expect(within(screen.getByRole("tablist", { name: "Browser tabs" })).getByRole("tab", { name: "QA preview" })).toBeVisible());
	await userEvent.click(screen.getByRole("button", { name: "Browser controls" }));
	await userEvent.click(screen.getByRole("menuitem", { name: "Pop out" }));
	expect(screen.getByTestId("browser-popout-topbar")).toBeInTheDocument();
	await userEvent.click(within(document.querySelector(".browser-popout-overlay") as HTMLElement).getByRole("button", { name: "Return to panel" }));
	expect(screen.queryByTestId("browser-popout-topbar")).not.toBeInTheDocument();
	expect(sessionReads).toBe(3);
	onTabsState.mockRestore();
	ensure.mockRestore();
	navigate.mockRestore();
	resolvePreview.mockRestore();
});

it("opens a TUI host file in a shared center tab and renames on that host", async () => {
	const requests: Array<{ url: string; method: string }> = [];
	let title = "Worker";
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const path = new URL(request.url).pathname;
		requests.push({ url: request.url, method: request.method });
		if (path.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: "Remote", path: "/remote" }] });
		if (path.endsWith("/sessions") && request.method === "GET") return Response.json({ sessions: [{ id: "session-1", projectId: "project-1", displayName: title, harness: "codex", status: "working", mode: "tui", terminalHandleId: "terminal-1", prs: [] }] });
		if (path.endsWith("/sessions/session-1") && request.method === "PATCH") {
			title = (await request.json() as { displayName: string }).displayName;
			return Response.json({});
		}
		if (path.endsWith("/workspace/files")) return Response.json({ sessionId: "session-1", workspaceVersion: "version-1", files: [{ path: "app/page.tsx", status: "modified", additions: 1, deletions: 1, size: 6, binary: false }], sections: { committed: [], staged: [], unstaged: [{ path: "app/page.tsx", status: "modified", additions: 1, deletions: 1, size: 6, binary: false }], untracked: [] }, commits: [], summary: { additions: 1, deletions: 1, files: 1 }, truncated: false });
		if (path.endsWith("/workspace/diffs")) return Response.json({ sessionId: "session-1", workspaceVersion: "version-1", groups: [{ repository: "", patch: "diff --git a/app/page.tsx b/app/page.tsx\n--- a/app/page.tsx\n+++ b/app/page.tsx\n@@ -1 +1 @@\n-old\n+new\n", truncated: false, includedPaths: ["app/page.tsx"], deferred: [] }] });
		if (path.endsWith("/workspace/file")) return Response.json({ path: "app/page.tsx", diff: "@@ -1 +1 @@\n-old\n+new", content: "new", binary: false, contentTruncated: false, diffTruncated: false });
		return Response.json({});
	}));
	await connectHost("http://box-a:3001");
	renderRemoteSession(new QueryClient({ defaultOptions: { queries: { retry: false } } }));
	expect(await screen.findByTestId("remote-terminal-base")).toHaveTextContent("http://127.0.0.1:4000");
	await userEvent.click(screen.getByRole("button", { name: "Open inspector panel" }));
	await userEvent.click(screen.getByRole("tab", { name: "Files" }));
	await userEvent.click(await screen.findByRole("button", { name: "Open full file" }));
	expect(screen.getByRole("tab", { name: "page.tsx" })).toHaveAttribute("aria-selected", "true");
	expect(screen.getByTestId("session-file-workspace")).toBeInTheDocument();
	await userEvent.click(screen.getByRole("tab", { name: /Worker · Codex/ }));
	expect(screen.queryByTestId("session-file-workspace")).not.toBeInTheDocument();
	expect(screen.getByTestId("remote-terminal-base")).toBeVisible();
	await userEvent.dblClick(screen.getByRole("tab", { name: /Worker · Codex/ }));
	const rename = screen.getByRole("textbox", { name: /Rename/ });
	await userEvent.clear(rename);
	await userEvent.type(rename, "Remote worker");
	fireEvent.blur(rename);
	await waitFor(() => expect(requests).toContainEqual({ url: "http://127.0.0.1:4000/api/v1/sessions/session-1", method: "PATCH" }));
	expect(localGet).not.toHaveBeenCalled();
});

it("loads older remote history once while polling only the latest page", async () => {
	const conversationReads: URL[] = [];
	let latestReads = 0;
	localGet.mockReset();
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const url = new URL(request.url);
		let body: unknown;
		if (url.pathname.endsWith("/projects")) body = { projects: [{ id: "project-1", name: "Remote", path: "/remote" }] };
		else if (url.pathname.endsWith("/sessions")) body = { sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "working", mode: "chat", prs: [] }] };
		else if (url.pathname.endsWith("/conversation")) {
			conversationReads.push(url);
			if (url.searchParams.has("beforeSequence")) {
				body = conversationBody({ latestSequence: 200, oldestSequence: 1, hasMoreBefore: false,
					messages: [{ id: "older", role: "assistant", text: "Earlier work", sequence: 200 }] });
			} else {
				latestReads++;
				body = conversationBody({ latestSequence: 400 + latestReads, oldestSequence: 200 + latestReads, hasMoreBefore: true,
					messages: [
						...(latestReads === 1 ? [{ id: "edge", role: "assistant", text: "Sliding window edge", sequence: 201 }] : []),
						{ id: "latest", role: "assistant", text: `Latest update ${latestReads}`, sequence: 400 + latestReads },
					] });
			}
		} else throw new Error(`Unexpected request ${request.url}`);
		return new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
	}));
	await connectHost("http://box-a:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	renderRemoteSession(queryClient);
	await screen.findByText("Latest update 1");
	fireEvent.click(screen.getByRole("button", { name: "Load earlier messages" }));
	await screen.findByText("Earlier work");
	expect(conversationReads.map((url) => url.searchParams.get("beforeSequence"))).toContain("201");
	expect(conversationReads.every((url) => url.searchParams.get("limit") === "200")).toBe(true);
	await screen.findByText("Latest update 2", {}, { timeout: 4_000 });
	expect(screen.getByText("Sliding window edge")).toBeInTheDocument();
	expect(screen.getByText("Earlier work")).toBeInTheDocument();
	expect(conversationReads.filter((url) => url.searchParams.has("beforeSequence"))).toHaveLength(1);
	expect(localGet).not.toHaveBeenCalled();
});

it("keeps accepted remote sends accepted when the follow-up read fails", async () => {
	let conversationReads = 0;
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const path = new URL(request.url).pathname;
		if (path.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: "Remote", path: "/remote" }] });
		if (path.endsWith("/sessions")) return Response.json({ sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "working", mode: "chat", prs: [] }] });
		if (path.endsWith("/conversation")) {
			conversationReads++;
			return conversationReads === 1
				? Response.json(conversationBody({ messages: [{ id: "first", role: "assistant", text: "Still here", sequence: 1 }] }))
				: Response.json({ code: "UNAVAILABLE", message: "Connection lost" }, { status: 503 });
		}
		if (path.endsWith("/conversation/messages")) return Response.json({ state: "accepted", turnId: "turn-2" }, { status: 202 });
		throw new Error(`Unexpected request ${request.url}`);
	}));
	await connectHost("http://box-a:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	renderRemoteSession(queryClient);
	await screen.findByText("Still here");
	await typeInLexicalEditor(screen.getByRole("combobox", { name: "Message the agent" }), "Continue");
	fireEvent.click(screen.getByRole("button", { name: "Send message" }));
	expect(await screen.findByText("Could not load this remote conversation.")).toHaveAttribute("role", "alert");
	expect(screen.getByText("Still here")).toBeInTheDocument();
	expect(screen.queryByText(/delivery wasn’t confirmed/)).not.toBeInTheDocument();
});

it("reattaches a terminal when the same host gets a new proxy connection", async () => {
	remoteConnect.mockReset()
		.mockResolvedValueOnce({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000/old" })
		.mockResolvedValueOnce({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4001/new" });
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const body = request.url.endsWith("/projects")
			? { projects: [{ id: "project-1", name: "Remote", path: "/remote" }] }
			: { sessions: [{ id: "session-1", projectId: "project-1", displayName: "Worker", harness: "codex", status: "working", mode: "tui", terminalHandleId: "terminal-1", prs: [] }] };
		return new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
	}));
	await connectHost("http://box-a:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	renderRemoteSession(queryClient);
	expect(await screen.findByTestId("remote-terminal-base")).toHaveTextContent("http://127.0.0.1:4000/old");
	await act(async () => { await connectHost("http://box-a:3001"); });
	await waitFor(() => expect(screen.getByTestId("remote-terminal-base")).toHaveTextContent("http://127.0.0.1:4001/new"));
});

it("reuses a Chat delivery ID when a lost response is retried", async () => {
	const deliveryIds: string[] = [];
	const posts: string[] = [];
	localGet.mockReset();
	localPost.mockReset();
	let boxAConnections = 0;
	remoteConnect.mockImplementation(async (url: string) => url.includes("box-b")
		? { hostId: "box-b", label: "Box B", url, base: "http://127.0.0.1:4001" }
		: { hostId: "box-a", label: "Box A", url, base: boxAConnections++ === 0 ? "http://127.0.0.1:4000" : "http://127.0.0.1:4002" });
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		let body: unknown;
		let status = 200;
		if (request.url.endsWith("/projects")) body = { projects: [{ id: "project-1", name: "Remote", path: "/remote" }] };
		else if (request.url.endsWith("/sessions")) body = { sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "working", mode: "chat", prs: [] }] };
		else if (new URL(request.url).pathname.endsWith("/conversation")) body = conversationBody();
		else if (new URL(request.url).pathname.endsWith("/conversation/messages")) {
			posts.push(request.url);
			deliveryIds.push((await request.json() as { clientMessageId: string }).clientMessageId);
			if (deliveryIds.length === 1) throw new TypeError("response lost after acceptance");
			status = 202;
			body = { state: "accepted", turnId: "turn-1" };
		} else throw new Error(`Unexpected request ${request.url}`);
		return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
	}));
	await connectHost("http://box-a:3001");
	await connectHost("http://box-b:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	renderRemoteSession(queryClient);
	const message = await screen.findByRole("combobox", { name: "Message the agent" });
	await typeInLexicalEditor(message, "Continue the task");
	await userEvent.click(screen.getByRole("button", { name: "Send message" }));
	await screen.findByText(/delivery wasn’t confirmed/);
	await act(async () => { await disconnectHost("box-a"); });
	expect(screen.getByRole("alert")).toHaveTextContent("Host is offline");
	await act(async () => { await connectHost("http://box-a:3001"); });
	await screen.findByRole("button", { name: "Retry message safely" });
	await userEvent.click(screen.getByRole("button", { name: "Retry message safely" }));
	await waitFor(() => expect(deliveryIds).toHaveLength(2));
	expect(deliveryIds[1]).toBe(deliveryIds[0]);
	expect(posts).toEqual([
		"http://127.0.0.1:4000/api/v1/sessions/session-1/conversation/messages",
		"http://127.0.0.1:4002/api/v1/sessions/session-1/conversation/messages",
	]);
	expect(localGet).not.toHaveBeenCalled();
	expect(localPost).not.toHaveBeenCalled();
});

it("refreshes a remote approval after an already-answered 409", async () => {
	const decisions: Array<{ url: string; decisionId: string }> = [];
	let conversationReads = 0;
	localGet.mockReset();
	localPost.mockReset();
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		let body: unknown;
		if (request.url.endsWith("/projects")) body = { projects: [{ id: "project-1", name: "Remote", path: "/remote" }] };
		else if (request.url.endsWith("/sessions")) body = { sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "working", mode: "chat", prs: [] }] };
		else if (new URL(request.url).pathname.endsWith("/conversation")) {
			conversationReads++;
			body = conversationBody({ controller: "busy", turns: [{ id: "turn-1", state: "running", requestedAt: "2026-09-28T00:00:00Z" }], activities: conversationReads === 1 ? [{
				kind: "activity", id: "approval-1", turnId: "turn-1", sequence: 1, revision: 1,
				activityKind: "approval", status: "pending", summary: "Run command", requestId: "acp:host:1",
				detail: { command: "npm test", decisions: [{ id: "allow-once", label: "Allow once", kind: "allow_once" }] },
				createdAt: "2026-09-28T00:00:00Z",
			}] : [] });
		} else if (request.url.endsWith("/resolve")) {
			decisions.push({ url: request.url, decisionId: (await request.json() as { decisionId: string }).decisionId });
			return new Response(JSON.stringify({ code: "CHAT_REQUEST_NOT_PENDING", message: "already answered" }), {
				status: 409, headers: { "content-type": "application/json" },
			});
		} else throw new Error(`Unexpected request ${request.url}`);
		return new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
	}));
	await connectHost("http://box-a:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	renderRemoteSession(queryClient);
	const approval = await screen.findByRole("group", { name: "Approval request acp:host:1" });
	expect(screen.queryByRole("combobox", { name: "Message the agent" })).not.toBeInTheDocument();
	fireEvent.click(within(approval).getByRole("button", { name: /Allow once/ }));
	await waitFor(() => expect(decisions).toEqual([{
		url: "http://127.0.0.1:4000/api/v1/sessions/session-1/conversation/approvals/acp%3Ahost%3A1/resolve",
		decisionId: "allow-once",
	}]));
	await waitFor(() => expect(screen.queryByRole("group", { name: "Approval request acp:host:1" })).not.toBeInTheDocument());
	expect(localPost).not.toHaveBeenCalled();
	expect(localGet).not.toHaveBeenCalled();
});

it("retries reviewer Chat on Box B with the same delivery ID", async () => {
	HTMLElement.prototype.scrollTo = vi.fn();
	localGet.mockReset();
	localPost.mockReset();
	let boxBConnections = 0;
	remoteConnect.mockImplementation(async (url: string) => url.includes("box-b")
		? { hostId: "box-b", label: "Box B", url, base: boxBConnections++ === 0 ? "http://127.0.0.1:4001" : "http://127.0.0.1:4002" }
		: { hostId: "box-a", label: "Box A", url, base: "http://127.0.0.1:4000" });
	const posts: string[] = [];
	const deliveryIds: string[] = [];
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const path = new URL(request.url).pathname;
		if (path.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: "Todo", path: "/remote" }] });
		if (path.endsWith("/sessions")) return Response.json({ sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "working", mode: "chat", prs: [{ url: "https://github.com/acme/todo/pull/7", number: 7, state: "open", ci: "passing", review: "none", mergeability: "mergeable", reviewComments: false, updatedAt: "2026-09-28T00:00:00Z" }] }] });
		if (path.endsWith("/sessions/session-1/reviews")) return Response.json({ reviewerHandleId: "", reviewerSurface: { mode: "chat", reviewId: "review-1", harness: "codex" }, reviews: [], runs: [] });
		if (path.endsWith("/reviews/review-1/conversation")) return Response.json(conversationBody({ sessionId: "review-1", messages: [{ id: "msg-review", role: "assistant", text: `Review on port ${new URL(request.url).port}`, sequence: 1 }] }));
		if (path.endsWith("/reviews/review-1/conversation/messages")) {
			posts.push(request.url);
			deliveryIds.push((await request.json() as { clientMessageId: string }).clientMessageId);
			if (deliveryIds.length === 1) throw new TypeError("response lost after acceptance");
			return Response.json({ state: "accepted", turnId: "review-turn" }, { status: 202 });
		}
		if (path.endsWith("/sessions/session-1/conversation")) return Response.json(conversationBody());
		return Response.json({});
	}));
	await connectHost("http://box-a:3001");
	await connectHost("http://box-b:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	queryClient.setQueryData(reviewerConversationQueryKey("review-1"), "local-sentinel");
	queryClient.setQueryData(reviewerConversationQueryKey("review-1", "box-a"), "box-a-sentinel");
	renderRemoteSession(queryClient, "box-b");
	await userEvent.click(await screen.findByRole("tab", { name: "Reviewer" }));
	expect(await screen.findByText("Review on port 4001")).toBeInTheDocument();
	await typeInLexicalEditor(screen.getByRole("combobox", { name: "Message the agent" }), "Review this change");
	await userEvent.click(screen.getByRole("button", { name: "Send message" }));
	await screen.findByText(/delivery wasn’t confirmed/);
	await act(async () => { await disconnectHost("box-b"); });
	expect(screen.getByRole("alert")).toHaveTextContent("Host is offline");
	await act(async () => { await connectHost("http://box-b:3001"); });
	await screen.findByRole("button", { name: "Retry message safely" });
	await userEvent.click(screen.getByRole("button", { name: "Retry message safely" }));
	await waitFor(() => expect(deliveryIds).toHaveLength(2));
	expect(deliveryIds[1]).toBe(deliveryIds[0]);
	expect(posts).toEqual([
		"http://127.0.0.1:4001/api/v1/reviews/review-1/conversation/messages",
		"http://127.0.0.1:4002/api/v1/reviews/review-1/conversation/messages",
	]);
	expect(queryClient.getQueryData(reviewerConversationQueryKey("review-1"))).toBe("local-sentinel");
	expect(queryClient.getQueryData(reviewerConversationQueryKey("review-1", "box-a"))).toBe("box-a-sentinel");
	expect(queryClient.getQueryData(reviewerConversationQueryKey("review-1", "box-b"))).toBeDefined();
	expect(localGet).not.toHaveBeenCalled();
	expect(localPost).not.toHaveBeenCalled();
});

it("opens the returned reviewer Chat when a remote review is triggered", async () => {
	HTMLElement.prototype.scrollTo = vi.fn();
	remoteConnect.mockResolvedValue({ hostId: "box-b", label: "Box B", url: "http://box-b:3001", base: "http://127.0.0.1:4001" });
	const prUrl = "https://github.com/acme/todo/pull/7";
	let reviews = { reviewerHandleId: "", reviews: [{ prNumber: 7, prUrl, status: "needs_review", targetSha: "head", title: "PR 7" }], runs: [] } as Record<string, unknown>;
	const mutations: string[] = [];
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const path = new URL(request.url).pathname;
		if (path.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: "Todo", path: "/remote" }] });
		if (path.endsWith("/sessions")) return Response.json({ sessions: [{ id: "session-1", projectId: "project-1", harness: "opencode", status: "working", mode: "chat", prs: [{ url: prUrl, number: 7, state: "open", ci: "passing", review: "none", mergeability: "mergeable", reviewComments: false, updatedAt: "2026-09-28T00:00:00Z" }] }] });
		if (path.endsWith("/sessions/session-1/reviews") && request.method === "GET") return Response.json(reviews);
		if (path.endsWith("/sessions/session-1/reviews/trigger")) {
			mutations.push(request.url);
			reviews = {
				reviewerHandleId: "", reviewerSurface: { mode: "chat", reviewId: "review-1", harness: "opencode" }, runs: [],
				reviews: [{ prNumber: 7, prUrl, status: "running", targetSha: "head", title: "PR 7", latestRun: { id: "run-1", reviewId: "review-1", sessionId: "session-1", prUrl, targetSha: "head", harness: "opencode", status: "running", createdAt: "2026-09-28T00:00:00Z", triggerSource: "manual", autoInjectReview: true, githubReviewId: "", body: "", verdict: "" } }],
			};
			return Response.json(reviews, { status: 201 });
		}
		if (path.endsWith("/projects/project-1")) return Response.json({ project: { config: { reviewers: [{ harness: "opencode" }] } } });
		if (path.endsWith("/agents/readiness") || path.endsWith("/agents/readiness/ensure")) return Response.json({ agents: [agentReadiness("opencode")] });
		if (path.endsWith("/reviews/review-1/conversation")) return Response.json(conversationBody({ sessionId: "review-1", messages: [{ id: "review-msg", role: "assistant", text: "Reviewer is on Box B", sequence: 1 }] }));
		if (path.endsWith("/sessions/session-1/conversation")) return Response.json(conversationBody());
		return Response.json({});
	}));
	await connectHost("http://box-b:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	renderRemoteSession(queryClient, "box-b");
	await userEvent.click(await screen.findByRole("button", { name: "Open inspector panel" }));
	await userEvent.click(await screen.findByRole("tab", { name: "Reviews" }));
	await userEvent.click(await screen.findByRole("button", { name: "Review latest commit" }));
	expect(await screen.findByText("Reviewer is on Box B")).toBeInTheDocument();
	expect(mutations).toEqual(["http://127.0.0.1:4001/api/v1/sessions/session-1/reviews/trigger"]);
	expect(screen.getByRole("tab", { name: "Reviewer" })).toHaveAttribute("aria-selected", "true");
});

it("opens a TUI reviewer terminal through Box B's mux handle", async () => {
	remoteConnect.mockResolvedValue({ hostId: "box-b", label: "Box B", url: "http://box-b:3001", base: "http://127.0.0.1:4001" });
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const path = new URL(request.url).pathname;
		if (path.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: "Todo", path: "/remote" }] });
		if (path.endsWith("/sessions")) return Response.json({ sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "working", mode: "tui", terminalHandleId: "worker-handle", prs: [{ url: "https://github.com/acme/todo/pull/7", number: 7, state: "open" }] }] });
		if (path.endsWith("/sessions/session-1/reviews")) return Response.json({ reviewerHandleId: "reviewer-handle", reviewerHarness: "codex", reviews: [], runs: [] });
		return Response.json({});
	}));
	await connectHost("http://box-b:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	renderRemoteSession(queryClient, "box-b");
	await userEvent.click(await screen.findByRole("tab", { name: "Reviewer" }));
	expect(screen.getByTestId("remote-terminal-base")).toHaveAttribute("data-terminal-handle", "reviewer-handle");
	expect(screen.getByTestId("remote-terminal-base")).toHaveTextContent("http://127.0.0.1:4001");
	await userEvent.click(screen.getByRole("tab", { name: /session-1/ }));
	expect(screen.getByTestId("remote-terminal-base")).toHaveAttribute("data-terminal-handle", "worker-handle");
});

it("opens a shell tab on Box B without attaching a local or Box A terminal", async () => {
	HTMLElement.prototype.scrollTo = vi.fn();
	localGet.mockReset();
	localPost.mockReset();
	remoteConnect.mockResolvedValue({ hostId: "box-b", label: "Box B", url: "http://box-b:3001", base: "http://127.0.0.1:4001" });
	const shellPosts: string[] = [];
	const openedShell = { handleId: "shared-handle", projectId: "project-1", sessionId: "session-1", title: "Terminal 1", workingDir: "/remote", createdAt: "2026-09-28T00:00:00Z" };
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const path = new URL(request.url).pathname;
		if (path.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: "Todo", path: "/remote" }] });
		if (path.endsWith("/sessions")) return Response.json({ sessions: [{ id: "session-1", projectId: "project-1", harness: "opencode", status: "working", mode: "chat", prs: [] }] });
		if (path.endsWith("/sessions/session-1/conversation")) return Response.json(conversationBody({ harness: "opencode" }));
		if (path.endsWith("/shell-terminals") && request.method === "GET") return Response.json({ shellTerminals: shellPosts.length ? [openedShell] : [] });
		if (path.endsWith("/shell-terminals") && request.method === "POST") {
			shellPosts.push(request.url);
			return Response.json({ shellTerminal: openedShell }, { status: 201 });
		}
		return Response.json({});
	}));
	await connectHost("http://box-b:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	queryClient.setQueryData(shellTerminalsQueryKey, "local-sentinel");
	queryClient.setQueryData(shellTerminalsQueryKeyForHost("box-a"), "box-a-sentinel");
	renderRemoteSession(queryClient, "box-b");
	await userEvent.click(await screen.findByRole("button", { name: "New terminal" }));
	await waitFor(() => expect(shellPosts).toEqual(["http://127.0.0.1:4001/api/v1/shell-terminals"]));
	const shellPane = await screen.findByTestId("chat-shell-terminal");
	expect(within(shellPane).getByTestId("remote-terminal-base")).toHaveAttribute("data-host-id", "box-b");
	expect(within(shellPane).getByTestId("remote-terminal-base")).toHaveAttribute("data-terminal-handle", "shared-handle");
	expect(queryClient.getQueryData(shellTerminalsQueryKey)).toBe("local-sentinel");
	expect(queryClient.getQueryData(shellTerminalsQueryKeyForHost("box-a"))).toBe("box-a-sentinel");
	expect(localGet).not.toHaveBeenCalled();
	expect(localPost).not.toHaveBeenCalled();
});

it("switches a remote Terminal session through its host and fences worker input while pending", async () => {
	localGet.mockReset();
	localPost.mockReset();
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	const posts: Array<{ url: string; body: unknown }> = [];
	let transitionStatus: Record<string, unknown> = { supported: true, targetMode: "chat" };
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const path = new URL(request.url).pathname;
		if (path.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: "Remote", path: "/remote" }] });
		if (path.endsWith("/sessions")) return Response.json({ sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "idle", mode: "tui", terminalHandleId: "worker-terminal", prs: [] }] });
		if (path.endsWith("/settings")) return Response.json({ chatHarnesses: ["codex"] });
		if (path.endsWith("/interface-transition") && request.method === "GET") return Response.json(transitionStatus);
		if (path.endsWith("/interface-transition") && request.method === "POST") {
			posts.push({ url: request.url, body: await request.json() });
			const transition = { id: "transition-a", sessionId: "session-1", sourceMode: "tui", targetMode: "chat", policy: "drain", historyPolicy: "strict", phase: "requested", createdAt: "2026-09-28T00:00:00Z", updatedAt: "2026-09-28T00:00:00Z" };
			transitionStatus = { supported: true, targetMode: "chat", transition };
			return Response.json({ transition }, { status: 202 });
		}
		return Response.json({});
	}));
	await connectHost("http://box-a:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	renderRemoteSession(queryClient);
	const terminal = await screen.findByTestId("remote-terminal-base");
	expect(terminal).toHaveAttribute("data-input-disabled", "false");
	await userEvent.click(await screen.findByRole("button", { name: "Session actions" }));
	await userEvent.click(await screen.findByRole("menuitem", { name: "Switch to chat UI" }));
	await waitFor(() => expect(posts).toEqual([{ url: "http://127.0.0.1:4000/api/v1/sessions/session-1/interface-transition", body: { targetMode: "chat", policy: "drain", historyPolicy: "strict" } }]));
	await waitFor(() => expect(terminal).toHaveAttribute("data-input-disabled", "true"));
	expect(localGet).not.toHaveBeenCalled();
	expect(localPost).not.toHaveBeenCalled();
});

it("requires remote Chat draft confirmation before posting an interface switch", async () => {
	localPost.mockReset();
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	const posts: string[] = [];
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const path = new URL(request.url).pathname;
		if (path.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: "Remote", path: "/remote" }] });
		if (path.endsWith("/sessions")) return Response.json({ sessions: [{ id: "session-1", projectId: "project-1", harness: "codex", status: "working", mode: "chat", prs: [] }] });
		if (path.endsWith("/settings")) return Response.json({ chatHarnesses: ["codex"] });
		if (path.endsWith("/interface-transition") && request.method === "GET") return Response.json({ supported: true, targetMode: "tui" });
		if (path.endsWith("/interface-transition") && request.method === "POST") {
			posts.push(request.url);
			return Response.json({ transition: { id: "transition-a", sessionId: "session-1", sourceMode: "chat", targetMode: "tui", policy: "drain", historyPolicy: "strict", phase: "requested", createdAt: "2026-09-28T00:00:00Z", updatedAt: "2026-09-28T00:00:00Z" } }, { status: 202 });
		}
		if (path.endsWith("/conversation")) return Response.json(conversationBody());
		return Response.json({});
	}));
	await connectHost("http://box-a:3001");
	renderRemoteSession(new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } }));
	await screen.findByRole("combobox", { name: "Message the agent" });
	act(() => setChatDraftBoundary(sessionUiKey("session-1", "box-a"), "queued-edit", "persistence-failed"));
	await userEvent.click(await screen.findByRole("button", { name: "Session actions" }));
	await userEvent.click(await screen.findByRole("menuitem", { name: "Switch to terminal UI" }));
	await userEvent.click(await screen.findByRole("button", { name: /^Finish work, then switch/ }));
	const confirm = await screen.findByRole("dialog", { name: "Discard unsafe Chat draft state?" });
	expect(posts).toEqual([]);
	await userEvent.click(within(confirm).getByRole("button", { name: "Leave chat" }));
	await waitFor(() => expect(posts).toEqual(["http://127.0.0.1:4000/api/v1/sessions/session-1/interface-transition"]));
	expect(localPost).not.toHaveBeenCalled();
});

it("hides Chat switching for a remote harness outside that host's Chat list", async () => {
	remoteConnect.mockResolvedValue({ hostId: "box-a", label: "Box A", url: "http://box-a:3001", base: "http://127.0.0.1:4000" });
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const path = new URL(input instanceof Request ? input.url : String(input)).pathname;
		if (path.endsWith("/projects")) return Response.json({ projects: [{ id: "project-1", name: "Remote", path: "/remote" }] });
		if (path.endsWith("/sessions")) return Response.json({ sessions: [{ id: "session-1", projectId: "project-1", harness: "opencode", status: "idle", mode: "tui", prs: [] }] });
		if (path.endsWith("/settings")) return Response.json({ chatHarnesses: ["claude-code", "codex"] });
		if (path.endsWith("/interface-transition")) return Response.json({ supported: false, targetMode: "chat", reasonCode: "SESSION_TERMINATED" });
		return Response.json({});
	}));
	await connectHost("http://box-a:3001");
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	renderRemoteSession(queryClient);
	await screen.findByTestId("remote-terminal-base");
	await waitFor(() => expect(queryClient.getQueryData(["settings", "box-a"])).toMatchObject({ chatHarnesses: ["claude-code", "codex"] }));
	expect(screen.queryByRole("button", { name: "Session actions" })).not.toBeInTheDocument();
});

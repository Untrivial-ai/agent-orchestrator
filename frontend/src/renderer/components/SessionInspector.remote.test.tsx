import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { connectHost, disconnectHost } from "../lib/host-clients";
import { sessionReviewsQueryKey } from "../lib/session-reviews";
import { agentReadiness } from "../test/agent-readiness-fixtures";
import type { WorkspaceSession } from "../types/workspace";
import { SessionInspector } from "./SessionInspector";
import { TooltipProvider } from "./ui/tooltip";

const { localGet, localPost, localPatch, localPut, remoteConnect } = vi.hoisted(() => ({
	localGet: vi.fn(), localPost: vi.fn(), localPatch: vi.fn(), localPut: vi.fn(), remoteConnect: vi.fn(),
}));
vi.mock("../lib/api-client", async (importOriginal) => ({
	...await importOriginal<typeof import("../lib/api-client")>(),
	apiClient: { GET: localGet, POST: localPost, PATCH: localPatch, PUT: localPut },
}));
vi.mock("../lib/bridge", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../lib/bridge")>();
	return { ...actual, aoBridge: { ...actual.aoBridge, remotes: { connect: remoteConnect, disconnect: vi.fn() } } };
});
vi.mock("../lib/telemetry", () => ({ captureRendererEvent: vi.fn() }));
vi.mock("@tanstack/react-router", async (importOriginal) => ({
	...await importOriginal<typeof import("@tanstack/react-router")>(),
	useNavigate: () => vi.fn(),
}));
vi.mock("../hooks/useCloudCp", () => ({ useCloudCp: () => ({ ready: false, baseUrl: "", client: {} }) }));

function session(hostId: string): WorkspaceSession {
	return {
		id: "same-session", hostId, workspaceId: "same-project", workspaceName: "todo", title: "Fix todo",
		provider: "opencode", kind: "worker", status: "review_pending", mode: "chat",
		createdAt: "2026-09-28T00:00:00Z", updatedAt: "2026-09-28T00:00:00Z", autoReviewEnabled: false,
		autoInjectCI: true, autoInjectReview: true,
		prs: [{ url: "https://github.com/acme/todo/pull/7", number: 7, state: "open", ci: "passing", review: "none", mergeability: "mergeable", reviewComments: false, updatedAt: "2026-09-28T00:00:00Z" }],
	};
}

afterEach(async () => {
	await disconnectHost("box-a");
	await disconnectHost("box-b");
	vi.unstubAllGlobals();
});

it("sends inspector policy and review mutations to the selected host with equal raw IDs", async () => {
	localGet.mockReset(); localPost.mockReset(); localPatch.mockReset(); localPut.mockReset();
	remoteConnect.mockImplementation(async (url: string) => url.includes("box-b")
		? { hostId: "box-b", label: "Box B", url, base: "http://127.0.0.1:4001" }
		: { hostId: "box-a", label: "Box A", url, base: "http://127.0.0.1:4000" });
	const mutations: string[] = [];
	vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
		const request = input instanceof Request ? input : new Request(input);
		const url = new URL(request.url);
		if (request.method !== "GET") mutations.push(`${request.method} ${url.origin}${url.pathname}`);
		if (url.pathname.endsWith("/reviews")) return Response.json({ reviewerHandleId: "", reviews: [{ prNumber: 7, prUrl: "https://github.com/acme/todo/pull/7", status: "needs_review", targetSha: "head", title: "PR 7" }], runs: [] });
		if (url.pathname.endsWith("/projects/same-project")) return Response.json({ project: { config: { reviewers: [{ harness: "opencode" }] } } });
		if (url.pathname.endsWith("/agents/readiness") || url.pathname.endsWith("/agents/readiness/ensure")) return Response.json({ agents: [agentReadiness("opencode")] });
		if (url.pathname.endsWith("/workspace/files")) return Response.json({ files: [] });
		if (url.pathname.endsWith("/pr")) return Response.json({ prs: [] });
		if (url.pathname.endsWith("/interface-transition")) return Response.json({ supported: false });
		return Response.json({});
	}));
	await connectHost("http://box-a:3001");
	await connectHost("http://box-b:3001");
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	client.setQueryData(sessionReviewsQueryKey("same-session"), "local-sentinel");
	const renderInspector = (hostId: string) => render(<QueryClientProvider client={client}>
		<TooltipProvider><SessionInspector hostId={hostId} session={session(hostId)} /></TooltipProvider>
	</QueryClientProvider>);

	const a = renderInspector("box-a");
	await userEvent.click(screen.getByRole("switch", { name: "Automatically fix CI failures" }));
	await waitFor(() => expect(mutations).toContain("PATCH http://127.0.0.1:4000/api/v1/sessions/same-session/auto-inject-ci"));
	a.unmount();
	const b = renderInspector("box-b");
	await userEvent.click(screen.getByRole("tab", { name: "Reviews" }));
	await userEvent.click(await screen.findByRole("button", { name: "Review latest commit" }));
	await waitFor(() => expect(mutations).toContain("POST http://127.0.0.1:4001/api/v1/sessions/same-session/reviews/trigger"));
	expect(mutations).not.toContain("PATCH http://127.0.0.1:4001/api/v1/sessions/same-session/auto-inject-ci");
	expect(client.getQueryData(sessionReviewsQueryKey("same-session"))).toBe("local-sentinel");
	expect(client.getQueryData(sessionReviewsQueryKey("same-session", "box-b"))).toBeDefined();
	expect(localGet).not.toHaveBeenCalled();
	expect(localPost).not.toHaveBeenCalled();
	expect(localPatch).not.toHaveBeenCalled();
	expect(localPut).not.toHaveBeenCalled();
	b.unmount();
	const exited = { ...session("box-a"), status: "exited" as const, activity: { state: "exited" as const, lastActivityAt: "2026-09-28T00:00:00Z" } };
	const resumeView = render(<QueryClientProvider client={client}>
		<TooltipProvider><SessionInspector hostId="box-a" session={exited} /></TooltipProvider>
	</QueryClientProvider>);
	await userEvent.click(await screen.findByRole("button", { name: "Resume agent" }));
	await waitFor(() => expect(mutations).toContain("POST http://127.0.0.1:4000/api/v1/sessions/same-session/resume-agent"));
	resumeView.unmount();
});

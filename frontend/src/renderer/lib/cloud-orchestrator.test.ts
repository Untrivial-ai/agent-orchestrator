import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { settingsQueryKey, type Settings } from "../hooks/useSettings";
import type { CloudCpProviderConnection } from "./cloud-cp";
import { selectCloudOrchestratorHarness, spawnCloudOrchestrator } from "./cloud-orchestrator";

const h = vi.hoisted(() => ({
	me: vi.fn(),
	listProviderConnections: vi.fn(),
	listUserProviderConnections: vi.fn(),
	listProjects: vi.fn(),
	createSession: vi.fn(),
	beginCloudStartupAttempt: vi.fn(() => ({ attemptId: "attempt-orchestrator", startedAtMs: 10 })),
	bindCloudStartupAttempt: vi.fn(),
	captureRendererEvent: vi.fn(),
}));

vi.mock("../hooks/useCloudCp", () => ({
	createRendererCloudCpClient: () => ({
		me: h.me,
		listProviderConnections: h.listProviderConnections,
		listUserProviderConnections: h.listUserProviderConnections,
		listProjects: h.listProjects,
		createSession: h.createSession,
	}),
}));

vi.mock("../stores/sandbox-provider-store", () => ({ readSelectedSandboxProvider: () => null }));
vi.mock("./telemetry", () => ({ captureRendererEvent: h.captureRendererEvent }));
vi.mock("./cloud-startup-timing", () => ({
	beginCloudStartupAttempt: h.beginCloudStartupAttempt,
	bindCloudStartupAttempt: h.bindCloudStartupAttempt,
}));

beforeEach(() => {
	for (const mock of Object.values(h)) mock.mockReset();
	h.beginCloudStartupAttempt.mockReturnValue({ attemptId: "attempt-orchestrator", startedAtMs: 10 });
});

function connection(provider: string, validationState = "valid"): CloudCpProviderConnection {
	return {
		id: provider,
		provider,
		label: "default",
		config: {},
		validationState,
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
	};
}

describe("selectCloudOrchestratorHarness", () => {
	it("uses the connected harness when it is the only Cloud option", () => {
		expect(selectCloudOrchestratorHarness([connection("claude-code")])).toBe("claude-code");
	});

	it("preserves Codex as the preference when several supported agents are connected", () => {
		expect(selectCloudOrchestratorHarness([connection("cursor"), connection("codex")])).toBe("codex");
	});

	it("ignores invalid, non-default, and non-agent provider connections", () => {
		const invalid = connection("codex", "invalid");
		const nonDefault = { ...connection("claude-code"), label: "secondary" };
		expect(selectCloudOrchestratorHarness([invalid, nonDefault, connection("github")])).toBeUndefined();
	});
});

describe("spawnCloudOrchestrator", () => {
	function primeClient(project?: { id: string; config?: Record<string, unknown> }) {
		const queryClient = new QueryClient();
		queryClient.setQueryData<Settings>(settingsQueryKey, {
			cloudControlPlaneUrl: "https://cloud.example.com",
		} as Settings);
		h.me.mockResolvedValue({ organizations: [{ id: "org-1" }] });
		h.listUserProviderConnections.mockResolvedValue({ providerConnections: [] });
		h.listProjects.mockResolvedValue({ items: project ? [project] : [] });
		h.createSession.mockResolvedValue({ session: { id: "session-1" } });
		return queryClient;
	}

	it("binds the startup attempt to the created session", async () => {
		const queryClient = primeClient({ id: "project-1" });
		h.listProviderConnections.mockResolvedValue({ providerConnections: [connection("codex")] });
		h.createSession.mockResolvedValue({ session: { id: "orchestrator-1" } });

		await expect(spawnCloudOrchestrator(queryClient, "project-1")).resolves.toBe("orchestrator-1");
		expect(h.beginCloudStartupAttempt).toHaveBeenCalledOnce();
		expect(h.bindCloudStartupAttempt).toHaveBeenCalledWith("orchestrator-1", {
			attemptId: "attempt-orchestrator",
			startedAtMs: 10,
		});
	});

	it("starts without a user kickoff prompt so the role comes only from the system prompt", async () => {
		const queryClient = primeClient({ id: "project-1" });
		h.listProviderConnections.mockResolvedValue({
			providerConnections: [connection("claude-code")],
		});

		await expect(spawnCloudOrchestrator(queryClient, "project-1")).resolves.toBe("session-1");
		expect(h.createSession).toHaveBeenCalledWith("org-1", {
			projectId: "project-1",
			kind: "orchestrator",
			harness: "claude-code",
			displayName: "Orchestrator",
			prompt: "",
		});
	});

	it("honors the project's configured orchestrator agent over the Codex-first fallback", async () => {
		// The project picked Claude Code, but Codex is also connected (e.g. a
		// ChatGPT login). The configured choice must win instead of Codex-first.
		const queryClient = primeClient({
			id: "project-1",
			config: { orchestrator: { agent: "claude-code" } },
		});
		h.listProviderConnections.mockResolvedValue({
			providerConnections: [connection("codex"), connection("claude-code")],
		});

		await expect(spawnCloudOrchestrator(queryClient, "project-1")).resolves.toBe("session-1");
		expect(h.createSession).toHaveBeenCalledWith(
			"org-1",
			expect.objectContaining({ harness: "claude-code", kind: "orchestrator" }),
		);
	});

	it("falls back to the connected-credential priority when the configured agent is not connected", async () => {
		// Project configured Cursor, but only Codex is connected: fall back rather
		// than block the spawn on an unusable choice.
		const queryClient = primeClient({
			id: "project-1",
			config: { orchestrator: { agent: "cursor" } },
		});
		h.listProviderConnections.mockResolvedValue({
			providerConnections: [connection("codex")],
		});

		await expect(spawnCloudOrchestrator(queryClient, "project-1")).resolves.toBe("session-1");
		expect(h.createSession).toHaveBeenCalledWith(
			"org-1",
			expect.objectContaining({ harness: "codex" }),
		);
	});
});

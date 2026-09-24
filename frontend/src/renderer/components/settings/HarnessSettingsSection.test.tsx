import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "../../lib/api-client";
import { aoBridge } from "../../lib/bridge";
import { appI18n } from "../../i18n";
import { agentReadinessQueryKey, useAgentReadinessQuery, type AgentReadiness } from "../../hooks/useAgentReadinessQuery";
import type { TerminalSessionState } from "../../hooks/useTerminalSession";
import { agentReadiness } from "../../test/agent-readiness-fixtures";
import { HarnessSettingsSection } from "./HarnessSettingsSection";

const { terminalFocusRequested, terminalStateCallback } = vi.hoisted(() => ({
	terminalFocusRequested: { value: false },
	terminalStateCallback: { value: undefined as ((state: TerminalSessionState) => void) | undefined },
}));

vi.mock("../TerminalPane", () => ({
	TerminalPane: ({ focusRequested, onTerminalStateChange }: { focusRequested?: boolean; onTerminalStateChange?: (state: TerminalSessionState) => void }) => {
		terminalFocusRequested.value = focusRequested === true;
		terminalStateCallback.value = onTerminalStateChange;
		return (
			<div data-testid="inline-terminal-body">
				<button onClick={() => onTerminalStateChange?.("exited")}>Complete login terminal</button>
			</div>
		);
	},
}));

function catalogWithInstalled(...installed: string[]) {
	return {
		agents: [
			{ id: "claude-code", label: "Claude Code" },
			{ id: "codex", label: "Codex" },
			{ id: "cursor", label: "Cursor" },
			{ id: "goose", label: "Goose" },
		].map((agent) => ({
			...agent,
			installation: { state: installed.includes(agent.id) ? "installed" : "not_installed", freshness: "fresh", reason: "", reasonCode: "", attemptedAt: null, checkedAt: null },
			authentication: { state: "unknown", freshness: "fresh", reason: "", reasonCode: "", attemptedAt: null, checkedAt: null },
			effectiveReadiness: installed.includes(agent.id) ? "ready" : "not_ready",
			usageCount: 0,
		})),
	};
}

const catalog = catalogWithInstalled("claude-code");

const plans = {
	agents: [
		{
			agentId: "claude-code", available: true, automatic: true, method: "homebrew",
			command: "brew install --cask claude-code", documentationUrl: "https://code.claude.com/docs/en/installation",
			methods: [{ id: "homebrew", label: "Homebrew", available: true, recommended: true, command: "brew install --cask claude-code", reinstallAvailable: true, reinstallCommand: "brew reinstall --cask claude-code" }],
		},
		{
			agentId: "codex", available: true, automatic: true, method: "homebrew",
			command: "brew install --cask codex", documentationUrl: "https://github.com/openai/codex",
			methods: [
				{ id: "homebrew", label: "Homebrew", available: true, recommended: true, command: "brew install --cask codex", reinstallAvailable: true, reinstallCommand: "brew reinstall --cask codex" },
				{ id: "npm", label: "npm", available: true, recommended: false, command: "npm install -g @openai/codex", expectedDestination: "/Users/test/.npm/bin", reinstallAvailable: true, reinstallCommand: "npm install -g @openai/codex --force" },
			],
		},
		{
			agentId: "aider", available: true, automatic: true, method: "pipx",
			command: "pipx install aider-chat", documentationUrl: "https://aider.chat/docs/install.html",
			methods: [{ id: "pipx", label: "pipx", available: true, recommended: true, command: "pipx install aider-chat", reinstallAvailable: true, reinstallCommand: "pipx reinstall aider-chat" }],
		},
		{
			agentId: "cursor", available: true, automatic: true, method: "official-installer",
			command: "bash <downloaded from https://cursor.com/install>", documentationUrl: "https://cursor.com/cli",
			methods: [{ id: "official-installer", label: "Official installer", available: true, recommended: true, command: "bash <downloaded from https://cursor.com/install>", reinstallAvailable: false, reinstallReason: "No headless reinstall" }],
		},
		{
			agentId: "goose", available: true, automatic: true, method: "official-installer",
			command: "pwsh.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File <downloaded from https://raw.githubusercontent.com/aaif-goose/goose/main/download_cli.ps1>",
			documentationUrl: "https://goose-docs.ai/docs/getting-started/installation/",
			methods: [{ id: "official-installer", label: "Official installer", available: true, recommended: true, command: "pwsh.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File <downloaded from https://raw.githubusercontent.com/aaif-goose/goose/main/download_cli.ps1>", reinstallAvailable: false, reinstallReason: "No headless reinstall" }],
		},
	],
};

function ReadinessSelector({ agentId }: { agentId: string }) {
	const readiness = useAgentReadinessQuery();
	return (
		<div data-testid="originating-selector">
			{readiness.data?.agents.find((agent) => agent.id === agentId)?.effectiveReadiness}
		</div>
	);
}

function renderSection(focusAgentId?: string, selectorAgentId?: string) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const view = render(
		<QueryClientProvider client={client}>
			{selectorAgentId ? <ReadinessSelector agentId={selectorAgentId} /> : null}
			<HarnessSettingsSection focusAgentId={focusAgentId} />
		</QueryClientProvider>,
	);
	return { ...view, client };
}

describe("HarnessSettingsSection", () => {
	it("shows Junie as an experimental terminal agent with its official logo", async () => {
		renderSection();
		const row = (await screen.findByText("Junie")).closest('[data-agent="junie"]');
		expect(row).not.toBeNull();
		expect(row).toHaveTextContent("Experimental");
		expect(row).toHaveTextContent("Terminal UI");
		expect(row?.querySelector('img[src*="junie"]')).not.toBeNull();
	});

	beforeEach(async () => {
		await appI18n.changeLanguage("en");
		terminalFocusRequested.value = false;
		terminalStateCallback.value = undefined;
		window.ao!.clipboard.writeText = vi.fn().mockResolvedValue(undefined);
		vi.spyOn(apiClient, "GET").mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: catalog } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			return { data: undefined } as never;
		});
		vi.spyOn(apiClient, "POST").mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness/ensure") return { data: catalog } as never;
			if (path === "/api/v1/agents/refresh") return { data: catalog } as never;
			if (path === "/api/v1/agents/{agent}/install") {
				return { data: { target: "codex", status: "failed", error: "npm failed" } } as never;
			}
			return { data: undefined } as never;
		});
	});

	afterEach(() => {
		vi.useRealTimers();
		vi.restoreAllMocks();
	});

	it("keeps a targeted harness visible, scrolls it, focuses Install, and highlights it for two seconds once", async () => {
		const scrollIntoView = vi.fn();
		const setTimeoutSpy = vi.spyOn(window, "setTimeout");
		Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: scrollIntoView });
		const view = renderSection("codex");
		const row = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;
		const install = await within(row).findByRole("button", { name: "Install" });

		await waitFor(() => expect(document.activeElement).toBe(install));
		expect(scrollIntoView).toHaveBeenCalledWith({ behavior: "smooth", block: "center" });
		expect(row).toHaveAttribute("data-focus-highlighted");

		fireEvent.change(screen.getByRole("textbox", { name: "Search harnesses" }), { target: { value: "Claude" } });
		expect(document.querySelector('[data-agent="codex"]')).toBe(row);

		const highlightTimeout = setTimeoutSpy.mock.calls.find(([, delay]) => delay === 2_000)?.[0];
		expect(highlightTimeout).toBeTypeOf("function");
		act(() => highlightTimeout?.());
		expect(row).not.toHaveAttribute("data-focus-highlighted");

		view.rerender(
			<QueryClientProvider client={view.client}>
				<HarnessSettingsSection focusAgentId="cursor" />
			</QueryClientProvider>,
		);
		expect(scrollIntoView).toHaveBeenCalledTimes(1);
	});

	it("focuses Login for an installed harness when authentication is required", async () => {
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: catalog } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			if (path === "/api/v1/agents/auth-plans") {
				return { data: { plans: [{ agentId: "claude-code", action: "login", launchMode: "terminal", available: true }] } } as never;
			}
			return { data: undefined } as never;
		});
		Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
		renderSection("claude-code");
		const row = (await screen.findByText("Claude Code")).closest('[data-agent="claude-code"]') as HTMLElement;
		const login = await within(row).findByRole("button", { name: "Login" });

		await waitFor(() => expect(document.activeElement).toBe(login));
	});

	it("falls back to the targeted row when it has no primary action", async () => {
		Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
		renderSection("claude-code");
		const row = (await screen.findByText("Claude Code")).closest('[data-agent="claude-code"]') as HTMLElement;

		await waitFor(() => expect(document.activeElement).toBe(row));
		expect(row).toHaveAttribute("tabindex", "-1");
	});

	it("does not scroll, focus, or highlight for an unknown harness id", async () => {
		const scrollIntoView = vi.fn();
		Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: scrollIntoView });
		renderSection("not-a-harness");
		await screen.findByText("Codex");
		await waitFor(() => expect(apiClient.GET).toHaveBeenCalledWith("/api/v1/agents/install-jobs"));

		expect(scrollIntoView).not.toHaveBeenCalled();
		expect(document.querySelector("[data-focus-highlighted]")).toBeNull();
		expect(document.activeElement).toBe(document.body);
	});

	it("shows installed harnesses and install actions without authentication UI", async () => {
		renderSection();
		await waitFor(() => expect(screen.getAllByText("Installed").length).toBeGreaterThan(0), { timeout: 10_000 });
		expect(screen.getByText("Codex")).toBeInTheDocument();
		expect(screen.queryByText(/sign in/i)).not.toBeInTheDocument();
	});

	it("shows the authentication action for an installed agent and opens documentation", async () => {
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: catalog } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			if (path === "/api/v1/agents/auth-plans") {
				return { data: { plans: [{ agentId: "claude-code", action: "login", launchMode: "documentation", available: true, documentationUrl: "https://example.test/login" }] } } as never;
			}
			return { data: undefined } as never;
		});
		const openExternal = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		renderSection();
		const row = (await screen.findByText("Claude Code")).closest('[data-agent="claude-code"]') as HTMLElement;
		const login = await within(row).findByRole("button", { name: "Login" });
		await userEvent.click(login);
		expect(openExternal).toHaveBeenCalledWith("https://example.test/login");
	});

	it("shows cached readiness while silently refreshing when the page opens", async () => {
		const refreshed = catalogWithInstalled("claude-code", "codex");
		let current = catalog;
		let resolveRefresh!: (value: { data: typeof refreshed }) => void;
		const pendingRefresh = new Promise<{ data: typeof refreshed }>((resolve) => { resolveRefresh = resolve; });
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: current } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			if (path === "/api/v1/agents/auth-plans") return { data: { plans: [] } } as never;
			return { data: undefined } as never;
		});
		vi.mocked(apiClient.POST).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/refresh") return await pendingRefresh as never;
			return { data: undefined } as never;
		});

		renderSection();
		const row = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;

		await waitFor(() => expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/agents/refresh"));
		await within(row).findByRole("button", { name: "Install" });
		expect(screen.queryByRole("button", { name: "Refresh harness status" })).not.toBeInTheDocument();
		expect(screen.queryByText("Checking…")).not.toBeInTheDocument();
		await act(async () => {
			current = refreshed;
			resolveRefresh({ data: refreshed });
		});
		await waitFor(() => {
			expect(within(row).getByRole("button", { name: "Installed" })).toBeDisabled();
		});
	});

	it("runs a fresh authentication check after terminal completion", async () => {
		const authorized = catalogWithInstalled("claude-code");
		authorized.agents[0].authentication.state = "authorized";
		let probeCalls = 0;
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: catalog } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			if (path === "/api/v1/agents/auth-plans") return { data: { plans: [
				{ agentId: "claude-code", action: "login", launchMode: "terminal", available: true },
			] } } as never;
			return { data: undefined } as never;
		});
		vi.mocked(apiClient.POST).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/{agent}/probe") {
				probeCalls += 1;
				return { data: { agent: { id: "claude-code", label: "Claude Code", authStatus: "authorized" }, supported: true, installed: true } } as never;
			}
			if (path === "/api/v1/agents/readiness/ensure") return { data: authorized } as never;
			if (path === "/api/v1/agents/{agent}/auth") return { data: {
				agentId: "claude-code",
				action: "login",
				guidance: "Complete login in the terminal.",
				terminal: {
					handleId: "auth-terminal-1",
					title: "Claude Code login",
					workingDir: "/tmp",
					createdAt: "2026-09-15T00:00:00Z",
				},
			} } as never;
			return { data: undefined } as never;
		});
		const close = vi.spyOn(apiClient, "DELETE").mockResolvedValue({ data: undefined } as never);
		const user = userEvent.setup();

		renderSection();
		const row = (await screen.findByText("Claude Code")).closest('[data-agent="claude-code"]') as HTMLElement;
		const login = await within(row).findByRole("button", { name: "Login" });
		await user.click(login);
		await within(row).findByTestId("inline-terminal-body");
		expect(terminalStateCallback.value).toBeDefined();
		expect(terminalFocusRequested.value).toBe(false);

		act(() => terminalStateCallback.value?.("attached"));
		await waitFor(() => expect(terminalFocusRequested.value).toBe(true));

		act(() => terminalStateCallback.value?.("exited"));
		await waitFor(() => expect(probeCalls).toBe(1));

		await waitFor(() => expect(close).toHaveBeenCalledWith("/api/v1/shell-terminals/{handleId}", {
			params: { path: { handleId: "auth-terminal-1" } },
		}));
		await waitFor(() => expect(within(row).queryByTestId("inline-terminal-body")).not.toBeInTheDocument());
	});

	it("refreshes authentication when the user closes the login terminal", async () => {
		const authorized = catalogWithInstalled("claude-code");
		authorized.agents[0].authentication.state = "authorized";
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: catalog } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			if (path === "/api/v1/agents/auth-plans") return { data: { plans: [
				{ agentId: "claude-code", action: "login", launchMode: "terminal", available: true },
			] } } as never;
			return { data: undefined } as never;
		});
		vi.mocked(apiClient.POST).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/{agent}/probe") {
				return { data: { agent: { id: "claude-code", label: "Claude Code", authStatus: "authorized" }, supported: true, installed: true } } as never;
			}
			if (path === "/api/v1/agents/readiness/ensure") return { data: authorized } as never;
			if (path === "/api/v1/agents/{agent}/auth") return { data: {
				agentId: "claude-code",
				action: "login",
				guidance: "Complete login in the terminal.",
				terminal: {
					handleId: "auth-terminal-close",
					title: "Claude Code login",
					workingDir: "/tmp",
					createdAt: "2026-09-15T00:00:00Z",
				},
			} } as never;
			return { data: undefined } as never;
		});
		const closeTerminal = vi.spyOn(apiClient, "DELETE").mockResolvedValue({ data: undefined } as never);
		const user = userEvent.setup();

		renderSection();
		const row = (await screen.findByText("Claude Code")).closest('[data-agent="claude-code"]') as HTMLElement;
		await user.click(await within(row).findByRole("button", { name: "Login" }));
		await user.click(await within(row).findByRole("button", { name: "Close settings" }));

		await waitFor(() => expect(closeTerminal).toHaveBeenCalledWith("/api/v1/shell-terminals/{handleId}", {
			params: { path: { handleId: "auth-terminal-close" } },
		}));
		await waitFor(() => expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/agents/{agent}/probe", {
			params: { path: { agent: "claude-code" } },
		}));
		await within(row).findByRole("button", { name: "Authorized" });
	});

	it("uses Set up for a completed setup action", async () => {
		const authorized = catalogWithInstalled("codex");
		authorized.agents[1].authentication.state = "authorized";
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: authorized } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			if (path === "/api/v1/agents/auth-plans") return { data: { plans: [
				{ agentId: "codex", action: "setup", launchMode: "terminal", available: true },
			] } } as never;
			return { data: undefined } as never;
		});
		vi.mocked(apiClient.POST).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness/ensure") return { data: authorized } as never;
			if (path === "/api/v1/agents/{agent}/probe") return { data: { agent: { id: "codex", label: "Codex", authStatus: "authorized" }, supported: true, installed: true } } as never;
			return { data: undefined } as never;
		});

		renderSection();
		const row = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;

		await within(row).findByText("Set up");
	});

	it("does not expose manual readiness controls", async () => {
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: catalog } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			if (path === "/api/v1/agents/auth-plans") return { data: { plans: [
				{ agentId: "claude-code", action: "login", launchMode: "terminal", available: true },
			] } } as never;
			return { data: undefined } as never;
		});
		renderSection();
		const row = (await screen.findByText("Claude Code")).closest('[data-agent="claude-code"]') as HTMLElement;
		expect(await within(row).findByRole("button", { name: "Login" })).toBeInTheDocument();
		expect(within(row).queryByRole("button", { name: "Installed" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Refresh harness status" })).not.toBeInTheDocument();
		expect(within(row).queryByRole("button", { name: "Check login" })).not.toBeInTheDocument();
		expect(within(row).queryByRole("button", { name: "Check configuration" })).not.toBeInTheDocument();
	});

	it("shows a neutral unknown state without install controls before the first observation", async () => {
		const unknown = catalogWithInstalled();
		unknown.agents[1].installation.state = "unknown";
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: unknown } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			if (path === "/api/v1/agents/auth-plans") return { data: { plans: [] } } as never;
			return { data: undefined } as never;
		});

		renderSection();
		const row = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;
		expect(await within(row).findByText("Installation status unknown")).toBeInTheDocument();
		expect(within(row).queryByRole("button", { name: "Install" })).not.toBeInTheDocument();
	});

	it("falls back to installer plans when readiness cannot be loaded", async () => {
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { error: { message: "readiness unavailable" } } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			if (path === "/api/v1/agents/auth-plans") return { data: { plans: [] } } as never;
			return { data: undefined } as never;
		});

		renderSection();
		const row = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;
		// The readiness query retries once before surfacing its error.
		expect(await within(row).findByRole("button", { name: "Install" }, { timeout: 5_000 })).toBeInTheDocument();
		expect(within(row).queryByText("Installation status unknown")).not.toBeInTheDocument();
	});

	it("falls back to ensuring readiness when the page-open refresh fails", async () => {
		vi.mocked(apiClient.POST).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/refresh") return { error: { message: "refresh failed" } } as never;
			if (path === "/api/v1/agents/readiness/ensure") return { data: catalog } as never;
			return { data: undefined } as never;
		});

		renderSection();
		await waitFor(() => expect(apiClient.POST).toHaveBeenCalledWith(
			"/api/v1/agents/readiness/ensure",
			{ body: { agentIds: [], purpose: "display" } },
		));
	});

	it("re-fetches readiness when both the page-open refresh and ensure fail", async () => {
		vi.mocked(apiClient.POST).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/refresh") throw new Error("network down");
			if (path === "/api/v1/agents/readiness/ensure") throw new Error("network down");
			return { data: undefined } as never;
		});
		let readinessFetches = 0;
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") {
				readinessFetches += 1;
				return { data: catalog } as never;
			}
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			return { data: undefined } as never;
		});

		renderSection();
		await screen.findByText("Codex");
		await waitFor(() => expect(readinessFetches).toBeGreaterThanOrEqual(2));
	});

	it("sorts harnesses by authentication state while preserving catalog order within each group", async () => {
		const readiness = catalogWithInstalled("claude-code", "codex", "cursor", "goose");
		readiness.agents[0].authentication.state = "unknown";
		readiness.agents[1].authentication.state = "authorized";
		readiness.agents[2].authentication.state = "unauthorized";
		readiness.agents[3].authentication.state = "not_applicable";
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: readiness } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			return { data: undefined } as never;
		});

		renderSection();

		await waitFor(() => {
			const agentIds = Array.from(document.querySelectorAll<HTMLElement>("[data-agent]"))
				.map((row) => row.dataset.agent);
			expect(agentIds.slice(0, 4)).toEqual(["codex", "goose", "cursor", "claude-code"]);
		});
	});

	it("starts the fixed daemon install route and exposes retry after failure", async () => {
		const user = userEvent.setup();
		renderSection();
		await screen.findByText("Codex");
		const codexRow = document.querySelector('[data-agent="codex"]');
		expect(codexRow).not.toBeNull();
		await waitFor(() => expect(codexRow).toHaveTextContent("Available via Homebrew"), { timeout: 10_000 });
		await user.click(within(codexRow as HTMLElement).getByRole("button", { name: "Installation method" }));
		await user.click(await screen.findByRole("menuitem", { name: "npm" }));
		await user.click(within(codexRow as HTMLElement).getByRole("button", { name: "Install" }));

		await waitFor(() => expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/agents/{agent}/install", {
			params: { path: { agent: "codex" } },
			body: { method: "npm", operation: "install" },
		}));
		await waitFor(() => expect(codexRow).toHaveTextContent("npm failed"));
		expect(codexRow).toHaveTextContent("Retry");
		await user.click(within(codexRow as HTMLElement).getByRole("button", { name: "Installation method" }));
		await user.click(await screen.findByRole("menuitem", { name: "Homebrew" }));
		await user.click(within(codexRow as HTMLElement).getByRole("button", { name: "Retry" }));
		await waitFor(() => expect(apiClient.POST).toHaveBeenLastCalledWith("/api/v1/agents/{agent}/install", {
			params: { path: { agent: "codex" } },
			body: { method: "homebrew", operation: "install" },
		}));
	});

	it("automatically uses the installer available on the user's machine", async () => {
		const npmOnlyPlans = {
			agents: plans.agents.map((plan) => plan.agentId === "codex" ? {
				...plan,
				method: "npm",
				methods: plan.methods.map((method) => ({
					...method,
					available: method.id === "npm",
					recommended: method.id === "npm",
				})),
			} : plan),
		};
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: catalog } as never;
			if (path === "/api/v1/agents/installers") return { data: npmOnlyPlans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			return { data: undefined } as never;
		});
		const user = userEvent.setup();
		renderSection();
		const row = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;

		await waitFor(() => expect(row).toHaveTextContent("Available via npm"));
		expect(within(row).queryByRole("combobox", { name: "Installation method" })).not.toBeInTheDocument();
		await user.click(within(row).getByRole("button", { name: "Install" }));

		await waitFor(() => expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/agents/{agent}/install", {
			params: { path: { agent: "codex" } },
			body: { method: "npm", operation: "install" },
		}));
	});

	it("does not offer reinstall actions for installed harnesses", async () => {
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: catalogWithInstalled("claude-code", "cursor") } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			return { data: undefined } as never;
		});
		renderSection();
		const claudeRow = (await screen.findByText("Claude Code")).closest('[data-agent="claude-code"]') as HTMLElement;
		const cursorRow = (await screen.findByText("Cursor")).closest('[data-agent="cursor"]') as HTMLElement;

		expect(claudeRow).toHaveTextContent("Installed");
		expect(within(claudeRow).queryByRole("button", { name: "Reinstall" })).not.toBeInTheDocument();
		expect(within(cursorRow).queryByRole("button", { name: "Reinstall" })).not.toBeInTheDocument();
		expect(within(cursorRow).queryByRole("button", { name: "Instructions" })).not.toBeInTheDocument();
	});

	it("starts an official vendor installer with one click and no instructions dialog", async () => {
		vi.mocked(apiClient.POST).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/{agent}/install") {
				return { data: { target: "cursor", status: "installing", method: "official-installer" } } as never;
			}
			return { data: undefined } as never;
		});
		const user = userEvent.setup();
		renderSection();
		const row = (await screen.findByText("Cursor")).closest('[data-agent="cursor"]') as HTMLElement;
		await waitFor(() => expect(row).toHaveTextContent("Available via Official"));
		expect(within(row).queryByRole("button", { name: "Instructions" })).not.toBeInTheDocument();

		await user.click(within(row).getByRole("button", { name: "Install" }));

		await waitFor(() => expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/agents/{agent}/install", {
			params: { path: { agent: "cursor" } },
			body: { method: "official-installer", operation: "install" },
		}));
		expect(row).toHaveTextContent("Installing…");
	});

	it("shows the official Goose installer", async () => {
		renderSection();
		const row = (await screen.findByText("Goose")).closest('[data-agent="goose"]') as HTMLElement;
		await waitFor(() => expect(row).toHaveTextContent("Available via Official"));
		expect(within(row).getByRole("button", { name: "Install" })).toBeInTheDocument();
	});

	it("does not treat a historical successful job as current installation inventory", async () => {
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: catalog } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [{ target: "codex", status: "succeeded", method: "npm", updatedAt: "2026-08-01T00:00:00Z" }] } } as never;
			return { data: undefined } as never;
		});
		renderSection();
		const row = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;
		await waitFor(() => expect(row).toHaveTextContent("Available via Homebrew & npm"));
		expect(within(row).getByRole("button", { name: "Install" })).toBeEnabled();
		expect(apiClient.POST).not.toHaveBeenCalledWith("/api/v1/agents/{agent}/probe", expect.anything());
	});

	it("probes the installed harness after an observed install completes", async () => {
		let installed = false;
		let installerFetches = 0;
		let jobFetches = 0;
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: installed ? catalogWithInstalled("claude-code", "codex") : catalog } as never;
			if (path === "/api/v1/agents/installers") {
				installerFetches += 1;
				return { data: plans } as never;
			}
			if (path === "/api/v1/agents/install-jobs") {
				jobFetches += 1;
				return { data: { jobs: [{
					target: "codex",
					status: jobFetches === 1 ? "installing" : "succeeded",
					method: "npm",
					updatedAt: "2026-08-31T00:00:00Z",
				}] } } as never;
			}
			return { data: undefined } as never;
		});
		vi.mocked(apiClient.POST).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/{agent}/probe") {
				installed = true;
				return { data: { agent: { id: "codex", label: "Codex" }, supported: true, installed: true } } as never;
			}
			if (path === "/api/v1/agents/readiness/ensure") {
				return { data: installed ? catalogWithInstalled("claude-code", "codex") : catalog } as never;
			}
			return { data: undefined } as never;
		});
		renderSection();
		const row = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;
		await waitFor(() => expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/agents/{agent}/probe", { params: { path: { agent: "codex" } } }), { timeout: 3_000 });
		await waitFor(() => expect(row).toHaveTextContent("Installed"));
		await waitFor(() => expect(installerFetches).toBe(2));
	});

	it.each(["authorized", "unauthorized"] as const)("updates a mounted readiness consumer after installation returns %s", async (authentication) => {
		const initial = { agents: [
			agentReadiness("claude-code", "Claude Code"),
			agentReadiness("codex", "Codex", { installation: "not_installed", authentication: "unknown" }),
		] };
		let probed = false;
		const updated = agentReadiness("codex", "Codex", { authentication });
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: initial } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			return { data: undefined } as never;
		});
		vi.mocked(apiClient.POST).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness/ensure") return { data: probed ? { agents: [updated] } : initial } as never;
			if (path === "/api/v1/agents/{agent}/install") return { data: { target: "codex", status: "succeeded", method: "homebrew", updatedAt: "2026-09-19T00:00:00Z" } } as never;
			if (path === "/api/v1/agents/{agent}/probe") {
				probed = true;
				return { data: { agent: { id: "codex", authStatus: authentication }, installed: true } } as never;
			}
			return { data: undefined } as never;
		});
		const { client } = renderSection(undefined, "codex");
		const selector = screen.getByTestId("originating-selector");
		await waitFor(() => expect(selector).toHaveTextContent("not_ready"));
		const row = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;
		await userEvent.click(await within(row).findByRole("button", { name: "Install" }));

		await waitFor(() => expect(row).toHaveTextContent(authentication === "authorized" ? "Authorized" : "Installed"));
		await waitFor(() => expect(selector).toHaveTextContent(authentication === "authorized" ? /^ready$/ : /^not_ready$/));
		expect(client.getQueryData<AgentReadiness>(agentReadinessQueryKey)?.agents).toEqual([initial.agents[0], updated]);
		expect(screen.getByTestId("originating-selector")).toBe(selector);
	});

	it("updates a mounted readiness consumer when the Harness authentication terminal completes", async () => {
		const initial = { agents: [
			agentReadiness("claude-code", "Claude Code"),
			agentReadiness("codex", "Codex", { authentication: "unauthorized" }),
		] };
		const authorized = agentReadiness("codex", "Codex");
		let probed = false;
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: initial } as never;
			if (path === "/api/v1/agents/auth-plans") return { data: { plans: [{ agentId: "codex", action: "login", launchMode: "terminal", available: true }] } } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			return { data: undefined } as never;
		});
		vi.mocked(apiClient.POST).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness/ensure") return { data: probed ? { agents: [authorized] } : initial } as never;
			if (path === "/api/v1/agents/{agent}/auth") return { data: {
				agentId: "codex", action: "login", terminal: { handleId: "auth-codex", projectId: null, sessionId: null, workingDir: "/tmp", title: "Codex login", createdAt: "2026-09-19T00:00:00Z" },
			} } as never;
			if (path === "/api/v1/agents/{agent}/probe") {
				probed = true;
				return { data: { agent: { id: "codex", authStatus: "authorized" }, installed: true } } as never;
			}
			return { data: undefined } as never;
		});
		vi.spyOn(apiClient, "DELETE").mockResolvedValue({ data: undefined } as never);
		Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
		const { client } = renderSection(undefined, "codex");
		const selector = screen.getByTestId("originating-selector");
		await waitFor(() => expect(selector).toHaveTextContent("not_ready"));
		const row = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;
		await userEvent.click(await within(row).findByRole("button", { name: "Login" }));
		await userEvent.click(await screen.findByRole("button", { name: "Complete login terminal" }));

		await waitFor(() => expect(selector).toHaveTextContent(/^ready$/));
		expect(client.getQueryData<AgentReadiness>(agentReadinessQueryKey)?.agents).toEqual([initial.agents[0], authorized]);
		expect(screen.getByTestId("originating-selector")).toBe(selector);
		await waitFor(() => expect(screen.queryByRole("button", { name: "Complete login terminal" })).not.toBeInTheDocument());
	});

	it("admits only one install request per harness while the first POST is pending", async () => {
		let resolveInstall!: (value: unknown) => void;
		let installCalls = 0;
		const pendingInstall = new Promise((resolve) => { resolveInstall = resolve; });
		vi.mocked(apiClient.POST).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/{agent}/install") {
				installCalls += 1;
				return await pendingInstall as never;
			}
			return { data: undefined } as never;
		});
		const user = userEvent.setup();
		renderSection();
		const row = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;
		const button = await within(row).findByRole("button", { name: "Install" });
		await user.dblClick(button);
		expect(installCalls).toBe(1);
		resolveInstall({ data: { target: "codex", status: "installing", method: "homebrew" } });
		await waitFor(() => expect(row).toHaveTextContent("Installing…"));
	});

	it("keeps concurrent installs independent with only one spinner status per row", async () => {
		vi.mocked(apiClient.POST).mockImplementation(async (path, options) => {
			if (path === "/api/v1/agents/{agent}/install") {
				const agent = (options as { params: { path: { agent: string } } }).params.path.agent;
				return { data: { target: agent, status: "installing" } } as never;
			}
			return { data: undefined } as never;
		});
		const user = userEvent.setup();
		renderSection();
		const codexRow = (await screen.findByText("Codex")).closest('[data-agent="codex"]');
		const aiderRow = (await screen.findByText("Aider")).closest('[data-agent="aider"]');
		expect(codexRow).not.toBeNull();
		expect(aiderRow).not.toBeNull();
		await waitFor(() => expect(within(codexRow as HTMLElement).getByRole("button", { name: "Install" })).toBeEnabled());

		await user.click(within(codexRow as HTMLElement).getByRole("button", { name: "Install" }));
		const codexStatus = await within(codexRow as HTMLElement).findByRole("status");
		await user.click(within(aiderRow as HTMLElement).getByRole("button", { name: "Install" }));

		const aiderStatus = await within(aiderRow as HTMLElement).findByRole("status");
		expect(codexStatus.querySelector("svg.animate-spin")).not.toBeNull();
		expect(aiderStatus.querySelector("svg.animate-spin")).not.toBeNull();
		expect(within(codexRow as HTMLElement).queryByRole("progressbar")).not.toBeInTheDocument();
		expect(within(aiderRow as HTMLElement).queryByRole("progressbar")).not.toBeInTheDocument();
		expect(within(codexRow as HTMLElement).getAllByText("Installing…")).toHaveLength(1);
		expect(within(aiderRow as HTMLElement).getAllByText("Installing…")).toHaveLength(1);
	});

	it("hydrates interrupted jobs and offers verification", async () => {
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: catalog } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [{ target: "codex", status: "interrupted", method: "npm", error: "AO restarted", output: "partial output", expectedDestination: "/Users/test/.npm/bin/codex" }] } } as never;
			return { data: undefined } as never;
		});
		vi.mocked(apiClient.POST).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/{agent}/verify") return { data: { target: "codex", status: "verifying" } } as never;
			if (path === "/api/v1/agents/{agent}/install") return { data: { target: "codex", status: "installing", method: "npm" } } as never;
			return { data: undefined } as never;
		});
		const user = userEvent.setup();
		renderSection();
		const row = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;
		await waitFor(() => expect(row).toHaveTextContent("Interrupted"));
		await user.click(within(row).getByRole("button", { name: "Verify again" }));
		expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/agents/{agent}/verify", { params: { path: { agent: "codex" } } });
		await waitFor(() => expect(row).toHaveTextContent("Verifying…"));
	});

	it("shows and copies daemon diagnostics", async () => {
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: catalog } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [{ target: "codex", status: "failed", method: "npm", error: "exit status 1", output: "permission denied", expectedDestination: "/Users/test/.npm/bin/codex" }] } } as never;
			return { data: undefined } as never;
		});
		const user = userEvent.setup();
		renderSection();
		const row = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;
		await user.click(await within(row).findByRole("button", { name: "Show diagnostics" }));
		expect(row).toHaveTextContent("permission denied");
		expect(row).toHaveTextContent("/Users/test/.npm/bin/codex");
		await user.click(within(row).getByRole("button", { name: "Copy diagnostics" }));
		expect(window.ao!.clipboard.writeText).toHaveBeenCalledWith(expect.stringContaining("permission denied"));
	});

	it("surfaces install job polling failures", async () => {
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: catalog } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { error: { error: { message: "Could not poll installation status." } } } as never;
			return { data: undefined } as never;
		});
		renderSection();
		expect(await screen.findByText("Could not poll installation status.")).toBeInTheDocument();
	});
});

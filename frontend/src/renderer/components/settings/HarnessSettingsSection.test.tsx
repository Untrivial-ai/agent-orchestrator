import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "../../lib/api-client";
import { aoBridge } from "../../lib/bridge";
import { appI18n } from "../../i18n";
import { HarnessSettingsSection } from "./HarnessSettingsSection";

const { terminalStateCallback } = vi.hoisted(() => ({
	terminalStateCallback: { value: undefined as ((state: "exited") => void) | undefined },
}));

vi.mock("../TerminalPane", () => ({
	TerminalPane: ({ onTerminalStateChange }: { onTerminalStateChange?: (state: "exited") => void }) => {
		terminalStateCallback.value = onTerminalStateChange;
		return <div data-testid="inline-terminal-body" />;
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

function renderSection() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={client}>
			<HarnessSettingsSection />
		</QueryClientProvider>,
	);
}

describe("HarnessSettingsSection", () => {
	beforeEach(async () => {
		await appI18n.changeLanguage("en");
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

	afterEach(() => vi.restoreAllMocks());

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

	it("ensures and caches readiness when the page opens", async () => {
		const refreshed = catalogWithInstalled("claude-code", "codex");
		let resolveEnsure!: (value: { data: typeof refreshed }) => void;
		const pendingEnsure = new Promise<{ data: typeof refreshed }>((resolve) => { resolveEnsure = resolve; });
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: catalog } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			if (path === "/api/v1/agents/auth-plans") return { data: { plans: [] } } as never;
			return { data: undefined } as never;
		});
		vi.mocked(apiClient.POST).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness/ensure") return await pendingEnsure as never;
			return { data: undefined } as never;
		});

		renderSection();
		const row = (await screen.findByText("Codex")).closest('[data-agent="codex"]') as HTMLElement;

		await waitFor(() => expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/agents/readiness/ensure", {
			body: { agentIds: [], purpose: "display" },
		}));
		await within(row).findByRole("button", { name: "Install" });
		await act(async () => {
			resolveEnsure({ data: refreshed });
		});
		await waitFor(() => {
			expect(within(row).getByRole("button", { name: "Installed" })).toBeDisabled();
		});
	});

	it("shows a compact authentication status with a separate manual recheck control", async () => {
		const authorized = catalogWithInstalled("claude-code");
		authorized.agents[0].authentication.state = "authorized";
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: authorized } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			if (path === "/api/v1/agents/auth-plans") return { data: { plans: [
				{ agentId: "claude-code", action: "login", launchMode: "terminal", available: true },
			] } } as never;
			return { data: undefined } as never;
		});
		vi.mocked(apiClient.POST).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness/ensure") return { data: authorized } as never;
			if (path === "/api/v1/agents/{agent}/probe") return { data: { agent: { id: "claude-code", label: "Claude Code", authStatus: "authorized" }, supported: true, installed: true } } as never;
			return { data: undefined } as never;
		});
		const user = userEvent.setup();
		renderSection();
		const row = (await screen.findByText("Claude Code")).closest('[data-agent="claude-code"]') as HTMLElement;
		await within(row).findByText("Connected");
		const recheck = within(row).getByRole("button", { name: "Claude Code: Check login" });
		expect(recheck).not.toHaveTextContent("Connected");
		vi.mocked(apiClient.POST).mockClear();

		await user.click(recheck);

		await waitFor(() => expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/agents/{agent}/probe", {
			params: { path: { agent: "claude-code" } },
		}));
	});

	it("runs a fresh authentication check after terminal completion", async () => {
		const authorized = catalogWithInstalled("claude-code");
		authorized.agents[0].authentication.state = "authorized";
		let resolveInitialProbe!: (value: unknown) => void;
		const pendingInitialProbe = new Promise((resolve) => { resolveInitialProbe = resolve; });
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
				if (probeCalls === 1) return await pendingInitialProbe as never;
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
		const checkLogin = await within(row).findByRole(
			"button",
			{ name: "Claude Code: Check login" },
			{ timeout: 10_000 },
		);
		await user.click(checkLogin);
		await waitFor(() => expect(probeCalls).toBe(1));
		const login = await within(row).findByRole("button", { name: "Login" });
		await user.click(login);
		await within(row).findByTestId("inline-terminal-body");
		expect(terminalStateCallback.value).toBeDefined();

		act(() => terminalStateCallback.value?.("exited"));
		expect(probeCalls).toBe(1);
		resolveInitialProbe({ data: { agent: { id: "claude-code", label: "Claude Code", authStatus: "unauthorized" }, supported: true, installed: true } });
		await waitFor(() => expect(probeCalls).toBe(2));

		await waitFor(() => expect(close).toHaveBeenCalledWith("/api/v1/shell-terminals/{handleId}", {
			params: { path: { handleId: "auth-terminal-1" } },
		}));
		await waitFor(() => expect(within(row).queryByTestId("inline-terminal-body")).not.toBeInTheDocument());
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

	it("does not show a global harness refresh control", async () => {
		renderSection();
		await screen.findByText("Claude Code");
		expect(screen.queryByRole("button", { name: "Refresh harness status" })).not.toBeInTheDocument();
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

	it("offers a safe reinstall action for installed harnesses", async () => {
		vi.mocked(apiClient.GET).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/readiness") return { data: catalogWithInstalled("claude-code", "cursor") } as never;
			if (path === "/api/v1/agents/installers") return { data: plans } as never;
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } } as never;
			return { data: undefined } as never;
		});
		vi.mocked(apiClient.POST).mockImplementation(async (path) => {
			if (path === "/api/v1/agents/{agent}/install") {
				return { data: { target: "claude-code", status: "installing", method: "homebrew" } } as never;
			}
			return { data: undefined } as never;
		});
		const user = userEvent.setup();
		renderSection();
		const claudeRow = (await screen.findByText("Claude Code")).closest('[data-agent="claude-code"]') as HTMLElement;
		const cursorRow = (await screen.findByText("Cursor")).closest('[data-agent="cursor"]') as HTMLElement;

		expect(claudeRow).toHaveTextContent("Installed");
		await user.click(within(claudeRow).getByRole("button", { name: "Reinstall" }));
		await waitFor(() => expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/agents/{agent}/install", {
			params: { path: { agent: "claude-code" } },
			body: { method: "homebrew", operation: "reinstall" },
		}));
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

	it("hydrates interrupted jobs and offers separate verify and reinstall actions", async () => {
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

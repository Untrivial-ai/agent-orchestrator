import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

type MockAgentCatalog = {
	authorized: { id: string; label: string; authStatus: string }[];
	installed: { id: string; label: string; authStatus: string }[];
	supported: { id: string; label: string }[];
};

type MockAgentsQuery = {
	data: MockAgentCatalog | undefined;
	isFetching: boolean;
	isLoading: boolean;
};

const routeMocks = vi.hoisted(() => ({
	navigate: vi.fn(),
	agentsQuery: {} as MockAgentsQuery,
	requestOnboardingFinish: vi.fn(),
	clearOnboardingFinishError: vi.fn(),
	onboardingFinishRequest: null as null | {
		path: string;
		orchestratorAgent: string;
		workerAgent: string;
		nonce: number;
	},
	onboardingFinishError: null as null | { message: string; nonce: number },
}));

const apiMocks = vi.hoisted(() => ({
	GET: vi.fn(),
	POST: vi.fn(),
	DELETE: vi.fn(),
	PATCH: vi.fn(),
}));

vi.mock("../lib/api-client", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../lib/api-client")>();
	return {
		...actual,
		apiClient: { ...actual.apiClient, DELETE: apiMocks.DELETE, GET: apiMocks.GET, PATCH: apiMocks.PATCH, POST: apiMocks.POST },
	};
});

vi.mock("@tanstack/react-router", async (importOriginal) => ({
	...(await importOriginal<typeof import("@tanstack/react-router")>()),
	createFileRoute: () => (options: unknown) => ({ options }),
	useNavigate: () => routeMocks.navigate,
}));

vi.mock("../stores/ui-store", () => ({
	useResolvedTheme: () => "dark" as const,
	useUiStore: (selector: (state: unknown) => unknown) =>
		selector({
			requestOnboardingFinish: routeMocks.requestOnboardingFinish,
			clearOnboardingFinishError: routeMocks.clearOnboardingFinishError,
			onboardingFinishRequest: routeMocks.onboardingFinishRequest,
			onboardingFinishError: routeMocks.onboardingFinishError,
		}),
}));

vi.mock("../hooks/useAgentsQuery", () => ({
	refreshAgentsIfStale: vi.fn().mockResolvedValue(undefined),
	useAgentsQuery: () => routeMocks.agentsQuery,
}));

vi.mock("../components/OnboardingProjectSetup", () => ({
	OnboardingProjectSetup: ({ onPrepared }: { onPrepared: (input: { path: string }) => void }) => (
		<button type="button" onClick={() => onPrepared({ path: "/tmp/acme/project" })}>
			Prepare project
		</button>
	),
}));

import { OnboardingPage } from "../components/OnboardingPage";

async function renderOnboarding() {
	await act(async () => {
		render(
			<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
				<OnboardingPage />
			</QueryClientProvider>,
		);
	});
}

async function goToOrchestratorStep(user: ReturnType<typeof userEvent.setup>) {
	await goToProjectStep(user);
	await user.click(await screen.findByRole("button", { name: "Prepare project" }));
	await screen.findByRole("heading", { name: "Pick your orchestrator agent." });
}

async function goToProjectStep(user: ReturnType<typeof userEvent.setup>) {
	await goToGitHubStep(user);
	await user.click(screen.getByRole("button", { name: "Continue" }));
	await screen.findByRole("heading", { name: "Run sessions in the cloud" });
	await user.click(screen.getByRole("button", { name: "Continue" }));
	await screen.findByRole("heading", { name: "Create your first project." });
}

/** The GitHub page sits between the agent sign-in step and the cloud step. */
async function goToGitHubStep(user: ReturnType<typeof userEvent.setup>) {
	await user.click(screen.getByRole("button", { name: "Continue" }));
	await user.click(await screen.findByRole("button", { name: "Proceed to setup" }));
	await screen.findByRole("heading", { name: "Connect GitHub" });
}

beforeEach(() => {
	routeMocks.navigate.mockReset();
	routeMocks.requestOnboardingFinish.mockReset();
	routeMocks.clearOnboardingFinishError.mockReset();
	routeMocks.onboardingFinishRequest = null;
	routeMocks.onboardingFinishError = null;
	apiMocks.GET.mockReset();
	apiMocks.POST.mockReset();
	apiMocks.DELETE.mockReset();
	apiMocks.PATCH.mockReset();
	apiMocks.PATCH.mockResolvedValue({ data: {} });
	apiMocks.GET.mockImplementation(async (path: string) => {
		if (path === "/api/v1/agents/installers") return { data: { agents: [] } };
		if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } };
		if (path === "/api/v1/agents/auth-plans") {
			return {
				data: {
					plans: [
						{ action: "login", agentId: "kiro", available: true, documentationUrl: "https://example.com/kiro", launchMode: "terminal" },
					],
				},
			};
		}
		if (path === "/api/v1/shell-terminals") return { data: { terminals: [] } };
		if (path === "/api/v1/system/requirements") {
			return {
				data: {
					ready: true,
					requirements: [
						// Connected by default so the flow can pass the GitHub step; the
						// tests for that step override this.
						{ detail: "/usr/local/bin/gh", id: "gh", label: "GitHub CLI", required: false, satisfied: true },
					],
				},
			};
		}
		if (path === "/api/v1/system/github-auth") {
			return { data: { id: "github-auth", label: "GitHub account", required: false, satisfied: true } };
		}
		if (path === "/api/v1/system/install/{target}") {
			return { data: { status: "idle", target: "gh" } };
		}
		return { data: undefined };
	});
	apiMocks.POST.mockImplementation(async (path: string) => {
		if (path === "/api/v1/agents/{agent}/install") {
			return { data: { status: "installing", target: "cursor" } };
		}
		if (path === "/api/v1/agents/{agent}/auth") {
			return {
				data: {
					action: "login",
					agentId: "kiro",
					guidance: "Complete the native sign-in flow in the terminal.",
					terminal: { createdAt: new Date().toISOString(), handleId: "auth-terminal-1", title: "Kiro sign-in", workingDir: "/tmp" },
					terminalInput: "",
				},
			};
		}
		if (path === "/api/v1/system/install/{target}") {
			return { data: { status: "installing", target: "gh" } };
		}
		if (path === "/api/v1/system/github-auth/terminal") {
			return {
				data: {
					shellTerminal: {
						createdAt: new Date().toISOString(),
						handleId: "github-auth-terminal-1",
						title: "Connect GitHub",
						workingDir: "/tmp",
					},
				},
			};
		}
		return { data: undefined };
	});
	routeMocks.agentsQuery = {
		data: {
			authorized: [
				{ id: "claude-code", label: "Claude Code", authStatus: "authorized" },
				{ id: "codex", label: "Codex", authStatus: "authorized" },
			],
			installed: [
				{ id: "claude-code", label: "Claude Code", authStatus: "authorized" },
				{ id: "codex", label: "Codex", authStatus: "authorized" },
				{ id: "kiro", label: "Kiro", authStatus: "unknown" },
			],
			supported: [
				{ id: "claude-code", label: "Claude Code" },
				{ id: "codex", label: "Codex" },
				{ id: "kiro", label: "Kiro" },
				{ id: "cursor", label: "Cursor" },
			],
		},
		isFetching: false,
		isLoading: false,
	};
});

describe("onboarding route", () => {
	it("does not leave the agent list in a permanent checking state", async () => {
		vi.useFakeTimers();
		try {
			routeMocks.agentsQuery = { data: undefined, isFetching: true, isLoading: true };
			await renderOnboarding();
			await act(async () => {
				fireEvent.click(screen.getByRole("button", { name: "Continue" }));
			});
			await act(async () => {
				fireEvent.click(screen.getByRole("button", { name: "Proceed to setup" }));
			});
			// Pass the GitHub and cloud steps to reach the project step.
			for (let index = 0; index < 2; index += 1) {
				await act(async () => {
					fireEvent.click(screen.getByRole("button", { name: "Continue" }));
				});
			}
			await act(async () => {
				fireEvent.click(screen.getByRole("button", { name: "Prepare project" }));
			});

			expect(screen.getAllByLabelText("Checking availability")).not.toHaveLength(0);
			await act(async () => {
				vi.advanceTimersByTime(2_500);
			});
			expect(screen.queryByLabelText("Checking availability")).not.toBeInTheDocument();
			expect(screen.getByRole("button", { name: "Claude Code" })).toBeEnabled();
		} finally {
			vi.useRealTimers();
		}
	});

	it("walks through the preview steps and requires separate agent role selections", async () => {
		const user = userEvent.setup();
		await renderOnboarding();

		expect(screen.getByRole("heading", { name: "Stop babysitting agents." })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Continue" }));
		expect(await screen.findByRole("heading", { name: "Keep the loop moving." })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Proceed to setup" }));
		// GitHub, then cloud, then the project step. Agent sign-in lives in the
		// orchestrator and worker steps instead of a page of its own.
		expect(await screen.findByRole("heading", { name: "Connect GitHub" })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Continue" }));
		expect(await screen.findByRole("heading", { name: "Run sessions in the cloud" })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Continue" }));
		expect(await screen.findByRole("heading", { name: "Create your first project." })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Prepare project" }));

		expect(await screen.findByRole("heading", { name: "Pick your orchestrator agent." })).toBeInTheDocument();

		const nextButton = screen.getByRole("button", { name: "Choose workers" });
		expect(nextButton).toBeDisabled();
		const agentPicker = screen.getByRole("region", { name: "Pick your orchestrator agent." });
		const orchestratorPicker = within(agentPicker).getByRole("region", { name: "Orchestrator agent" });
		expect(orchestratorPicker).toHaveTextContent("Claude Code");
		expect(within(orchestratorPicker).getByRole("button", { name: "Install Cursor" })).toBeEnabled();
		expect(within(orchestratorPicker).getByRole("button", { name: "Kiro" })).toBeEnabled();
		await user.click(within(orchestratorPicker).getByRole("button", { name: "Claude Code" }));
		expect(nextButton).toBeEnabled();
		await user.click(nextButton);

		expect(await screen.findByRole("heading", { name: "Pick your worker agents." })).toBeInTheDocument();
		const workerPicker = screen.getByRole("region", { name: "Worker agents" });
		const projectButton = screen.getByRole("button", { name: "See how it works" });
		expect(projectButton).toBeDisabled();
		expect(within(workerPicker).getByRole("button", { name: "Install Cursor" })).toBeEnabled();
		await user.click(within(workerPicker).getByRole("button", { name: "Codex" }));
		expect(projectButton).toBeEnabled();

		await user.click(projectButton);
		expect(await screen.findByRole("heading", { name: "Give your orchestrator a goal." })).toBeInTheDocument();
		expect(screen.getByText(/Break this feature into 3 parallel tasks/)).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Continue to orchestrator" })).toBeInTheDocument();
	});

	it("supports back navigation and opens the orchestrator from the final step", async () => {
		const user = userEvent.setup();
		await renderOnboarding();

		await user.click(screen.getByRole("button", { name: "Continue" }));
		await user.click(await screen.findByRole("button", { name: "Proceed to setup" }));
		await screen.findByRole("heading", { name: "Connect GitHub" });
		await user.click(screen.getByRole("button", { name: "Continue" }));
		await screen.findByRole("heading", { name: "Run sessions in the cloud" });
		await user.click(screen.getByRole("button", { name: "Continue" }));
		expect(await screen.findByRole("heading", { name: "Create your first project." })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Prepare project" }));
		expect(await screen.findByRole("heading", { name: "Pick your orchestrator agent." })).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Back" }));
		expect(await screen.findByRole("heading", { name: "Create your first project." })).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Choose orchestrator" }));
		const agentPicker = await screen.findByRole("region", { name: "Pick your orchestrator agent." });
		await user.click(within(within(agentPicker).getByRole("region", { name: "Orchestrator agent" })).getByRole("button", { name: "Codex" }));
		await user.click(screen.getByRole("button", { name: "Choose workers" }));
		const workerPicker = await screen.findByRole("region", { name: "Worker agents" });
		await user.click(within(workerPicker).getByRole("button", { name: "Claude Code" }));
		await user.click(screen.getByRole("button", { name: "See how it works" }));
		await user.click(screen.getByRole("button", { name: "Continue to orchestrator" }));
		await waitFor(() => {
			expect(routeMocks.requestOnboardingFinish).toHaveBeenCalledWith({
				path: "/tmp/acme/project",
				orchestratorAgent: "codex",
				workerAgent: "claude-code",
			});
			expect(routeMocks.navigate).toHaveBeenCalledWith({ to: "/" });
		});
		// Completion is recorded by the handoff, not by leaving the last step.
		expect(window.localStorage.getItem("ao.onboarding.completed")).toBeNull();
	});

	it("does not record completion until the handoff succeeds", async () => {
		const user = userEvent.setup();
		await renderOnboarding();
		await goToOrchestratorStep(user);
		const orchestrators = screen.getByRole("region", { name: "Orchestrator agent" });
		await user.click(within(orchestrators).getByRole("button", { name: "Claude Code" }));
		await user.click(screen.getByRole("button", { name: "Choose workers" }));
		const workers = await screen.findByRole("region", { name: "Worker agents" });
		await user.click(within(workers).getByRole("button", { name: "Codex" }));
		await user.click(screen.getByRole("button", { name: "See how it works" }));
		await screen.findByRole("heading", { name: "Give your orchestrator a goal." });

		// The old flow marked onboarding complete before creating anything, so a
		// failed project left the user on an empty board with the flow spent.
		await user.click(screen.getByRole("button", { name: "Continue to orchestrator" }));
		expect(routeMocks.requestOnboardingFinish).toHaveBeenCalled();
		expect(window.localStorage.getItem("ao.onboarding.completed")).toBeNull();
	});

	it("shows a failed handoff in place with a retry", async () => {
		routeMocks.onboardingFinishRequest = {
			nonce: 1,
			orchestratorAgent: "claude-code",
			path: "/tmp/acme/project",
			workerAgent: "codex",
		};
		routeMocks.onboardingFinishError = { message: "clone failed: repository not found", nonce: 1 };
		await renderOnboarding();

		expect(await screen.findByText("Setup could not finish")).toBeInTheDocument();
		expect(screen.getByText("clone failed: repository not found")).toBeInTheDocument();

		await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
		expect(routeMocks.clearOnboardingFinishError).toHaveBeenCalled();
		expect(routeMocks.navigate).toHaveBeenCalledWith({ to: "/" });
	});

	it("installs a missing harness in place instead of leaving onboarding", async () => {
		const user = userEvent.setup();
		await renderOnboarding();
		await goToOrchestratorStep(user);

		const picker = screen.getByRole("region", { name: "Orchestrator agent" });
		await user.click(within(picker).getByRole("button", { name: "Install Cursor" }));

		await waitFor(() => {
			expect(apiMocks.POST).toHaveBeenCalledWith(
				"/api/v1/agents/{agent}/install",
				expect.objectContaining({
					params: { path: { agent: "cursor" } },
					body: expect.objectContaining({ operation: "install" }),
				}),
			);
		});
		// Sending the user to Settings and the home route used to drop them out of
		// setup with no way back to the step they were on.
		expect(routeMocks.navigate).not.toHaveBeenCalled();
	});

	it("surfaces a failed install with the reason and a retry in place", async () => {
		apiMocks.GET.mockImplementation(async (path: string) => {
			if (path === "/api/v1/agents/installers") return { data: { agents: [] } };
			if (path === "/api/v1/agents/install-jobs") {
				return { data: { jobs: [{ error: "brew is not installed", status: "failed", target: "cursor" }] } };
			}
			if (path === "/api/v1/agents/auth-plans") return { data: { plans: [] } };
			if (path === "/api/v1/shell-terminals") return { data: { terminals: [] } };
			if (path === "/api/v1/system/requirements") {
				return { data: { ready: true, requirements: [{ detail: "/usr/local/bin/gh", id: "gh", label: "GitHub CLI", required: false, satisfied: true }] } };
			}
			if (path === "/api/v1/system/github-auth") {
				return { data: { id: "github-auth", label: "GitHub account", required: false, satisfied: true } };
			}
			return { data: undefined };
		});
		const user = userEvent.setup();
		await renderOnboarding();
		await goToOrchestratorStep(user);

		const picker = screen.getByRole("region", { name: "Orchestrator agent" });
		expect(await within(picker).findByText("brew is not installed")).toBeInTheDocument();
		expect(within(picker).getByRole("button", { name: "Try again to install Cursor" })).toBeEnabled();
	});

	it("offers an in-place sign-in for an installed harness that is not authenticated", async () => {
		const user = userEvent.setup();
		await renderOnboarding();
		await goToOrchestratorStep(user);

		const picker = screen.getByRole("region", { name: "Orchestrator agent" });
		await user.click(within(picker).getByRole("button", { name: "Sign in to Kiro" }));

		await waitFor(() => {
			expect(apiMocks.POST).toHaveBeenCalledWith(
				"/api/v1/agents/{agent}/auth",
				expect.objectContaining({ params: { path: { agent: "kiro" } } }),
			);
		});
		expect(await screen.findByTestId("harness-auth-terminal")).toBeInTheDocument();
		expect(routeMocks.navigate).not.toHaveBeenCalled();
	});

	it("sends a harness with no terminal login flow to its setup guide", async () => {
		apiMocks.GET.mockImplementation(async (path: string) => {
			if (path === "/api/v1/agents/auth-plans") {
				return {
					data: {
						plans: [
							{ action: "setup", agentId: "kiro", available: false, documentationUrl: "https://example.com/kiro-setup", launchMode: "documentation", reason: "No native login command" },
						],
					},
				};
			}
			if (path === "/api/v1/agents/installers") return { data: { agents: [] } };
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } };
			if (path === "/api/v1/shell-terminals") return { data: { terminals: [] } };
			if (path === "/api/v1/system/requirements") return { data: { ready: true, requirements: [] } };
			if (path === "/api/v1/system/github-auth") return { data: { id: "github-auth", label: "GitHub account", required: false, satisfied: true } };
			return { data: undefined };
		});
		const user = userEvent.setup();
		await renderOnboarding();
		await goToOrchestratorStep(user);

		const picker = screen.getByRole("region", { name: "Orchestrator agent" });
		expect(within(picker).getByRole("button", { name: "Open the setup guide for Kiro" })).toBeEnabled();
		expect(within(picker).queryByRole("button", { name: "Sign in to Kiro" })).not.toBeInTheDocument();
	});

	it("turns cloud on from the cloud step", async () => {
		const user = userEvent.setup();
		await renderOnboarding();
		await goToGitHubStep(user);
		await user.click(screen.getByRole("button", { name: "Continue" }));

		expect(await screen.findByRole("heading", { name: "Run sessions in the cloud" })).toBeInTheDocument();
		expect(screen.getByText("Optional. Everything works locally either way.")).toBeInTheDocument();
		expect(screen.getByText("Lets you run a project in a remote sandbox.")).toBeInTheDocument();

		// Choosing applies the setting in place; Continue is what moves the flow on.
		await user.click(screen.getByRole("button", { name: "Add cloud sessions" }));
		await waitFor(() => {
			expect(apiMocks.PATCH).toHaveBeenCalledWith("/api/v1/settings/cloud-offering", { body: { enabled: true } });
		});
		expect(screen.getByRole("heading", { name: "Run sessions in the cloud" })).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Continue" }));
		expect(await screen.findByRole("heading", { name: "Create your first project." })).toBeInTheDocument();
	});

	it("keeps the GitHub step from being skipped while GitHub is not connected", async () => {
		apiMocks.GET.mockImplementation(async (path: string) => {
			if (path === "/api/v1/system/requirements") {
				return {
					data: {
						ready: true,
						requirements: [{ detail: "/usr/local/bin/gh", id: "gh", label: "GitHub CLI", required: false, satisfied: true }],
					},
				};
			}
			if (path === "/api/v1/system/github-auth") {
				return { data: { id: "github-auth", label: "GitHub account", required: false, satisfied: false } };
			}
			if (path === "/api/v1/shell-terminals") return { data: { terminals: [] } };
			if (path === "/api/v1/agents/installers") return { data: { agents: [] } };
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } };
			if (path === "/api/v1/agents/auth-plans") return { data: { plans: [] } };
			return { data: undefined };
		});
		const user = userEvent.setup();
		await renderOnboarding();
		await goToGitHubStep(user);

		expect(await screen.findByRole("button", { name: "Sign in with GitHub" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
	});

	it("installs the GitHub CLI in place when it is missing", async () => {
		apiMocks.GET.mockImplementation(async (path: string) => {
			if (path === "/api/v1/system/requirements") {
				return { data: { ready: true, requirements: [{ detail: "gh was not found on PATH.", id: "gh", label: "GitHub CLI", required: false, satisfied: false }] } };
			}
			if (path === "/api/v1/system/github-auth") {
				return { data: { id: "github-auth", label: "GitHub account", required: false, satisfied: false } };
			}
			if (path === "/api/v1/shell-terminals") return { data: { terminals: [] } };
			if (path === "/api/v1/agents/installers") return { data: { agents: [] } };
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } };
			if (path === "/api/v1/agents/auth-plans") return { data: { plans: [] } };
			return { data: undefined };
		});
		const user = userEvent.setup();
		await renderOnboarding();
		await goToGitHubStep(user);

		expect(await screen.findByText("Needed for pull requests and issues.")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Install gh" }));

		await waitFor(() => {
			expect(apiMocks.POST).toHaveBeenCalledWith(
				"/api/v1/system/install/{target}",
				expect.objectContaining({ params: { path: { target: "gh" } } }),
			);
		});
		expect(routeMocks.navigate).not.toHaveBeenCalled();
	});

	it("signs in to GitHub in place once the CLI is present", async () => {
		apiMocks.GET.mockImplementation(async (path: string) => {
			if (path === "/api/v1/shell-terminals") return { data: { terminals: [] } };
			if (path === "/api/v1/system/requirements") {
				return {
					data: {
						ready: true,
						requirements: [
							{ detail: "/usr/local/bin/gh", id: "gh", label: "GitHub CLI", required: false, satisfied: true },
						],
					},
				};
			}
			if (path === "/api/v1/system/github-auth") {
				return { data: { id: "github-auth", label: "GitHub account", required: false, satisfied: false } };
			}
			if (path === "/api/v1/agents/installers") return { data: { agents: [] } };
			if (path === "/api/v1/agents/install-jobs") return { data: { jobs: [] } };
			if (path === "/api/v1/agents/auth-plans") return { data: { plans: [] } };
			return { data: undefined };
		});
		const user = userEvent.setup();
		await renderOnboarding();
		await goToGitHubStep(user);

		await user.click(await screen.findByRole("button", { name: "Sign in with GitHub" }));

		await waitFor(() => {
			expect(apiMocks.POST).toHaveBeenCalledWith("/api/v1/system/github-auth/terminal");
		});
		expect(await screen.findByTestId("github-auth-terminal")).toBeInTheDocument();
		expect(routeMocks.navigate).not.toHaveBeenCalled();
	});
});

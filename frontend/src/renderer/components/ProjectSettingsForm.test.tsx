import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { getMock, putMock, postMock, navigateMock, closeSettingsMock, setOrchestratorReplacementErrorMock, captureOrchestratorReplacementFailureMock, refreshAgentsIfStaleMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	putMock: vi.fn(),
	postMock: vi.fn(),
	navigateMock: vi.fn(),
	closeSettingsMock: vi.fn(),
	setOrchestratorReplacementErrorMock: vi.fn(),
	captureOrchestratorReplacementFailureMock: vi.fn(),
	refreshAgentsIfStaleMock: vi.fn(async () => undefined),
}));

vi.mock("../hooks/useAgentsQuery", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../hooks/useAgentsQuery")>();
	return { ...actual, refreshAgentsIfStale: refreshAgentsIfStaleMock };
});

vi.mock("@tanstack/react-router", async (importOriginal) => {
	const actual = await importOriginal<typeof import("@tanstack/react-router")>();
	return {
		...actual,
		useNavigate: () => navigateMock,
	};
});

vi.mock("../stores/ui-store", () => ({
	useUiStore: (selector: (state: Record<string, unknown>) => unknown) =>
		selector({
			closeSettings: closeSettingsMock,
			setOrchestratorReplacementError: setOrchestratorReplacementErrorMock,
		}),
}));

vi.mock("../lib/orchestrator-replacement-telemetry", () => ({
	captureOrchestratorReplacementFailure: captureOrchestratorReplacementFailureMock,
}));

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: getMock,
		PUT: putMock,
		POST: postMock,
	},
	apiErrorCode: (error: unknown) =>
		typeof error === "object" && error !== null && "code" in error
			? String((error as { code: unknown }).code)
			: undefined,
	apiErrorRequestId: (error: unknown) =>
		typeof error === "object" && error !== null && "requestId" in error
			? String((error as { requestId: unknown }).requestId)
			: undefined,
	apiErrorMessage: (error: unknown) => {
		if (error instanceof Error) return error.message;
		if (typeof error === "object" && error !== null && "message" in error) {
			return String((error as { message: unknown }).message);
		}
		return "Request failed";
	},
}));

import { ProjectSettingsForm, type ProjectSettingsSaveState, type ProjectSettingsSection } from "./ProjectSettingsForm";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import type { WorkspaceSummary } from "../types/workspace";

async function beginEdit(label: string) {
	await userEvent.click(await screen.findByRole("button", { name: `Edit ${label}` }));
	return screen.getByLabelText(label);
}

function TestProjectSettings({
	projectId,
	section,
}: {
	projectId: string;
	section?: ProjectSettingsSection;
}) {
	const [saveState, setSaveState] = useState<ProjectSettingsSaveState>({
		isPending: false,
		showSaving: false,
		validationError: null,
		mutationError: null,
		saved: false,
		replacementError: null,
	});
	return (
		<>
			<ProjectSettingsForm projectId={projectId} section={section} onSaveState={setSaveState} />
			{saveState.validationError && <span>{saveState.validationError}</span>}
			{saveState.mutationError && <span>{saveState.mutationError}</span>}
			{saveState.saved && <span>{"Saved"}</span>}
			{saveState.replacementError && <span>{`Orchestrator restart failed: ${saveState.replacementError}`}</span>}
		</>
	);
}

function renderSettings(projectId = "proj-1", workspaces?: WorkspaceSummary[], section?: ProjectSettingsSection) {
	const queryClient = new QueryClient({
		defaultOptions: {
			queries: { retry: false },
			mutations: { retry: false },
		},
	});
	if (workspaces) {
		queryClient.setQueryData(workspaceQueryKey, workspaces);
	}
	render(
		<QueryClientProvider client={queryClient}>
			<TestProjectSettings projectId={projectId} section={section} />
		</QueryClientProvider>,
	);
	return queryClient;
}

async function chooseOption(trigger: HTMLElement, optionName: string) {
	await userEvent.click(trigger);
	const escaped = optionName.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
	await userEvent.click(await screen.findByRole("menuitem", { name: new RegExp(`^${escaped}$`, "i") }));
}

function submitSettings() {
	fireEvent.submit(document.getElementById("project-settings-form")!);
}

async function expectReplacementNavigation(sessionId = "proj-1-orch-2") {
	await waitFor(() =>
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "proj-1", sessionId },
		}),
	);
	expect(closeSettingsMock).toHaveBeenCalledTimes(1);
}

const agentCatalogResponse = {
	data: {
		supported: [
			{ id: "claude-code", label: "Claude Code" },
			{ id: "codex", label: "Codex" },
			{ id: "copilot", label: "GitHub Copilot" },
			{ id: "cursor", label: "Cursor" },
			{ id: "goose", label: "Goose" },
			{ id: "kilocode", label: "Kilo Code" },
			{ id: "kiro", label: "Kiro" },
			{ id: "opencode", label: "OpenCode" },
			{ id: "pi", label: "Pi" },
		],
		installed: [
			{ id: "claude-code", label: "Claude Code", authStatus: "authorized" },
			{ id: "codex", label: "Codex", authStatus: "authorized" },
			{ id: "copilot", label: "GitHub Copilot", authStatus: "authorized" },
			{ id: "cursor", label: "Cursor", authStatus: "authorized" },
			{ id: "goose", label: "Goose", authStatus: "authorized" },
			{ id: "kilocode", label: "Kilo Code", authStatus: "authorized" },
			{ id: "kiro", label: "Kiro", authStatus: "unknown" },
			{ id: "opencode", label: "OpenCode", authStatus: "authorized" },
			{ id: "pi", label: "Pi", authStatus: "authorized" },
		],
		authorized: [
			{ id: "claude-code", label: "Claude Code", authStatus: "authorized" },
			{ id: "codex", label: "Codex", authStatus: "authorized" },
			{ id: "copilot", label: "GitHub Copilot", authStatus: "authorized" },
			{ id: "cursor", label: "Cursor", authStatus: "authorized" },
			{ id: "goose", label: "Goose", authStatus: "authorized" },
			{ id: "kilocode", label: "Kilo Code", authStatus: "authorized" },
			{ id: "opencode", label: "OpenCode", authStatus: "authorized" },
			{ id: "pi", label: "Pi", authStatus: "authorized" },
		],
	},
	error: undefined,
};

function mockProject(project: Record<string, unknown>) {
	getMock.mockImplementation(async (path: string) => {
		if (path === "/api/v1/agents") return agentCatalogResponse;
		if (path === "/api/v1/agents/{agent}/models") {
			return {
				data: {
					agentId: "test-agent",
					selectionMode: "text",
					models: [],
					allowCustom: true,
					source: "manual",
					fetchedAt: "2026-07-31T00:00:00Z",
					stale: false,
				},
				error: undefined,
			};
		}
		return {
			data: {
				status: "ok",
				project,
			},
			error: undefined,
		};
	});
}

beforeEach(() => {
	getMock.mockReset();
	putMock.mockReset();
	postMock.mockReset();
	navigateMock.mockReset();
	closeSettingsMock.mockReset();
	setOrchestratorReplacementErrorMock.mockReset();
	captureOrchestratorReplacementFailureMock.mockReset();
	refreshAgentsIfStaleMock.mockReset().mockResolvedValue(undefined);
	putMock.mockResolvedValue({ data: { project: {} }, error: undefined });
	postMock.mockResolvedValue({
		data: { orchestrator: { id: "proj-1-orch-2" } },
		error: undefined,
		response: { status: 200 },
	});
});

describe("ProjectSettingsForm", () => {
	it("refreshes agent inventory in the background without catalog refresh buttons", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings("proj-1", undefined, "agents");

		await waitFor(() => expect(refreshAgentsIfStaleMock).toHaveBeenCalledOnce());
		expect(screen.queryByRole("button", { name: "Refresh agents" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Refresh worker model list" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Refresh orchestrator model list" })).not.toBeInTheDocument();
	});

	it("does not have its own close button (dialog handles closing)", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings();
		await screen.findByRole("button", { name: "Edit Project name" });

		// Close button is now in SettingsDialog, not in the form itself
		expect(screen.queryByRole("button", { name: "Close settings" })).not.toBeInTheDocument();
		expect(navigateMock).not.toHaveBeenCalled();
	});

	it("shows the project name as text with a pencil until edit is clicked", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings();

		expect(await screen.findByRole("button", { name: "Edit Project name" })).toHaveTextContent("Project One");
		expect(screen.queryByRole("textbox", { name: "Project name" })).not.toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Edit Project name" }));
		expect(screen.getByRole("textbox", { name: "Project name" })).toHaveValue("Project One");
		expect(screen.queryByRole("button", { name: "Edit Project name" })).not.toBeInTheDocument();

		await userEvent.keyboard("{Escape}");
		expect(await screen.findByRole("button", { name: "Edit Project name" })).toBeInTheDocument();
		expect(screen.queryByRole("textbox", { name: "Project name" })).not.toBeInTheDocument();
	});

	it("does not navigate on Escape (dialog handles closing)", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings();
		await screen.findByRole("button", { name: "Edit Project name" });

		await userEvent.keyboard("{Escape}");

		// Escape is handled by the Radix Dialog in SettingsDialog, not the form
		expect(navigateMock).not.toHaveBeenCalled();
	});

	it("atomically saves the project display name and config without changing its stable ID", async () => {
		mockProject({
			id: "tg_content_factory_5863f66be3",
			name: "tg_content_factory_5863f66be3",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings("tg_content_factory_5863f66be3");

		const projectName = await beginEdit("Project name");
		await userEvent.clear(projectName);
		await userEvent.type(projectName, "TG Content Factory");
		submitSettings();

		await waitFor(() => expect(putMock).toHaveBeenCalledTimes(1));
		expect(putMock).toHaveBeenCalledWith("/api/v1/projects/{id}", {
			params: { path: { id: "tg_content_factory_5863f66be3" } },
			body: expect.objectContaining({ displayName: "TG Content Factory" }),
		});
		expect(screen.getByText("tg_content_factory_5863f66be3")).toBeInTheDocument();
	});

	it("renders git scp-style remotes as clickable https links", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "git@github.com:acme/project-one.git",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings();

		const repoLink = await screen.findByRole("link", { name: "git@github.com:acme/project-one.git" });
		expect(repoLink).toHaveAttribute("href", "https://github.com/acme/project-one");
	});

	it("renders ssh remotes as clickable https links", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "ssh://git@github.com/acme/project-one.git",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings();

		const repoLink = await screen.findByRole("link", { name: "ssh://git@github.com/acme/project-one.git" });
		expect(repoLink).toHaveAttribute("href", "https://github.com/acme/project-one");
	});

	it("loads agents fields and saves without dropping hidden workflow config", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "git@github.com:acme/project-one.git",
			defaultBranch: "main",
			config: {
				defaultBranch: "develop",
				sessionPrefix: "po",
				env: { FOO: "bar" },
				symlinks: [".env"],
				postCreate: ["npm install"],
				worker: {
					agent: "codex",
					agentConfig: { model: "worker-model" },
				},
				orchestrator: { agent: "claude-code" },
				agentConfig: {
					model: "claude-opus-4-5",
					permissions: "auto",
				},
				reviewers: [{ harness: "claude-code" }],
			},
		});

		renderSettings("proj-1", undefined, "agents");

		expect(screen.queryByLabelText("Default branch")).not.toBeInTheDocument();
		expect(await screen.findByLabelText("Worker model")).toHaveValue("worker-model");
		expect(screen.getByLabelText("Orchestrator model")).toHaveValue("claude-opus-4-5");

		const workerAgent = screen.getByRole("button", { name: "Default worker agent" });
		const orchestratorAgent = screen.getByRole("button", { name: "Default orchestrator agent" });
		const permissionMode = screen.getByRole("button", { name: "Permission mode" });
		expect(workerAgent).toHaveTextContent("codex");
		expect(orchestratorAgent).toHaveTextContent("claude-code");
		expect(permissionMode).toHaveTextContent("Auto");

		await chooseOption(workerAgent, "OpenCode");
		await chooseOption(orchestratorAgent, "Goose");
		await userEvent.type(screen.getByLabelText("Worker model"), "openai/gpt-5.4");
		await userEvent.type(screen.getByLabelText("Orchestrator model"), "anthropic/claude-sonnet");
		await userEvent.click(permissionMode);
		await userEvent.click(await screen.findByRole("menuitem", { name: "Bypass permissions" }));

		submitSettings();

		await waitFor(() => expect(putMock).toHaveBeenCalledTimes(1));
		expect(putMock).toHaveBeenCalledWith("/api/v1/projects/{id}", {
			params: { path: { id: "proj-1" } },
			body: {
				displayName: "Project One",
				config: expect.objectContaining({
					// Hidden workflow config is preserved
					defaultBranch: "develop",
					sessionPrefix: "po",
					env: { FOO: "bar" },
					reviewers: [{ harness: "claude-code" }],
					// Agents changes applied
					worker: {
						agent: "opencode",
						agentConfig: { model: "openai/gpt-5.4" },
					},
					orchestrator: {
						agent: "goose",
						agentConfig: { model: "anthropic/claude-sonnet" },
					},
					agentConfig: {
						permissions: "bypass-permissions",
					},
				}),
			},
		});
		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		expect(await screen.findByText("Saved")).toBeInTheDocument();
	}, 20_000);

	it("loads workflow fields correctly", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "git@github.com:acme/project-one.git",
			defaultBranch: "main",
			config: {
				defaultBranch: "develop",
				sessionPrefix: "po",
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
				reviewers: [{ harness: "claude-code" }],
			},
		});

		renderSettings("proj-1", undefined, "workflow");

		expect(await beginEdit("Default branch")).toHaveValue("develop");
		await userEvent.keyboard("{Escape}");
		expect(await beginEdit("Session prefix")).toHaveValue("po");
	});

	it("loads and saves the project auto review setting", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "git@github.com:acme/project-one.git",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
				autoReview: true,
			},
		});

		renderSettings("proj-1", undefined, "agents");

		const toggle = await screen.findByRole("switch", { name: "Auto review PRs" });
		expect(toggle).toBeChecked();

		await userEvent.click(toggle);
		expect(toggle).not.toBeChecked();

		submitSettings();

		await waitFor(() => expect(putMock).toHaveBeenCalledTimes(1));
		const request = putMock.mock.calls[0]?.[1];
		expect(request?.body.config.autoReview).toBe(false);
	});

	it("keeps the automatic default branch unpinned when saving other settings", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "git@github.com:acme/project-one.git",
			defaultBranch: "trunk",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings("proj-1", undefined, "workflow");

		expect(await beginEdit("Default branch")).toHaveValue("auto");
		await userEvent.keyboard("{Escape}");
		submitSettings();

		await waitFor(() => expect(putMock).toHaveBeenCalledTimes(1));
		const request = putMock.mock.calls[0]?.[1];
		expect(request?.body.config.defaultBranch).toBeUndefined();
	});

	it("shows the full model catalog again after selecting a model", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/agents") return agentCatalogResponse;
			if (path === "/api/v1/agents/{agent}/models") {
				return {
					data: {
						agentId: "codex",
						selectionMode: "catalog",
						models: [
							{ id: "gpt-5.6-sol", label: "GPT-5.6 Sol", isDefault: true },
							{ id: "gpt-5.5", label: "GPT-5.5" },
							{ id: "gpt-5.4", label: "GPT-5.4" },
						],
						allowCustom: true,
						source: "official-catalog",
						fetchedAt: "2026-07-31T00:00:00Z",
						stale: false,
					},
					error: undefined,
				};
			}
			return {
				data: {
					status: "ok",
					project: {
						id: "proj-1",
						name: "Project One",
						kind: "single_repo",
						path: "/repo/project-one",
						repo: "",
						defaultBranch: "main",
						config: {
							worker: { agent: "codex" },
							orchestrator: { agent: "codex" },
						},
					},
				},
				error: undefined,
			};
		});

		renderSettings("proj-1", undefined, "agents");

		const workerModel = await screen.findByRole("button", { name: "Worker model" });
		await userEvent.click(workerModel);
		expect((await screen.findAllByRole("menuitem")).map((item) => item.textContent)).toEqual([
			"Agent default",
			"GPT-5.6 SolDefault",
			"GPT-5.5",
			"GPT-5.4",
			"Custom model…",
		]);
		// A compact catalog stays immediately scannable and does not spend a row on search.
		expect(screen.queryByRole("searchbox", { name: "Search worker model" })).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole("menuitem", { name: /GPT-5\.4/ }));
		expect(workerModel).toHaveTextContent("GPT-5.4");

		await userEvent.click(workerModel);
		expect(await screen.findByRole("menuitem", { name: /GPT-5\.6 Sol/ })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: /GPT-5\.5/ })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: /GPT-5\.4/ })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Custom model…" })).toBeInTheDocument();
	});

	it("shows a warning when background model revalidation fails", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/agents") return agentCatalogResponse;
			if (path === "/api/v1/agents/{agent}/models") {
				return {
					data: {
						agentId: "codex",
						selectionMode: "catalog",
						models: [{ id: "gpt-5.6-sol", label: "GPT-5.6 Sol" }],
						allowCustom: true,
						source: "official-catalog",
						fetchedAt: "2026-07-31T00:00:00Z",
						validatedAt: "2026-07-31T00:00:00Z",
						refreshRecommended: true,
						stale: false,
					},
					error: undefined,
				};
			}
			return {
				data: {
					status: "ok",
					project: {
						id: "proj-1",
						name: "Project One",
						kind: "single_repo",
						path: "/repo/project-one",
						repo: "",
						defaultBranch: "main",
						config: {
							worker: { agent: "codex" },
							orchestrator: { agent: "codex" },
						},
					},
				},
				error: undefined,
			};
		});
		postMock.mockResolvedValue({ data: undefined, error: { message: "model refresh unavailable" } });

		renderSettings("proj-1", undefined, "agents");

		expect(await screen.findAllByText("model refresh unavailable")).toHaveLength(2);
		expect(screen.getByRole("button", { name: "Worker model" })).toHaveTextContent("Agent default");
	});

	it("shows cached models immediately and deduplicates background revalidation", async () => {
		const cachedCatalog = {
			agentId: "codex",
			selectionMode: "catalog" as const,
			models: [{ id: "gpt-5.6-sol", label: "GPT-5.6 Sol" }],
			allowCustom: true,
			source: "official-catalog",
			fetchedAt: "2026-07-31T00:00:00Z",
			validatedAt: "2026-07-31T00:00:00Z",
			refreshRecommended: true,
			stale: false,
		};
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/agents") return agentCatalogResponse;
			if (path === "/api/v1/agents/{agent}/models") return { data: cachedCatalog, error: undefined };
			return {
				data: {
					status: "ok",
					project: {
						id: "proj-1",
						name: "Project One",
						kind: "single_repo",
						path: "/repo/project-one",
						repo: "",
						defaultBranch: "main",
						config: {
							worker: { agent: "codex" },
							orchestrator: { agent: "codex" },
						},
					},
				},
				error: undefined,
			};
		});
		postMock.mockResolvedValue({
			data: { ...cachedCatalog, refreshRecommended: false, validatedAt: "2026-08-03T00:00:00Z" },
			error: undefined,
		});

		renderSettings("proj-1", undefined, "agents");

		expect(await screen.findByRole("button", { name: "Worker model" })).toHaveTextContent("Agent default");
		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		expect(postMock).toHaveBeenCalledWith("/api/v1/agents/{agent}/models/refresh", {
			params: {
				path: { agent: "codex" },
				query: { projectId: "proj-1", revalidate: true },
			},
		});
	});

	it("shows the daemon validation message when the atomic settings save fails", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});
		putMock.mockResolvedValue({
			data: undefined,
			error: { message: "invalid permissions" },
		});

		renderSettings();

		const projectName = await beginEdit("Project name");
		await userEvent.clear(projectName);
		await userEvent.type(projectName, "Updated Project");
		submitSettings();

		expect(await screen.findByText("invalid permissions")).toBeInTheDocument();
		expect(screen.queryByText("Saved")).not.toBeInTheDocument();
		expect(postMock).not.toHaveBeenCalled();
	});

	it("rejects a blank project name before sending the settings update", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings();

		const projectName = await beginEdit("Project name");
		await userEvent.clear(projectName);
		await userEvent.type(projectName, "   ");
		submitSettings();

		expect(await screen.findByText("Project name is required.")).toBeInTheDocument();
		expect(putMock).not.toHaveBeenCalled();
	});

	it("requires worker and orchestrator agents for existing projects missing role config", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {},
		});

		renderSettings("proj-1", undefined, "agents");

		expect(await screen.findByText("Worker and orchestrator agents are required.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Default worker agent" })).toHaveTextContent("Select worker agent");
		expect(screen.getByRole("button", { name: "Default orchestrator agent" })).toHaveTextContent(
			"Select orchestrator agent",
		);

		submitSettings();

		expect(await screen.findAllByText("Worker and orchestrator agents are required.")).toHaveLength(2);
		expect(putMock).not.toHaveBeenCalled();
	});

	it("uses the localized default label for the project reviewer picker", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings("proj-1", undefined, "agents");

		const reviewerAgent = await screen.findByRole("button", { name: "Default reviewer agent" });
		expect(reviewerAgent).toHaveTextContent("Project default");

		await userEvent.click(reviewerAgent);
		expect(await screen.findByRole("menuitem", { name: "Project default" })).toBeInTheDocument();
	});

	it("disables agent selectors while the initial agent catalog is loading", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/agents") {
				return new Promise(() => {});
			}
			return {
				data: {
					status: "ok",
					project: {
						id: "proj-1",
						name: "Project One",
						kind: "single_repo",
						path: "/repo/project-one",
						repo: "",
						defaultBranch: "main",
						config: {
							worker: { agent: "codex" },
							orchestrator: { agent: "claude-code" },
						},
					},
				},
				error: undefined,
			};
		});

		renderSettings("proj-1", undefined, "agents");

		expect(await screen.findByRole("button", { name: "Default worker agent" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Default orchestrator agent" })).toBeDisabled();
	});

	it("offers both interactive Kiro and Pi reviewers", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings("proj-1", undefined, "agents");
		const reviewer = await screen.findByRole("button", { name: "Default reviewer agent" });
		await userEvent.click(reviewer);
		const labels = (await screen.findAllByRole("menuitem")).map((option) => option.textContent);
		expect(labels).toContain("KiroAuth unknown");
		expect(labels).toContain("Pi");
	});

	it("offers Muse Code as a reviewer", async () => {
		const muse = { id: "muse", label: "Muse Code", authStatus: "authorized" };
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {
				worker: { agent: "muse" },
				orchestrator: { agent: "claude-code" },
			},
		});
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/agents") {
				return {
					data: {
						supported: [...agentCatalogResponse.data.supported, muse],
						installed: [...agentCatalogResponse.data.installed, muse],
						authorized: [...agentCatalogResponse.data.authorized, muse],
					},
					error: undefined,
				};
			}
			return {
				data: {
					status: "ok",
					project: {
						id: "proj-1",
						name: "Project One",
						kind: "single_repo",
						path: "/repo/project-one",
						repo: "",
						defaultBranch: "main",
						config: {
							worker: { agent: "muse" },
							orchestrator: { agent: "claude-code" },
						},
					},
				},
				error: undefined,
			};
		});

		renderSettings("proj-1", undefined, "agents");

		const reviewer = await screen.findByRole("button", { name: "Default reviewer agent" });
		await userEvent.click(reviewer);

		expect(await screen.findByRole("menuitem", { name: /Muse Code/ })).toBeInTheDocument();
	});

	it("orders reviewers using the default agent priority", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings("proj-1", undefined, "agents");

		await userEvent.click(await screen.findByRole("button", { name: "Default reviewer agent" }));
		const reviewerLabels = (await screen.findAllByRole("menuitem"))
			.map((option) => option.textContent)
			.filter((label) => label !== "Project default");

		expect(reviewerLabels).toEqual([
			"Claude Code",
			"Codex",
			"Cursor",
			"OpenCode",
			"GitHub Copilot",
			"Goose",
			"Kilo Code",
			"Pi",
			"KiroAuth unknown",
		]);
	});

	it("offers the experimental host-trusted reviewer set", async () => {
		const project = {
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: { worker: { agent: "qwen" }, orchestrator: { agent: "claude-code" } },
		};
		const qwen = { id: "qwen", label: "Qwen Code", authStatus: "authorized" };
		const devin = { id: "devin", label: "Devin", authStatus: "authorized" };
		const droid = { id: "droid", label: "Droid", authStatus: "authorized" };
		const kimi = { id: "kimi", label: "Kimi", authStatus: "authorized" };
		const aider = { id: "aider", label: "Aider", authStatus: "authorized" };
		const amp = { id: "amp", label: "Amp", authStatus: "authorized" };
		const experimental = [
			{ id: "agy", label: "Agy", authStatus: "authorized" },
			{ id: "auggie", label: "Auggie", authStatus: "authorized" },
			{ id: "autohand", label: "Autohand", authStatus: "authorized" },
			{ id: "cline", label: "Cline", authStatus: "authorized" },
			{ id: "continue", label: "Continue", authStatus: "authorized" },
			{ id: "crush", label: "Crush", authStatus: "authorized" },
			{ id: "grok", label: "Grok", authStatus: "authorized" },
			{ id: "vibe", label: "Vibe", authStatus: "authorized" },
		];
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/agents") {
				return {
					data: {
						supported: [...agentCatalogResponse.data.supported, qwen, devin, droid, kimi, aider, amp, ...experimental],
						installed: [...agentCatalogResponse.data.installed, qwen, devin, droid, kimi, aider, amp, ...experimental],
						authorized: [...agentCatalogResponse.data.authorized, qwen, devin, droid, kimi, aider, amp, ...experimental],
					},
					error: undefined,
				};
			}
			return { data: { status: "ok", project }, error: undefined };
		});

		renderSettings("proj-1", undefined, "agents");

		const reviewer = await screen.findByRole("button", { name: "Default reviewer agent" });
		await userEvent.click(reviewer);
		const options = await screen.findAllByRole("menuitem");
		const labels = options.map((option) => option.textContent);
		expect(labels).toContain("Qwen Code");
		expect(labels).toContain("Agy");
		expect(labels).toContain("Continue");
		expect(labels).toContain("Goose");
		expect(labels).toContain("Vibe");
		expect(labels).toContain("Devin");
		expect(labels).toContain("Droid");
		expect(labels).toContain("Kimi");
		expect(labels).toContain("Aider");
		expect(labels).toContain("Amp");
		expect(labels).toContain("Auggie");
		expect(labels).toContain("Autohand");
		expect(labels).toContain("Cline");
		expect(labels).toContain("Crush");
		expect(labels).toContain("Grok");
	});

	it("warns when an experimental reviewer is selected", async () => {
		const kimchi = { id: "kimchi", label: "Kimchi", authStatus: "authorized" };
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/agents") {
				return {
					data: {
						supported: [...agentCatalogResponse.data.supported, kimchi],
						installed: [...agentCatalogResponse.data.installed, kimchi],
						authorized: [...agentCatalogResponse.data.authorized, kimchi],
					},
					error: undefined,
				};
			}
			return {
				data: {
					status: "ok",
					project: {
						id: "proj-1",
						name: "Project One",
						kind: "single_repo",
						path: "/repo/project-one",
						repo: "",
						defaultBranch: "main",
						config: { worker: { agent: "codex" }, orchestrator: { agent: "claude-code" } },
					},
				},
				error: undefined,
			};
		});

		renderSettings("proj-1", undefined, "agents");
		await chooseOption(await screen.findByRole("button", { name: "Default reviewer agent" }), "Kimchi");
		expect(screen.getByRole("status")).toHaveTextContent("Experimental host-trusted reviewer");
	});

	it("shows unknown-auth agents as selectable with a warning in project settings", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings("proj-1", undefined, "agents");

		const workerAgent = await screen.findByRole("button", { name: "Default worker agent" });
		await userEvent.click(workerAgent);
		const options = await screen.findAllByRole("menuitem");
		expect(options.map((option) => option.textContent)).toEqual([
			"Claude Code",
			"Codex",
			"Cursor",
			"OpenCode",
			"GitHub Copilot",
			"Goose",
			"Kilo Code",
			"Pi",
			"KiroAuth unknown",
		]);
		expect(options[8]).not.toHaveAttribute("aria-disabled", "true");
	});

	it("shows Copilot as a reviewer option and saves it in the reviewers payload", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings("proj-1", undefined, "agents");

		const reviewer = await screen.findByRole("button", { name: "Default reviewer agent" });
		await userEvent.click(reviewer);
		const copilot = await screen.findByRole("menuitem", { name: "GitHub Copilot" });
		expect(copilot).not.toHaveAttribute("aria-disabled", "true");
		await userEvent.click(copilot);
		submitSettings();

		await waitFor(() => expect(putMock).toHaveBeenCalledTimes(1));
		expect(putMock).toHaveBeenCalledWith(
			"/api/v1/projects/{id}",
			expect.objectContaining({
				body: expect.objectContaining({
					config: expect.objectContaining({ reviewers: [{ harness: "copilot" }] }),
				}),
			}),
		);
	});

	it("disables the Copilot reviewer when its binary is missing", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/agents") {
				return {
					data: {
						supported: [{ id: "copilot", label: "GitHub Copilot" }],
						installed: [],
						authorized: [],
					},
					error: undefined,
				};
			}
			return {
				data: {
					status: "ok",
					project: {
						id: "proj-1",
						name: "Project One",
						kind: "single_repo",
						path: "/repo/project-one",
						repo: "",
						defaultBranch: "main",
						config: {
							worker: { agent: "codex" },
							orchestrator: { agent: "claude-code" },
						},
					},
				},
				error: undefined,
			};
		});

		renderSettings("proj-1", undefined, "agents");

		await userEvent.click(await screen.findByRole("button", { name: "Default reviewer agent" }));
		const copilot = (await screen.findAllByRole("menuitem")).find((option) =>
			option.textContent?.includes("GitHub Copilot"),
		);
		expect(copilot).toHaveTextContent("Needs install");
		expect(copilot).toHaveAttribute("aria-disabled", "true");
	});

	it("shows the standard unknown-auth warning for an installed Copilot reviewer", async () => {
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/agents") {
				return {
					data: {
						supported: [{ id: "copilot", label: "GitHub Copilot" }],
						installed: [{ id: "copilot", label: "GitHub Copilot", authStatus: "unknown" }],
						authorized: [],
					},
					error: undefined,
				};
			}
			return {
				data: {
					status: "ok",
					project: {
						id: "proj-1",
						name: "Project One",
						kind: "single_repo",
						path: "/repo/project-one",
						repo: "",
						defaultBranch: "main",
						config: {
							worker: { agent: "codex" },
							orchestrator: { agent: "claude-code" },
						},
					},
				},
				error: undefined,
			};
		});

		renderSettings("proj-1", undefined, "agents");

		await userEvent.click(await screen.findByRole("button", { name: "Default reviewer agent" }));
		const copilot = (await screen.findAllByRole("menuitem")).find((option) =>
			option.textContent?.includes("GitHub Copilot"),
		);
		expect(copilot).toHaveTextContent("Auth unknown");
		expect(copilot).not.toHaveAttribute("aria-disabled", "true");
	});

	it("offers Kilo Code as a configured reviewer", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings("proj-1", undefined, "agents");

		const reviewer = await screen.findByRole("button", { name: "Default reviewer agent" });
		await userEvent.click(reviewer);
		expect(await screen.findByRole("menuitem", { name: "Kilo Code" })).toBeEnabled();
	});

	it("offers the experimental Agy reviewer", async () => {
		const project = {
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: { worker: { agent: "agy" }, orchestrator: { agent: "claude-code" } },
		};
		const agy = { id: "agy", label: "Agy", authStatus: "authorized" };
		getMock.mockImplementation(async (path: string) => {
			if (path === "/api/v1/agents") {
				return {
					data: {
						supported: [...agentCatalogResponse.data.supported, agy],
						installed: [...agentCatalogResponse.data.installed, agy],
						authorized: [...agentCatalogResponse.data.authorized, agy],
					},
					error: undefined,
				};
			}
			return { data: { status: "ok", project }, error: undefined };
		});

		renderSettings("proj-1", undefined, "agents");

		const reviewerAgent = await screen.findByRole("button", { name: "Default reviewer agent" });
		await userEvent.click(reviewerAgent);
		const options = await screen.findAllByRole("menuitem");
		expect(options.map((option) => option.textContent)).toContain("Agy");
	});

	it("shows scratch identity and saves only scratch-supported settings", async () => {
		mockProject({
			id: "scratch",
			name: "Scratch",
			kind: "scratch",
			path: "/home/me/.ao/scratch/default",
			repo: "",
			defaultBranch: "",
			config: {
				defaultBranch: "main",
				sessionPrefix: "ao",
				env: { FOO: "bar" },
				symlinks: [".env"],
				postCreate: ["npm install"],
				agentRules: "keep work small",
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
				agentConfig: {
					model: "gpt-5-codex",
					permissions: "auto",
				},
				reviewers: [{ harness: "codex" }],
				autoReview: { enabled: true },
				trackerIntake: { enabled: true, provider: "github", assignee: "octocat" },
			},
		});

		renderSettings("scratch");

		const kindRow = (await screen.findByText("Type")).closest(".settings-row-bar");
		expect(kindRow).toHaveTextContent("Scratch project");
		expect(screen.queryByLabelText("Default branch")).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Session prefix")).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Auto-review pull requests")).not.toBeInTheDocument();
		expect(screen.queryByText("Reviewers")).not.toBeInTheDocument();
		expect(screen.queryByText("Tracker intake")).not.toBeInTheDocument();

		submitSettings();

		await waitFor(() => expect(putMock).toHaveBeenCalledTimes(1));
		expect(putMock).toHaveBeenCalledWith("/api/v1/projects/{id}", {
			params: { path: { id: "scratch" } },
			body: {
				displayName: "Scratch",
				config: {
					env: { FOO: "bar" },
					sessionPrefix: "ao",
					symlinks: [".env"],
					postCreate: ["npm install"],
					agentRules: "keep work small",
					worker: { agent: "codex", agentConfig: { model: "gpt-5-codex" } },
					orchestrator: { agent: "claude-code", agentConfig: { model: "gpt-5-codex" } },
					agentConfig: {
						permissions: "auto",
					},
				},
			},
		});
		expect(postMock).not.toHaveBeenCalled();
	});

	it("saves GitHub tracker intake settings, deriving the repo from the project's git origin", async () => {
		getMock.mockResolvedValue({
			data: {
				status: "ok",
				project: {
					id: "proj-1",
					name: "Project One",
					kind: "single_repo",
					path: "/repo/project-one",
					repo: "git@github.com:acme/project-one.git",
					defaultBranch: "main",
					config: {
						worker: { agent: "codex" },
						orchestrator: { agent: "claude-code" },
					},
				},
			},
			error: undefined,
		});

		renderSettings("proj-1", undefined, "intake");

		await userEvent.click(await screen.findByLabelText("Enable issue intake"));

		// Repository is display-only, derived from the project's own git origin — no input to
		// fill. Assignee is the only eligibility rule in v1.
		expect(screen.getByRole("link", { name: "acme/project-one" })).toHaveAttribute(
			"href",
			"https://github.com/acme/project-one",
		);
		await userEvent.type(await beginEdit("Assignee"), "octocat");

		submitSettings();

		await waitFor(() => expect(putMock).toHaveBeenCalledTimes(1));
		const body = putMock.mock.calls[0]?.[1]?.body;
		expect(body.config.trackerIntake).toEqual({
			enabled: true,
			provider: "github",
			assignee: "octocat",
		});
	});

	it("blocks save when intake is enabled with no assignee", async () => {
		getMock.mockResolvedValue({
			data: {
				status: "ok",
				project: {
					id: "proj-1",
					name: "Project One",
					kind: "single_repo",
					path: "/repo/project-one",
					repo: "git@github.com:acme/project-one.git",
					defaultBranch: "main",
					config: {
						worker: { agent: "codex" },
						orchestrator: { agent: "claude-code" },
					},
				},
			},
			error: undefined,
		});

		renderSettings("proj-1", undefined, "intake");

		await userEvent.click(await screen.findByLabelText("Enable issue intake"));
		submitSettings();

		expect(await screen.findAllByText("Enabling intake requires an assignee.")).toHaveLength(2);
		expect(putMock).not.toHaveBeenCalled();
	});

	it("restarts when the saved orchestrator agent already differs from the running orchestrator", async () => {
		getMock.mockResolvedValue({
			data: {
				status: "ok",
				project: {
					id: "proj-1",
					name: "Project One",
					kind: "single_repo",
					path: "/repo/project-one",
					repo: "",
					defaultBranch: "main",
					config: {
						worker: { agent: "codex" },
						orchestrator: { agent: "goose" },
					},
				},
			},
			error: undefined,
		});

		renderSettings("proj-1", [
			{
				id: "proj-1",
				name: "Project One",
				path: "/repo/project-one",
				orchestratorAgent: "goose",
				sessions: [
					{
						id: "proj-1-orchestrator",
						workspaceId: "proj-1",
						workspaceName: "Project One",
						title: "Orchestrator",
						provider: "claude-code",
						kind: "orchestrator",
						branch: "ao/proj-1-orchestrator",
						status: "working",
						createdAt: "2026-07-03T00:00:00Z",
						updatedAt: "2026-07-03T00:00:00Z",
						prs: [],
					},
				],
			},
		], "agents");

		const orchestratorAgent = await screen.findByRole("button", { name: "Default orchestrator agent" });
		expect(orchestratorAgent).toHaveTextContent("goose");

		submitSettings();

		await waitFor(() => expect(putMock).toHaveBeenCalledTimes(1));
		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		expect(postMock).toHaveBeenCalledWith("/api/v1/orchestrators", {
			body: { projectId: "proj-1", clean: true },
		});
		await expectReplacementNavigation();
	});

	it("navigates to the replacement orchestrator after changing the default agent", async () => {
		mockProject({
			id: "proj-1",
			name: "Project One",
			kind: "single_repo",
			path: "/repo/project-one",
			repo: "",
			defaultBranch: "main",
			config: {
				worker: { agent: "codex" },
				orchestrator: { agent: "claude-code" },
			},
		});

		renderSettings("proj-1", undefined, "agents");

		const orchestratorAgent = await screen.findByRole("button", { name: "Default orchestrator agent" });
		await chooseOption(orchestratorAgent, "Goose");
		submitSettings();

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		await expectReplacementNavigation();
		expect(setOrchestratorReplacementErrorMock).not.toHaveBeenCalled();
	});

	it("keeps the config save successful when orchestrator replacement fails", async () => {
		getMock.mockResolvedValue({
			data: {
				status: "ok",
				project: {
					id: "proj-1",
					name: "Project One",
					kind: "single_repo",
					path: "/repo/project-one",
					repo: "",
					defaultBranch: "main",
					config: {
						worker: { agent: "codex" },
						orchestrator: { agent: "claude-code" },
					},
				},
			},
			error: undefined,
		});
		postMock.mockResolvedValue({
			data: undefined,
			error: {
				code: "ORCHESTRATOR_SPAWN_FAILED",
				message: "missing goose binary",
				requestId: "request-42",
			},
			response: { status: 500 },
		});

		const queryClient = renderSettings("proj-1", undefined, "agents");
		const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries");

		const orchestratorAgent = await screen.findByRole("button", { name: "Default orchestrator agent" });
		await chooseOption(orchestratorAgent, "goose");
		submitSettings();

		await waitFor(() => expect(putMock).toHaveBeenCalledTimes(1));
		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		expect(await screen.findByText("Saved")).toBeInTheDocument();
		expect(await screen.findByText("Orchestrator restart failed: missing goose binary")).toBeInTheDocument();
		expect(screen.queryByText("Save failed")).not.toBeInTheDocument();
		expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["project", "proj-1"] });
		expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: workspaceQueryKey });
		expect(closeSettingsMock).toHaveBeenCalledTimes(1);
		expect(setOrchestratorReplacementErrorMock).toHaveBeenCalledWith("proj-1", {
			message: "missing goose binary",
			code: "ORCHESTRATOR_SPAWN_FAILED",
			requestId: "request-42",
		});
		expect(captureOrchestratorReplacementFailureMock).toHaveBeenCalledWith(
			expect.objectContaining({
				code: "ORCHESTRATOR_SPAWN_FAILED",
				requestId: "request-42",
			}),
			"proj-1",
		);
	});
});

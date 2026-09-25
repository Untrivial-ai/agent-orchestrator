import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({
	get: vi.fn(),
	post: vi.fn(),
	capture: vi.fn(),
	ensureReadiness: vi.fn(),
	ensureTargetedReadiness: vi.fn(),
	createCloudSession: vi.fn(),
	prepareCloudSession: vi.fn(),
	commitCloudPreparation: vi.fn(),
	renewCloudPreparation: vi.fn(),
	detachCloudPreparation: vi.fn(),
	sendCloudMessage: vi.fn(),
	beginCloudStartupAttempt: vi.fn(() => ({ attemptId: "attempt-1", startedAtMs: 100 })),
	bindCloudStartupAttempt: vi.fn(),
	agentValues: [] as string[],
	agentCatalog: undefined as { agents: ReturnType<typeof import("../test/agent-readiness-fixtures").agentReadiness>[] } | undefined,
	cloudProjects: [] as Array<{ id: string; displayName?: string; repositoryUrl?: string; defaultBranch?: string; config?: Record<string, unknown> }>,
}));

vi.mock("../hooks/useCloudCp", () => ({
	useCloudCp: () => ({
		client: {
			createSession: h.createCloudSession,
			prepareSession: h.prepareCloudSession,
			commitSessionPreparation: h.commitCloudPreparation,
			renewSessionPreparation: h.renewCloudPreparation,
			detachSessionPreparation: h.detachCloudPreparation,
			sendSessionMessage: h.sendCloudMessage,
		},
	}),
}));

vi.mock("../hooks/useCloudOrg", () => ({
	useCloudOrg: () => ({ org: { id: "org-1" } }),
}));

vi.mock("../hooks/useWorkspaceQuery", () => ({
	cloudProjectsQueryKey: ["cloud-projects"] as const,
	cloudSessionsQueryKey: ["cloud-sessions"] as const,
	useCloudProjectsQuery: () => ({ data: h.cloudProjects }),
	useCloudSessionsQuery: () => ({ data: [] }),
}));

vi.mock("../lib/cloud-startup-timing", () => ({
	beginCloudStartupAttempt: h.beginCloudStartupAttempt,
	bindCloudStartupAttempt: h.bindCloudStartupAttempt,
}));

vi.mock("../hooks/useAgentReadinessQuery", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../hooks/useAgentReadinessQuery")>();
	return {
		...actual,
		ensureAgentReadiness: h.ensureTargetedReadiness,
		useAgentReadinessQuery: () => ({ data: h.agentCatalog, isFetching: false }),
		useEnsureAgentReadiness: h.ensureReadiness,
	};
});

vi.mock("./CreateProjectAgentSheet", () => ({
	RequiredAgentField: ({
		value,
		onChange,
		triggerClassName,
		disabled,
	}: {
		value: string;
		onChange: (value: string) => void;
		triggerClassName?: string;
		disabled?: boolean;
	}) => {
		h.agentValues.push(value);
		return (
			<button
				type="button"
				aria-label="Agent"
				className={triggerClassName}
				data-testid="agent-field"
				data-value={value}
				disabled={disabled}
				onClick={() => onChange(value === "codex" ? "claude-code" : "codex")}
			/>
		);
	},
}));

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: h.get,
		POST: h.post,
	},
	apiErrorCode: (error: { code?: string }) => error?.code,
	apiErrorMessage: (error: { message?: string }, fallback = "err") => error?.message ?? fallback,
}));

vi.mock("../lib/telemetry", () => ({ captureRendererEvent: h.capture }));

import { TaskComposer } from "./TaskComposer";
import { agentReadiness } from "../test/agent-readiness-fixtures";
import { agentReadinessQueryKey } from "../hooks/useAgentReadinessQuery";
import { resetCloudPendingSessionsForTests } from "../lib/cloud-pending-session";
import { resetCloudSessionPreparationRegistryForTests } from "../lib/cloud-session-preparation";

const preparationLease = {
	attachmentExpiresAt: "2099-09-23T12:02:00Z",
	expiresAt: "2099-09-23T12:02:00Z",
	generation: 1,
	leaseSeconds: 120,
};

function preparationResponse(id: string) {
	return { claimId: id, disposition: "created", preparation: preparationLease, session: { id } };
}

function Wrap({ children, queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } }) }: {
	children: ReactNode;
	queryClient?: QueryClient;
}) {
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

const task = () => screen.getByRole("textbox", { name: "Task" });
const startTask = () => screen.getByRole("button", { name: "Start task" });

async function waitForTaskReady() {
	await waitFor(() => expect(startTask()).toBeEnabled());
}

beforeEach(() => {
	h.renewCloudPreparation.mockResolvedValue({ preparation: preparationLease });
	h.get.mockImplementation(async (path: string) => {
		if (path.includes("/models")) {
			return {
				data: {
					agent: "codex",
					selectionMode: "text",
					models: [],
					allowCustom: true,
					refreshRecommended: false,
				},
			};
		}
		return { data: { status: "ok", project: { config: {} } } };
	});
});

afterEach(() => {
	h.get.mockReset();
	h.post.mockReset();
	h.capture.mockReset();
	h.ensureReadiness.mockReset();
	h.ensureTargetedReadiness.mockReset();
	h.createCloudSession.mockReset();
	h.prepareCloudSession.mockReset();
	h.commitCloudPreparation.mockReset();
	h.renewCloudPreparation.mockReset();
	h.detachCloudPreparation.mockReset();
	h.sendCloudMessage.mockReset();
	h.beginCloudStartupAttempt.mockClear();
	h.bindCloudStartupAttempt.mockReset();
	h.cloudProjects.length = 0;
	h.agentCatalog = undefined;
	vi.unstubAllGlobals();
	h.agentValues.length = 0;
	window.localStorage.removeItem("ao.taskComposer.preferences.v1");
	resetCloudPendingSessionsForTests();
	resetCloudSessionPreparationRegistryForTests();
});

describe("TaskComposer", () => {
	it("binds a Cloud startup attempt to the session created from the user action", async () => {
		h.cloudProjects.push({ id: "cloud-project" });
		h.prepareCloudSession.mockResolvedValue(preparationResponse("cloud-session-1"));
		h.commitCloudPreparation.mockResolvedValue({ session: { id: "cloud-session-1" } });
		const onCreated = vi.fn();

		render(
			<Wrap>
				<TaskComposer projectId="cloud-project" onCreated={onCreated} />
			</Wrap>,
		);
		await waitFor(() => expect(h.prepareCloudSession).toHaveBeenCalledOnce());
		fireEvent.change(task(), { target: { value: "Measure startup" } });
		fireEvent.click(screen.getByRole("button", { name: "Start task" }));

		await waitFor(() => expect(onCreated).toHaveBeenCalledWith("cloud-session-1"));
		expect(h.createCloudSession).not.toHaveBeenCalled();
		expect(h.commitCloudPreparation).toHaveBeenCalledWith(
			"org-1",
			"cloud-session-1",
			expect.objectContaining({ prompt: "Measure startup" }),
			expect.objectContaining({ idempotencyKey: expect.any(String) }),
		);
		expect(h.beginCloudStartupAttempt).toHaveBeenCalledOnce();
		expect(h.bindCloudStartupAttempt).toHaveBeenCalledWith("cloud-session-1", {
			attemptId: "attempt-1",
			startedAtMs: 100,
		});
	});

	it("opens the pending route before the Cloud create request settles", async () => {
		h.cloudProjects.push({ id: "cloud-project" });
		let resolveCreate!: (value: ReturnType<typeof preparationResponse>) => void;
		h.prepareCloudSession.mockReturnValue(new Promise((resolve) => {
			resolveCreate = resolve;
		}));
		h.commitCloudPreparation.mockResolvedValue({ session: { id: "cloud-session-1" } });
		const onCreated = vi.fn();
		const onPending = vi.fn();

		render(
			<Wrap>
				<TaskComposer projectId="cloud-project" onCreated={onCreated} onPending={onPending} />
			</Wrap>,
		);
		fireEvent.change(task(), { target: { value: "Start immediately" } });
		fireEvent.click(screen.getByRole("button", { name: "Start task" }));

		expect(onPending).toHaveBeenCalledWith("pending-cloud-attempt-1");
		expect(onCreated).not.toHaveBeenCalled();
		expect(h.prepareCloudSession).toHaveBeenCalledWith(
			"org-1",
			expect.any(Object),
			{ idempotencyKey: expect.any(String) },
		);

		resolveCreate(preparationResponse("cloud-session-1"));
		await waitFor(() => expect(onCreated).toHaveBeenCalledWith("cloud-session-1"));
	});

	it("creates a fresh session when preparation commit reports expiry", async () => {
		h.cloudProjects.push({ id: "cloud-project" });
		h.prepareCloudSession.mockResolvedValue(preparationResponse("cloud-session-1"));
		h.commitCloudPreparation.mockRejectedValue(
			Object.assign(new Error("expired"), { code: "PREPARATION_EXPIRED" }),
		);
		h.createCloudSession.mockResolvedValue({ session: { id: "cloud-session-2" } });
		const onCreated = vi.fn();

		render(
			<Wrap>
				<TaskComposer projectId="cloud-project" onCreated={onCreated} />
			</Wrap>,
		);
		await waitFor(() => expect(h.prepareCloudSession).toHaveBeenCalledOnce());
		fireEvent.change(task(), { target: { value: "Keep this prompt" } });
		fireEvent.click(screen.getByRole("button", { name: "Start task" }));

		await waitFor(() => expect(onCreated).toHaveBeenCalledWith("cloud-session-2"));
		expect(h.commitCloudPreparation).toHaveBeenCalledOnce();
		expect(h.createCloudSession).toHaveBeenCalledWith(
			"org-1",
			expect.objectContaining({ prompt: "Keep this prompt" }),
			expect.objectContaining({ idempotencyKey: expect.any(String) }),
		);
	});

	it("falls back to direct creation when session preparation is unsupported", async () => {
		h.cloudProjects.push({ id: "cloud-project" });
		h.prepareCloudSession.mockRejectedValue(
			Object.assign(new Error("route unavailable"), { status: 404 }),
		);
		h.createCloudSession.mockResolvedValue({ session: { id: "cloud-session-2" } });
		const onCreated = vi.fn();
		const prompt =
			"Report the current branch and list the top-level repository files. Then wait for another instruction.";

		render(
			<Wrap>
				<TaskComposer projectId="cloud-project" onCreated={onCreated} />
			</Wrap>,
		);
		await waitFor(() => expect(h.capture).toHaveBeenCalledWith(
			"ao.renderer.cloud_preparation_failed",
			{ project_id: "cloud-project" },
		));
		fireEvent.change(task(), { target: { value: prompt } });
		fireEvent.click(screen.getByRole("button", { name: "Start task" }));

		await waitFor(() => expect(onCreated).toHaveBeenCalledWith("cloud-session-2"));
		expect(h.prepareCloudSession).toHaveBeenCalledOnce();
		expect(h.commitCloudPreparation).not.toHaveBeenCalled();
		expect(h.createCloudSession).toHaveBeenCalledWith(
			"org-1",
			expect.objectContaining({
				displayName: prompt.slice(0, 80),
				prompt,
			}),
			expect.objectContaining({ idempotencyKey: expect.any(String) }),
		);
	});

	it("falls back when the unsupported preparation response arrives after submit", async () => {
		h.cloudProjects.push({ id: "cloud-project" });
		let rejectPreparation!: (error: Error) => void;
		h.prepareCloudSession.mockReturnValue(new Promise((_resolve, reject) => {
			rejectPreparation = reject;
		}));
		h.createCloudSession.mockResolvedValue({ session: { id: "cloud-session-2" } });
		const onCreated = vi.fn();

		render(
			<Wrap>
				<TaskComposer projectId="cloud-project" onCreated={onCreated} />
			</Wrap>,
		);
		fireEvent.change(task(), { target: { value: "Start while preparing" } });
		fireEvent.click(screen.getByRole("button", { name: "Start task" }));

		await act(async () => {
			rejectPreparation(Object.assign(new Error("route unavailable"), { status: 404 }));
		});
		await waitFor(() => expect(onCreated).toHaveBeenCalledWith("cloud-session-2"));
		expect(h.prepareCloudSession).toHaveBeenCalledOnce();
		expect(h.commitCloudPreparation).not.toHaveBeenCalled();
		expect(h.createCloudSession).toHaveBeenCalledOnce();
	});

	it("detaches an unsubmitted Cloud preparation when the composer closes", async () => {
		h.cloudProjects.push({ id: "cloud-project" });
		h.prepareCloudSession.mockResolvedValue(preparationResponse("cloud-session-1"));

		const view = render(
			<Wrap>
				<TaskComposer projectId="cloud-project" onCreated={vi.fn()} />
			</Wrap>,
		);
		await waitFor(() => expect(h.prepareCloudSession).toHaveBeenCalledOnce());
		view.unmount();

		await waitFor(() => expect(h.detachCloudPreparation).toHaveBeenCalledWith(
			"org-1", "cloud-session-1", expect.any(String), 1,
		));
		expect(h.renewCloudPreparation).not.toHaveBeenCalled();
	});

	it("replaces the Cloud preparation when the selected harness changes", async () => {
		h.cloudProjects.push({ id: "cloud-project" });
		h.prepareCloudSession
			.mockResolvedValueOnce(preparationResponse("cloud-session-1"))
			.mockResolvedValueOnce(preparationResponse("cloud-session-2"));
		h.detachCloudPreparation.mockResolvedValue({ preparation: preparationLease });

		render(
			<Wrap>
				<TaskComposer projectId="cloud-project" onCreated={vi.fn()} />
			</Wrap>,
		);
		await waitFor(() => expect(h.prepareCloudSession).toHaveBeenCalledOnce());
		fireEvent.click(screen.getByLabelText("Agent"));

		await waitFor(() => expect(h.prepareCloudSession).toHaveBeenCalledTimes(2));
		expect(h.detachCloudPreparation).toHaveBeenCalledWith(
			"org-1", "cloud-session-1", expect.any(String), 1,
		);
		expect(h.prepareCloudSession.mock.calls[1]?.[1]).toEqual(
			expect.objectContaining({ harness: "codex" }),
		);
	});

	it("preselects the highest-ranked ready agent for a standalone task", async () => {
		h.agentCatalog = {
			agents: [
				agentReadiness("claude-code", "Claude Code"),
				agentReadiness("codex", "Codex", { usageCount: 3, lastUsedAt: "2026-09-20T12:00:00Z" }),
				agentReadiness("cursor", "Cursor", { authentication: "unauthorized" }),
			],
		};

		render(
			<Wrap>
				<TaskComposer projectId="__standalone__" onCreated={vi.fn()} />
			</Wrap>,
		);

		await waitFor(() => expect(screen.getByLabelText("Agent")).toHaveAttribute("data-value", "codex"));
		await waitFor(() =>
			expect(screen.queryByRole("status", { name: "Loading models…" })).not.toBeInTheDocument(),
		);
	});

	it("preserves an explicitly selected standalone agent when readiness rankings refresh", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		h.agentCatalog = {
			agents: [
				agentReadiness("codex", "Codex", { usageCount: 3, lastUsedAt: "2026-09-20T12:00:00Z" }),
				agentReadiness("claude-code", "Claude Code"),
			],
		};

		const { rerender } = render(
			<Wrap queryClient={queryClient}>
				<TaskComposer projectId="__standalone__" onCreated={vi.fn()} />
			</Wrap>,
		);

		await waitFor(() => expect(screen.getByLabelText("Agent")).toHaveAttribute("data-value", "codex"));
		fireEvent.click(screen.getByLabelText("Agent"));
		expect(screen.getByLabelText("Agent")).toHaveAttribute("data-value", "claude-code");

		h.agentCatalog = {
			agents: [
				agentReadiness("claude-code", "Claude Code"),
				agentReadiness("codex", "Codex", { usageCount: 10, lastUsedAt: "2026-09-21T12:00:00Z" }),
			],
		};
		rerender(
			<Wrap queryClient={queryClient}>
				<TaskComposer projectId="__standalone__" onCreated={vi.fn()} />
			</Wrap>,
		);

		await waitFor(() =>
			expect(screen.getByLabelText("Agent")).toHaveAttribute("data-value", "claude-code"),
		);
	});

	it("restores a standalone agent's draft model after switching away and back", async () => {
		h.agentCatalog = {
			agents: [
				agentReadiness("codex", "Codex", { usageCount: 3, lastUsedAt: "2026-09-20T12:00:00Z" }),
				agentReadiness("claude-code", "Claude Code"),
			],
		};
		h.get.mockImplementation(async (path: string, request?: { params?: { path?: { agent?: string } } }) => {
			if (path.includes("/models")) {
				return request?.params?.path?.agent === "claude-code"
					? {
							data: {
								agent: "claude-code",
								selectionMode: "text",
								models: [{ id: "fable-5.1", label: "Fable 5.1", isDefault: true }],
								allowCustom: true,
							},
						}
					: {
							data: {
								agent: "codex",
								selectionMode: "text",
								models: [
									{ id: "gpt-6-astra", label: "GPT-6-Astra", isDefault: true },
									{ id: "gpt-5.5", label: "GPT-5.5" },
								],
								allowCustom: true,
							},
						};
			}
			return { data: { status: "ok", project: { config: {} } } };
		});

		render(<Wrap><TaskComposer projectId="__standalone__" onCreated={vi.fn()} /></Wrap>);

		const model = await screen.findByRole("button", { name: "Model" });
		expect(model).toHaveTextContent("GPT-6-Astra");
		await userEvent.click(model);
		await userEvent.click(await screen.findByRole("menuitem", { name: "GPT-5.5" }));
		expect(model).toHaveTextContent("GPT-5.5");

		fireEvent.click(screen.getByLabelText("Agent"));
		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("Fable 5.1");
		fireEvent.click(screen.getByLabelText("Agent"));

		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("GPT-5.5");
	});

	it("restores the last successfully spawned standalone harness and model in a new composer", async () => {
		h.agentCatalog = {
			agents: [
				agentReadiness("codex", "Codex", { usageCount: 3, lastUsedAt: "2026-09-20T12:00:00Z" }),
				agentReadiness("claude-code", "Claude Code"),
			],
		};
		h.get.mockImplementation(async (path: string, request?: { params?: { path?: { agent?: string } } }) => {
			if (path.includes("/models")) {
				return request?.params?.path?.agent === "claude-code"
					? {
							data: {
								agent: "claude-code",
								selectionMode: "text",
								models: [{ id: "fable-5.1", label: "Fable 5.1", isDefault: true }],
								allowCustom: true,
							},
						}
					: {
							data: {
								agent: "codex",
								selectionMode: "text",
								models: [
									{ id: "gpt-6-astra", label: "GPT-6-Astra", isDefault: true },
									{ id: "gpt-5.5", label: "GPT-5.5" },
								],
								allowCustom: true,
							},
						};
			}
			return { data: { status: "ok", project: { config: {} } } };
		});
		h.post.mockResolvedValueOnce({ data: { session: { id: "standalone-remembered" } } });

		const first = render(<Wrap><TaskComposer projectId="__standalone__" onCreated={vi.fn()} /></Wrap>);
		fireEvent.click(await screen.findByLabelText("Agent"));
		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("Fable 5.1");
		fireEvent.change(task(), { target: { value: "Remember this setup" } });
		fireEvent.click(screen.getByText("Start task"));
		await waitFor(() => expect(h.post).toHaveBeenCalledTimes(1));
		first.unmount();

		render(<Wrap><TaskComposer projectId="__standalone__" onCreated={vi.fn()} /></Wrap>);

		await waitFor(() => expect(screen.getByLabelText("Agent")).toHaveAttribute("data-value", "claude-code"));
		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("Fable 5.1");
	});

	it("persists successful model choices per project without leaking them to another project", async () => {
		h.agentCatalog = { agents: [agentReadiness("codex", "Codex")] };
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return {
					data: {
						agent: "codex",
						selectionMode: "text",
						models: [
							{ id: "gpt-6-astra", label: "GPT-6-Astra", isDefault: true },
							{ id: "gpt-5.5", label: "GPT-5.5" },
						],
						allowCustom: true,
					},
				};
			}
			return { data: { status: "ok", project: { agent: "codex", config: {} } } };
		});
		h.post.mockResolvedValueOnce({ data: { workerId: "project-remembered" } });

		const first = render(<Wrap><TaskComposer projectId="project-a" onCreated={vi.fn()} /></Wrap>);
		const model = await screen.findByRole("button", { name: "Model" });
		await userEvent.click(model);
		await userEvent.click(await screen.findByRole("menuitem", { name: "GPT-5.5" }));
		fireEvent.change(task(), { target: { value: "Remember this project setup" } });
		fireEvent.click(screen.getByText("Start task"));
		await waitFor(() => expect(h.post).toHaveBeenCalledTimes(1));
		first.unmount();

		const remembered = render(<Wrap><TaskComposer projectId="project-a" onCreated={vi.fn()} /></Wrap>);
		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("GPT-5.5");
		remembered.unmount();

		render(<Wrap><TaskComposer projectId="project-b" onCreated={vi.fn()} /></Wrap>);
		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("GPT-6-Astra");
	});

	it("does not persist a standalone preference when task creation fails", async () => {
		h.agentCatalog = { agents: [agentReadiness("codex", "Codex")] };
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return {
					data: {
						agent: "codex",
						selectionMode: "text",
						models: [
							{ id: "gpt-6-astra", label: "GPT-6-Astra", isDefault: true },
							{ id: "gpt-5.5", label: "GPT-5.5" },
						],
						allowCustom: true,
					},
				};
			}
			return { data: { status: "ok", project: { config: {} } } };
		});
		h.post.mockResolvedValueOnce({ error: { code: "SPAWN_FAILED", message: "Could not start task" } });

		render(<Wrap><TaskComposer projectId="__standalone__" onCreated={vi.fn()} /></Wrap>);
		const model = await screen.findByRole("button", { name: "Model" });
		await userEvent.click(model);
		await userEvent.click(await screen.findByRole("menuitem", { name: "GPT-5.5" }));
		fireEvent.change(task(), { target: { value: "This spawn will fail" } });
		fireEvent.click(screen.getByText("Start task"));

		expect(await screen.findByText("Could not start task")).toBeInTheDocument();
		expect(window.localStorage.getItem("ao.taskComposer.preferences.v1")).toBeNull();
	});

	it("falls back when remembered standalone harness and model choices are no longer valid", async () => {
		window.localStorage.setItem("ao.taskComposer.preferences.v1", JSON.stringify({
			__standalone__: {
				lastAgent: "removed-agent",
				agents: {
					codex: { model: "retired-model", mode: "" },
					"removed-agent": { model: "removed-model", mode: "" },
				},
			},
		}));
		h.agentCatalog = { agents: [agentReadiness("codex", "Codex")] };
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return {
					data: {
						agent: "codex",
						selectionMode: "text",
						models: [{ id: "gpt-6-astra", label: "GPT-6-Astra", isDefault: true }],
						allowCustom: false,
					},
				};
			}
			return { data: { status: "ok", project: { config: {} } } };
		});

		render(<Wrap><TaskComposer projectId="__standalone__" onCreated={vi.fn()} /></Wrap>);

		await waitFor(() => expect(screen.getByLabelText("Agent")).toHaveAttribute("data-value", "codex"));
		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("GPT-6-Astra");
	});

	it("keeps a standalone task unselected when no agent is ready", () => {
		h.agentCatalog = {
			agents: [
				agentReadiness("claude-code", "Claude Code", { installation: "not_installed" }),
				agentReadiness("codex", "Codex", { authentication: "unauthorized" }),
			],
		};

		render(
			<Wrap>
				<TaskComposer projectId="__standalone__" onCreated={vi.fn()} />
			</Wrap>,
		);

		expect(screen.getByLabelText("Agent")).toHaveAttribute("data-value", "");
		expect(screen.getByLabelText("Model")).toHaveTextContent("Select agent");
		expect(screen.getByLabelText("Model")).toHaveAttribute("aria-disabled", "true");
	});

	it("starts a standalone worker without loading or sending a project", async () => {
		const onCreated = vi.fn();
		h.agentCatalog = { agents: [agentReadiness("codex", "Codex")] };
		h.post.mockResolvedValueOnce({ data: { session: { id: "standalone-1" } } });

		render(
			<Wrap>
				<TaskComposer projectId="__standalone__" onCreated={onCreated} />
			</Wrap>,
		);

		await waitFor(() => expect(screen.getByLabelText("Agent")).toHaveAttribute("data-value", "codex"));
		fireEvent.change(task(), { target: { value: "Research release options" } });
		fireEvent.click(screen.getByText("Start task"));

		await waitFor(() => expect(onCreated).toHaveBeenCalledWith("standalone-1"));
		expect(h.post).toHaveBeenCalledWith(
			"/api/v1/sessions",
			expect.objectContaining({
				body: expect.objectContaining({
					kind: "worker",
					harness: "codex",
					prompt: "Research release options",
				}),
			}),
		);
		expect(h.post.mock.calls[0][1].body).not.toHaveProperty("projectId");
		expect(h.get.mock.calls.some(([path]) => path === "/api/v1/projects/{id}")).toBe(false);
	});

	it("sends the selected effort when starting a standalone worker", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return {
					data: {
						agent: "codex",
						selectionMode: "text",
						models: [{ id: "gpt-5", label: "GPT-5", isDefault: true, efforts: ["high"] }],
						allowCustom: true,
						refreshRecommended: false,
					},
				};
			}
			return { data: { status: "ok", project: { config: {} } } };
		});
		h.post.mockResolvedValueOnce({ data: { session: { id: "standalone-1" } } });

		render(
			<Wrap>
				<TaskComposer projectId="__standalone__" onCreated={vi.fn()} />
			</Wrap>,
		);

		fireEvent.click(screen.getByLabelText("Agent"));
		const effort = await screen.findByRole("button", { name: "Effort" });
		await userEvent.click(effort);
		await userEvent.click(screen.getByRole("menuitem", { name: "High" }));
		fireEvent.click(screen.getByText("Start task"));

		await waitFor(() =>
			expect(h.post).toHaveBeenCalledWith(
				"/api/v1/sessions",
				expect.objectContaining({ body: expect.objectContaining({ effort: "high" }) }),
			),
		);
	});

	it("ensures display readiness for every harness when the composer opens", async () => {
		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		await waitFor(() => expect(h.ensureReadiness).toHaveBeenCalledWith());
	});

	it("ensures the selected harness when agent selection changes", async () => {
		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		fireEvent.click(screen.getByLabelText("Agent"));
		await waitFor(() =>
			expect(h.ensureReadiness).toHaveBeenCalledWith({
				agentIds: ["codex"],
				enabled: true,
				purpose: "launch",
			}),
		);
	});

	it("submits a gateway-backed Claude project when refreshed global readiness is unauthorized", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return { data: { agent: "claude-code", selectionMode: "text", models: [], allowCustom: true } };
			}
			return {
				data: {
					status: "ok",
					project: {
						agent: "claude-code",
						config: { env: { ANTHROPIC_BASE_URL: "https://gateway.example" } },
					},
				},
			};
		});
		const unauthorized = agentReadiness("claude-code", "Claude Code", { authentication: "unauthorized" });
		h.ensureTargetedReadiness.mockResolvedValueOnce({ agents: [unauthorized] });
		h.post.mockResolvedValueOnce({ data: { workerId: "worker-1" } });
		const onCreated = vi.fn();

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={onCreated} />
			</Wrap>,
		);
		await waitFor(() => expect(screen.getByTestId("agent-field")).toHaveAttribute("data-value", "claude-code"));
		fireEvent.click(screen.getByRole("button", { name: "Start task" }));

		await waitFor(() => expect(onCreated).toHaveBeenCalledWith("worker-1"));
		expect(h.ensureTargetedReadiness).toHaveBeenCalledWith(["claude-code"], "launch");
	});

	it("keeps submission enabled for a gateway-backed Claude project when cached global readiness is unauthorized", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return { data: { agent: "claude-code", selectionMode: "text", models: [], allowCustom: true } };
			}
			return {
				data: {
					status: "ok",
					project: {
						agent: "claude-code",
						config: { env: { ANTHROPIC_BASE_URL: "https://gateway.example" } },
					},
				},
			};
		});
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		h.agentCatalog = {
			agents: [agentReadiness("claude-code", "Claude Code", { authentication: "unauthorized" })],
		};
		h.ensureTargetedReadiness.mockResolvedValueOnce({
			agents: [agentReadiness("claude-code", "Claude Code", { authentication: "authorized" })],
		});
		h.post.mockResolvedValueOnce({ data: { workerId: "worker-1" } });

		render(
			<Wrap queryClient={queryClient}>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);
		await waitFor(() => expect(screen.getByTestId("agent-field")).toHaveAttribute("data-value", "claude-code"));
		const submit = screen.getByRole("button", { name: "Start task" });
		expect(submit).toBeEnabled();
		fireEvent.click(submit);

		await waitFor(() => expect(h.ensureTargetedReadiness).toHaveBeenCalledWith(["claude-code"], "launch"));
		await waitFor(() => expect(h.post).toHaveBeenCalled());
	});

	it("waits for project context before allowing a local task to start", async () => {
		let resolveProject!: (value: unknown) => void;
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return { data: { agent: "codex", selectionMode: "text", models: [], allowCustom: true } };
			}
			if (path === "/api/v1/projects/{id}") {
				return new Promise((resolve) => {
					resolveProject = resolve;
				});
			}
			return { data: undefined };
		});

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		expect(startTask()).toBeDisabled();
		expect(screen.getByRole("status", { name: "loading project context…" })).toBeInTheDocument();
		fireEvent.change(task(), { target: { value: "should wait" } });
		fireEvent.keyDown(task(), { key: "Enter", shiftKey: false, altKey: false });
		expect(h.post).not.toHaveBeenCalled();

		await act(async () =>
			resolveProject({ data: { status: "ok", project: { name: "my-app", repo: "acme/my-app", defaultBranch: "main", path: "/repo", config: {} } } }),
		);
		await waitForTaskReady();
	});

	it.each([
		["binary launch failure", "AGENT_BINARY_NOT_FOUND"],
		["project credential rejection", "AGENT_AUTH_REQUIRED"],
	] as const)("waits for and caches targeted readiness after a %s", async (_name, errorCode) => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return { data: { agent: "codex", selectionMode: "text", models: [], allowCustom: true } };
			}
			return { data: { status: "ok", project: { agent: "codex", config: {} } } };
		});
		h.post.mockResolvedValueOnce({
			error: { code: errorCode, message: "Codex is not ready" },
		});
		const stale = agentReadiness("codex", "Codex", { freshness: "stale" });
		const completed = agentReadiness("codex", "Codex", { installation: "not_installed" });
		let finishReadiness!: (value: { agents: ReturnType<typeof agentReadiness>[] }) => void;
		h.ensureTargetedReadiness
			.mockResolvedValueOnce({ agents: [stale] })
			.mockReturnValueOnce(
			new Promise((resolve) => {
				finishReadiness = resolve;
			}),
			);
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		queryClient.setQueryData(agentReadinessQueryKey, { agents: [stale] });

		render(
			<Wrap queryClient={queryClient}>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);
		await waitFor(() => expect(screen.getByTestId("agent-field")).toHaveAttribute("data-value", "codex"));
		fireEvent.click(screen.getByRole("button", { name: "Start task" }));

		await waitFor(() => expect(h.ensureTargetedReadiness).toHaveBeenCalledTimes(2));
		expect(h.ensureTargetedReadiness).toHaveBeenLastCalledWith(["codex"], "launch");
		expect(screen.queryByText("Codex is not ready")).not.toBeInTheDocument();

		await act(async () => finishReadiness({ agents: [completed] }));
		expect(await screen.findByText("Codex is not ready")).toBeInTheDocument();
		expect(queryClient.getQueryData(agentReadinessQueryKey)).toEqual({ agents: [completed] });
	});

	it("starts a promptless worker when the task is empty", async () => {
		const onCreated = vi.fn();
		h.post.mockResolvedValueOnce({ data: { workerId: "sess-empty" } });

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={onCreated} />
			</Wrap>,
		);

		expect(task().getAttribute("placeholder")).toBeTruthy();
		expect(task()).toHaveClass("min-h-[calc(3lh+1.75rem)]");
		await waitForTaskReady();
		fireEvent.click(screen.getByText("Start task"));

		await waitFor(() =>
			expect(h.post).toHaveBeenCalledWith(
				"/api/v1/orchestrators/delegate",
				expect.objectContaining({ body: expect.objectContaining({ projectId: "proj-1", brief: "" }) }),
			),
		);
		expect(onCreated).toHaveBeenCalledWith("sess-empty");
	});

	it("keeps prompt guidance in the field instead of adding a separate footer row", () => {
		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		expect(task().getAttribute("placeholder")).toBeTruthy();
		expect(screen.queryByText("Start now — details can come later.")).not.toBeInTheDocument();
		expect(screen.queryByText("Shift+Enter for a new line")).not.toBeInTheDocument();
		fireEvent.change(task(), { target: { value: "Investigate the failure" } });
		expect(task()).toHaveValue("Investigate the failure");
	});

	it("does not rerender the agent control for every prompt keystroke", async () => {
		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		await waitFor(() => expect(screen.getByTestId("agent-field")).toBeInTheDocument());
		h.agentValues.length = 0;

		fireEvent.change(task(), { target: { value: "a" } });
		fireEvent.change(task(), { target: { value: "ab" } });
		fireEvent.change(task(), { target: { value: "abc" } });

		expect(h.agentValues).toHaveLength(1);
	});

	it("uses 2:2:1 toolbar tracks when the selected model advertises effort", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return { data: {
					agent: "codex",
					selectionMode: "catalog",
					models: [{ id: "gpt-test", label: "GPT Test", isDefault: true, efforts: ["low", "high"] }],
					allowCustom: true,
				} };
			}
			return { data: { status: "ok", project: { agent: "codex", config: {} } } };
		});
		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		await screen.findByRole("button", { name: "Effort" });
		const runControls = screen.getByRole("group", { name: "Runs with" });
		expect(runControls).toHaveClass("composer-run-controls", "composer-run-controls-with-effort");
		expect(runControls.closest(".composer-toolbar")).not.toBeNull();
		expect(runControls.querySelectorAll(".composer-toolbar-slot")).toHaveLength(3);
		expect(screen.getByTestId("agent-field").closest(".composer-toolbar-slot")).not.toBeNull();
		expect(screen.getByLabelText("Model").closest(".composer-toolbar-slot")).not.toBeNull();
		expect(screen.getByLabelText("Effort").closest(".composer-toolbar-effort-slot")).not.toBeNull();
	});

	it("keeps the file attach control in the bottom action row", () => {
		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		expect(screen.getByRole("button", { name: "Add file" }).closest(".composer-toolbar")).not.toBeNull();
	});

	it("emits busy state around an in-flight create and reports the new session", async () => {
		const onSubmittingChange = vi.fn();
		const onCreated = vi.fn();
		let resolveCreate!: (value: { data: { workerId: string } }) => void;
		h.post.mockReturnValueOnce(new Promise((resolve) => (resolveCreate = resolve)));

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={onCreated} onSubmittingChange={onSubmittingChange} />
			</Wrap>,
		);

		fireEvent.change(task(), { target: { value: "Do the thing" } });
		await waitForTaskReady();
		fireEvent.click(screen.getByText("Start task"));

		await waitFor(() => expect(onSubmittingChange).toHaveBeenLastCalledWith(true));
		expect(h.post).toHaveBeenCalledWith(
			"/api/v1/orchestrators/delegate",
			expect.objectContaining({
				body: expect.not.objectContaining({ attachments: expect.anything() }),
			}),
		);
		expect(h.post).toHaveBeenCalledWith(
			"/api/v1/orchestrators/delegate",
			expect.objectContaining({
				body: expect.objectContaining({ projectId: "proj-1", brief: "Do the thing" }),
			}),
		);

		await act(async () => resolveCreate({ data: { workerId: "sess-1" } }));
		await waitFor(() => expect(onCreated).toHaveBeenCalledWith("sess-1"));
		await waitFor(() => expect(onSubmittingChange).toHaveBeenLastCalledWith(false));
	});

	it("synchronously blocks duplicate local submissions", async () => {
		let resolveCreate!: (value: { data: { workerId: string } }) => void;
		h.post.mockReturnValueOnce(new Promise((resolve) => (resolveCreate = resolve)));

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		fireEvent.change(task(), { target: { value: "Create one worker" } });
		await waitForTaskReady();
		const form = task().closest("form");
		if (!form) throw new Error("task form missing");
		act(() => {
			fireEvent.submit(form);
			fireEvent.submit(form);
		});

		await waitFor(() => expect(h.post).toHaveBeenCalledOnce());
		expect(h.post).toHaveBeenCalledWith(
			"/api/v1/orchestrators/delegate",
			expect.objectContaining({
				body: expect.objectContaining({ idempotencyKey: expect.any(String) }),
			}),
		);
		await act(async () => resolveCreate({ data: { workerId: "sess-1" } }));
	});

	it("reuses the local idempotency key after an ambiguous client failure", async () => {
		vi.stubGlobal("crypto", { randomUUID: vi.fn(() => "local-request-1") });
		h.post
			.mockRejectedValueOnce(new Error("connection lost"))
			.mockResolvedValueOnce({ data: { workerId: "sess-1" } });

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);
		fireEvent.change(task(), { target: { value: "Retry safely" } });
		await waitForTaskReady();
		fireEvent.click(startTask());
		await screen.findByText("connection lost");
		fireEvent.click(startTask());

		await waitFor(() => expect(h.post).toHaveBeenCalledTimes(2));
		const keys = h.post.mock.calls.map(([, request]) => request.body.idempotencyKey);
		expect(keys).toEqual(["local-request-1", "local-request-1"]);
	});

	it.each([
		"TASK_DELEGATION_RECOVERY_REQUIRED", "TASK_DELEGATION_COMMIT_FAILED",
		"TASK_DELEGATION_IN_PROGRESS", "SPAWN_INTERNAL", "SPAWN_TIMEOUT", "SPAWN_CANCELLED",
	])("retains the local request identity after %s", async (code) => {
		let sequence = 0;
		vi.stubGlobal("crypto", { randomUUID: vi.fn(() => `local-request-${++sequence}`) });
		h.post
			.mockResolvedValueOnce({ error: { code, message: "Inspect the existing session before retrying" } })
			.mockResolvedValueOnce({ data: { workerId: "existing-worker" } });
		const onCreated = vi.fn();
		render(<Wrap><TaskComposer projectId="proj-1" onCreated={onCreated} /></Wrap>);
		fireEvent.change(task(), { target: { value: "Retry safely" } });
		await waitForTaskReady();
		fireEvent.click(startTask());
		await screen.findByText("Inspect the existing session before retrying");
		fireEvent.click(startTask());
		await waitFor(() => expect(onCreated).toHaveBeenCalledWith("existing-worker"));
		expect(h.post.mock.calls.map(([, request]) => request.body.idempotencyKey))
			.toEqual(["local-request-1", "local-request-1"]);
	});

	it("uses a new local idempotency key after a definitive server rejection", async () => {
		let sequence = 0;
		vi.stubGlobal("crypto", { randomUUID: vi.fn(() => `local-request-${++sequence}`) });
		h.post
			.mockResolvedValueOnce({ error: { code: "UNKNOWN_HARNESS", message: "Selection unavailable" } })
			.mockResolvedValueOnce({ data: { workerId: "sess-1" } });

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);
		fireEvent.change(task(), { target: { value: "Retry after setup" } });
		await waitForTaskReady();
		fireEvent.click(startTask());
		await screen.findByText("Selection unavailable");
		fireEvent.click(startTask());

		await waitFor(() => expect(h.post).toHaveBeenCalledTimes(2));
		const keys = h.post.mock.calls.map(([, request]) => request.body.idempotencyKey);
		expect(keys).toEqual(["local-request-1", "local-request-2"]);
	});

	it("locks agent and model selection while task creation is in flight, then unlocks them after failure", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return {
					data: {
						agent: "codex",
						selectionMode: "text",
						models: [],
						allowCustom: true,
						refreshRecommended: false,
					},
				};
			}
			return { data: { status: "ok", project: { agent: "codex", config: {} } } };
		});
		let rejectCreate!: (error: Error) => void;
		h.post.mockReturnValueOnce(new Promise((_resolve, reject) => (rejectCreate = reject)));

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		const agent = await screen.findByTestId("agent-field");
		await waitFor(() => expect(agent).toHaveAttribute("data-value", "codex"));
		const model = await screen.findByRole("button", { name: "Model" });
		const prompt = task();
		expect(agent).toBeEnabled();
		expect(model).toBeEnabled();
		expect(prompt).toBeEnabled();

		fireEvent.click(screen.getByRole("button", { name: "Start task" }));

		await waitFor(() => expect(h.post).toHaveBeenCalledOnce());
		expect(agent).toBeDisabled();
		expect(model).toBeDisabled();
		expect(prompt).toBeDisabled();

		await act(async () => rejectCreate(new Error("creation failed")));
		await screen.findByText("creation failed");
		expect(agent).toBeEnabled();
		expect(model).toBeEnabled();
		expect(prompt).toBeEnabled();
	});

	it.each([
		{
			name: "mode",
			catalog: {
				agent: "codex",
				selectionMode: "mode",
				models: [{ id: "plan", label: "Plan", isDefault: true }],
				customModelEntry: "none",
				allowCustom: false,
			},
			controls: async () => [await screen.findByRole("button", { name: "Model" })],
		},
		{
			name: "catalog",
			catalog: {
				agent: "codex",
				selectionMode: "catalog",
				models: [{ id: "gpt-5", label: "GPT-5", isDefault: true }],
				customModelEntry: "none",
				allowCustom: false,
			},
			controls: async () => [await screen.findByRole("button", { name: "Model" })],
		},
		{
			name: "search and direct model ID",
			catalog: {
				agent: "codex",
				selectionMode: "catalog",
				models: [{ id: "gpt-5", label: "GPT-5", isDefault: true }],
				customModelEntry: "direct",
				allowCustom: true,
			},
			controls: async () => {
				const model = await screen.findByRole("button", { name: "Model" });
				await userEvent.click(model);
				await userEvent.type(screen.getByRole("searchbox", { name: "Search model" }), "private/model-id");
				await userEvent.click(
					screen.getByRole("menuitem", { name: "Use “private/model-id” as a custom model" }),
				);
				expect(model).toHaveTextContent("private/model-id");
				expect(screen.queryByRole("textbox", { name: "Model" })).not.toBeInTheDocument();
				return [model];
			},
		},
	])("locks the $name selector while creating and restores it after failure", async ({ catalog, controls }) => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) return { data: catalog };
			return { data: { status: "ok", project: { agent: "codex", config: {} } } };
		});
		let rejectCreate!: (error: Error) => void;
		h.post.mockReturnValueOnce(new Promise((_resolve, reject) => (rejectCreate = reject)));

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		const modelControls = await controls();
		for (const control of modelControls) expect(control).toBeEnabled();

		fireEvent.click(screen.getByRole("button", { name: "Start task" }));

		await waitFor(() => expect(h.post).toHaveBeenCalledOnce());
		for (const control of modelControls) expect(control).toBeDisabled();

		await act(async () => rejectCreate(new Error("creation failed")));
		await screen.findByText("creation failed");
		for (const control of modelControls) expect(control).toBeEnabled();
	});

	it("attaches a selected file and sends it in the delegate body", async () => {
		h.post.mockResolvedValueOnce({ data: { workerId: "sess-1" } });

		const { container } = render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		const input = container.querySelector('input[type="file"]') as HTMLInputElement;
		const file = new File([new Uint8Array([1, 2, 3])], "notes.txt", { type: "text/plain" });
		fireEvent.change(input, { target: { files: [file] } });

		expect(await screen.findByText("notes.txt")).toBeInTheDocument();

		fireEvent.change(task(), { target: { value: "Use the notes" } });
		await waitForTaskReady();
		fireEvent.click(screen.getByText("Start task"));

		await waitFor(() => expect(h.post).toHaveBeenCalledTimes(1));
		const body = h.post.mock.calls[0][1].body as {
			attachments?: Array<{ mimeType: string; data: string }>;
		};
		expect(h.post.mock.calls[0][1].headers).toEqual({ "X-AO-Attachment-Upload": "1" });
		expect(body.attachments).toHaveLength(1);
		expect(body.attachments?.[0].mimeType).toBe("text/plain");
		expect(body.attachments?.[0].data.length).toBeGreaterThan(0);
	});

	it("rejects cloud task attachments instead of silently dropping them", async () => {
		h.prepareCloudSession.mockResolvedValue(preparationResponse("cloud-attachment-session"));
		h.cloudProjects.push({ id: "cloud-1", displayName: "Cloud", repositoryUrl: "https://example.com/repo", defaultBranch: "main", config: {} });
		const onCreated = vi.fn();
		const { container } = render(<Wrap><TaskComposer projectId="cloud-1" onCreated={onCreated} /></Wrap>);
		const input = container.querySelector('input[type="file"]') as HTMLInputElement;
		fireEvent.change(input, { target: { files: [new File(["notes"], "notes.txt", { type: "text/plain" })] } });
		expect(await screen.findByText("notes.txt")).toBeInTheDocument();

		fireEvent.change(task(), { target: { value: "Read the notes" } });
		await waitForTaskReady();
		fireEvent.click(startTask());

		expect(await screen.findByRole("alert")).toHaveTextContent("File attachments are not supported for cloud tasks yet.");
		expect(h.prepareCloudSession).toHaveBeenCalledOnce();
		expect(h.commitCloudPreparation).not.toHaveBeenCalled();
		expect(h.createCloudSession).not.toHaveBeenCalled();
		expect(onCreated).not.toHaveBeenCalled();
		expect(h.post).not.toHaveBeenCalled();
	});

	it("waits for a selected file read before submitting", async () => {
		h.post.mockResolvedValueOnce({ data: { workerId: "sess-1" } });
		let finishRead!: () => void;
		class SlowFileReader {
			error: Error | null = null;
			result: string | ArrayBuffer | null = null;
			onerror: (() => void) | null = null;
			onload: (() => void) | null = null;

			readAsDataURL(file: File) {
				finishRead = () => {
					this.result = `data:${file.type};base64,AQID`;
					this.onload?.();
				};
			}
		}
		vi.stubGlobal("FileReader", SlowFileReader);

		const { container } = render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		const input = container.querySelector('input[type="file"]') as HTMLInputElement;
		fireEvent.change(input, {
			target: { files: [new File([new Uint8Array([1, 2, 3])], "slow.txt", { type: "text/plain" })] },
		});
		fireEvent.change(task(), { target: { value: "Use the slow file" } });
		await waitForTaskReady();
		fireEvent.click(screen.getByText("Start task"));

		expect(h.post).not.toHaveBeenCalled();

		await act(async () => finishRead());
		await waitFor(() => expect(h.post).toHaveBeenCalledTimes(1));
		expect(h.post.mock.calls[0][1].body).toMatchObject({
			attachments: [{ mimeType: "text/plain", data: "AQID" }],
		});
	});

	it("waits for both rapidly selected file batches before delegating", async () => {
		h.post.mockResolvedValueOnce({ data: { workerId: "sess-1" } });
		const pendingReads: Array<() => void> = [];
		class SlowFileReader {
			error: Error | null = null;
			result: string | ArrayBuffer | null = null;
			onerror: (() => void) | null = null;
			onload: (() => void) | null = null;

			readAsDataURL(file: File) {
				pendingReads.push(() => {
					this.result = `data:${file.type};base64,${file.name === "first.txt" ? "AQ==" : "Ag=="}`;
					this.onload?.();
				});
			}
		}
		vi.stubGlobal("FileReader", SlowFileReader);
		const { container } = render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);
		const input = container.querySelector('input[type="file"]') as HTMLInputElement;
		fireEvent.change(input, {
			target: { files: [new File([new Uint8Array([1])], "first.txt", { type: "text/plain" })] },
		});
		fireEvent.change(input, {
			target: { files: [new File([new Uint8Array([2])], "second.txt", { type: "text/plain" })] },
		});
		fireEvent.change(task(), { target: { value: "Use both files" } });
		await waitForTaskReady();
		fireEvent.click(screen.getByText("Start task"));
		expect(h.post).not.toHaveBeenCalled();

		await act(async () => pendingReads.shift()?.());
		await waitFor(() => expect(pendingReads).toHaveLength(1));
		expect(h.post).not.toHaveBeenCalled();
		await act(async () => pendingReads.shift()?.());

		await waitFor(() => expect(h.post).toHaveBeenCalledTimes(1));
		expect(h.post.mock.calls[0][1].body).toMatchObject({
			attachments: [
				{ mimeType: "text/plain", data: "AQ==" },
				{ mimeType: "text/plain", data: "Ag==" },
			],
		});
	});

	it("removes a selected file before submitting", async () => {
		h.post.mockResolvedValueOnce({ data: { workerId: "sess-1" } });

		const { container } = render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		const input = container.querySelector('input[type="file"]') as HTMLInputElement;
		const file = new File([new Uint8Array([1, 2, 3])], "notes.txt", { type: "text/plain" });
		fireEvent.change(input, { target: { files: [file] } });

		expect(await screen.findByText("notes.txt")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Remove notes.txt" }));
		await waitFor(() => expect(screen.queryByText("notes.txt")).not.toBeInTheDocument());

		fireEvent.change(task(), { target: { value: "No attachment now" } });
		fireEvent.click(screen.getByText("Start task"));

		await waitFor(() => expect(h.post).toHaveBeenCalledTimes(1));
		expect(h.post.mock.calls[0][1].headers).toBeUndefined();
		expect(h.post.mock.calls[0][1].body).not.toHaveProperty("attachments");
	});

	it("clears busy state when a create rejects", async () => {
		const onSubmittingChange = vi.fn();
		h.post.mockRejectedValueOnce(new Error("nope"));

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} onSubmittingChange={onSubmittingChange} />
			</Wrap>,
		);

		fireEvent.change(task(), { target: { value: "B" } });
		await waitForTaskReady();
		fireEvent.click(screen.getByText("Start task"));

		await waitFor(() => expect(screen.getByText("nope")).toBeInTheDocument());
		await waitFor(() => expect(onSubmittingChange).toHaveBeenLastCalledWith(false));
	});

	it("silently routes agents without Chat support to Terminal UI", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path === "/api/v1/settings") {
				return { data: { defaultSessionMode: "chat", chatHarnesses: ["codex"] } };
			}
			if (path.includes("/models")) {
				return {
					data: {
						agent: "grok",
						selectionMode: "catalog",
						models: [{ id: "grok-4.6", label: "grok-4.6", isDefault: true }],
						allowCustom: true,
					},
				};
			}
			return { data: { status: "ok", project: { agent: "grok", config: {} } } };
		});
		h.post.mockResolvedValueOnce({ data: { workerId: "sess-grok" } });

		render(<Wrap><TaskComposer projectId="proj-1" onCreated={vi.fn()} /></Wrap>);

		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("grok-4.6");
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
		fireEvent.change(task(), { target: { value: "Do the thing" } });
		fireEvent.click(screen.getByText("Start task"));

		await waitFor(() => expect(h.post).toHaveBeenCalledWith(
			"/api/v1/orchestrators/delegate",
			expect.objectContaining({ body: expect.objectContaining({ agent: "grok", mode: "tui" }) }),
		));
	});

	it("preserves Codex effort when retrying in Terminal UI", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path === "/api/v1/settings") {
				return { data: { defaultSessionMode: "chat", chatHarnesses: ["codex"] } };
			}
			if (path.includes("/models")) {
				return {
					data: {
						agent: "codex",
						selectionMode: "catalog",
						models: [{
							id: "gpt-test",
							label: "GPT Test",
							isDefault: true,
							efforts: ["low", "high"],
						}],
						allowCustom: true,
					},
				};
			}
			return { data: { status: "ok", project: { agent: "codex", config: {} } } };
		});
		h.post
			.mockResolvedValueOnce({ error: { code: "CHAT_DRIVER_UNAVAILABLE" } })
			.mockResolvedValueOnce({ data: { workerId: "sess-tui" } });
		const onCreated = vi.fn();

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={onCreated} />
			</Wrap>,
		);
		await userEvent.click(await screen.findByRole("button", { name: "Effort" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "High" }));
		fireEvent.change(task(), { target: { value: "Do the thing" } });
		await waitForTaskReady();
		fireEvent.click(screen.getByText("Start task"));

		const fallback = await screen.findByRole("button", { name: "Create as Terminal UI" });
		fireEvent.click(fallback);
		await waitFor(() => expect(onCreated).toHaveBeenCalledWith("sess-tui"));
		expect(h.post).toHaveBeenLastCalledWith(
			"/api/v1/orchestrators/delegate",
			expect.objectContaining({ body: expect.objectContaining({ effort: "high", mode: "tui" }) }),
		);
	});

	it("offers an explicit approval-less retry from structured capability details", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return { data: { agent: "cursor", selectionMode: "text", models: [], allowCustom: true } };
			}
			return { data: { status: "ok", project: { agent: "cursor", config: {} } } };
		});
		h.post
			.mockResolvedValueOnce({
				error: {
					code: "SESSION_MODE_UNSUPPORTED",
					message: "This provider cannot satisfy the selected approval policy",
					details: {
						missingCapabilities: ["approvals"],
						allowedApprovalModes: ["bypass-permissions"],
					},
				},
			})
			.mockResolvedValueOnce({ data: { workerId: "sess-pi" } });
		const onCreated = vi.fn();

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={onCreated} />
			</Wrap>,
		);
		await waitFor(() => expect(screen.getByTestId("agent-field")).toHaveAttribute("data-value", "cursor"));
		fireEvent.change(task(), { target: { value: "Use approval-less Chat" } });
		fireEvent.click(screen.getByText("Start task"));

		const fallback = await screen.findByRole("button", { name: "Start without approvals" });
		fireEvent.click(fallback);
		await waitFor(() => expect(onCreated).toHaveBeenCalledWith("sess-pi"));
		expect(h.post).toHaveBeenLastCalledWith(
			"/api/v1/orchestrators/delegate",
			expect.objectContaining({
				body: expect.objectContaining({ approvalMode: "bypass-permissions" }),
			}),
		);
		expect(h.post.mock.calls[1][1].body).not.toHaveProperty("mode");
	});

	it("starts a standalone Unreal task in Chat after approval-less retry", async () => {
		h.agentCatalog = { agents: [agentReadiness("unreal-agent", "Unreal Agent")] };
		h.get.mockImplementation(async (path: string) => {
			if (path === "/api/v1/settings") {
				return { data: { defaultSessionMode: "tui", chatHarnesses: ["unreal-agent"] } };
			}
			if (path.includes("/models")) {
				return { data: { agent: "unreal-agent", selectionMode: "text", models: [], allowCustom: true } };
			}
			return { data: { status: "ok", project: { config: {} } } };
		});
		h.post
			.mockResolvedValueOnce({
				error: {
					code: "SESSION_MODE_UNSUPPORTED",
					message: "This provider cannot satisfy the selected approval policy",
					details: { missingCapabilities: ["approvals"], allowedApprovalModes: ["bypass-permissions"] },
				},
			})
			.mockResolvedValueOnce({ data: { session: { id: "sess-unreal" } } });
		const onCreated = vi.fn();

		render(<Wrap><TaskComposer projectId="__standalone__" onCreated={onCreated} /></Wrap>);
		await waitFor(() => expect(screen.getByTestId("agent-field")).toHaveAttribute("data-value", "unreal-agent"));
		fireEvent.change(task(), { target: { value: "Say hello" } });
		fireEvent.click(startTask());
		fireEvent.click(await screen.findByRole("button", { name: "Start without approvals" }));

		await waitFor(() => expect(onCreated).toHaveBeenCalledWith("sess-unreal"));
		expect(h.post.mock.calls[0][1].body).toMatchObject({ harness: "unreal-agent", mode: "chat" });
		expect(h.post.mock.calls[1][1].body).toMatchObject({
			harness: "unreal-agent", mode: "chat", approvalMode: "bypass-permissions",
		});
	});

	it("reports dirty then clears it on unmount", () => {
		const onDirtyChange = vi.fn();
		const { unmount } = render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} onDirtyChange={onDirtyChange} />
			</Wrap>,
		);
		fireEvent.change(task(), { target: { value: "T" } });
		expect(onDirtyChange).toHaveBeenLastCalledWith(true);
		unmount();
		expect(onDirtyChange).toHaveBeenLastCalledWith(false);
	});

	it("preselects the project worker agent and spawns with it", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return { data: { agent: "codex", selectionMode: "text", models: [], allowCustom: true } };
			}
			return {
				data: { status: "ok", project: { agent: "claude-code", config: { worker: { agent: "codex" } } } },
			};
		});
		h.post.mockResolvedValueOnce({ data: { workerId: "sess-3" } });

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		await waitFor(() => expect(screen.getByTestId("agent-field")).toHaveAttribute("data-value", "codex"));

		fireEvent.change(task(), { target: { value: "Ship it" } });
		fireEvent.click(screen.getByText("Start task"));

		await waitFor(() =>
			expect(h.post).toHaveBeenCalledWith(
				"/api/v1/orchestrators/delegate",
				expect.objectContaining({ body: expect.objectContaining({ agent: "codex" }) }),
			),
		);
	});

	it("renders a known default agent without an empty intermediate selection", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return {
					data: {
						agent: "codex",
						selectionMode: "text",
						models: [{ id: "gpt-5.6-sol", label: "GPT-5.6 Sol", isDefault: true }],
						allowCustom: true,
					},
				};
			}
			return { data: { status: "ok", project: { agent: "codex", config: {} } } };
		});
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		queryClient.setQueryData(["project", "proj-1"], { agent: "codex", config: {} });

		render(
			<QueryClientProvider client={queryClient}>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</QueryClientProvider>,
		);

		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("GPT-5.6 Sol");
		expect(h.agentValues).not.toContain("");
	});

	it("falls back to the global default agent when the project sets no worker agent", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return { data: { agent: "claude-code", selectionMode: "text", models: [], allowCustom: true } };
			}
			return { data: { status: "ok", project: { agent: "claude-code", config: {} } } };
		});

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		await waitFor(() => expect(screen.getByTestId("agent-field")).toHaveAttribute("data-value", "claude-code"));
	});

	it("exposes effort for any harness whose selected model advertises it", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return {
					data: {
						agent: "claude-code",
						selectionMode: "catalog",
						models: [{ id: "sonnet", label: "Sonnet", isDefault: true, efforts: ["low", "high"] }],
						allowCustom: true,
					},
				};
			}
			return { data: { status: "ok", project: { agent: "claude-code", config: {} } } };
		});
		h.post.mockResolvedValueOnce({ data: { workerId: "claude-worker" } });

		render(<Wrap><TaskComposer projectId="proj-1" onCreated={vi.fn()} /></Wrap>);

		await waitFor(() => expect(screen.getByTestId("agent-field")).toHaveAttribute("data-value", "claude-code"));
		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("Sonnet");
		expect(await screen.findByRole("button", { name: "Effort" })).toBeInTheDocument();
	});

	it("preselects the agent's default model when the project configures none", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return {
					data: {
						agent: "codex",
						selectionMode: "text",
						models: [
							{ id: "gpt-5", label: "GPT-5" },
							{ id: "gpt-5-codex", label: "GPT-5 Codex", isDefault: true },
						],
						allowCustom: true,
					},
				};
			}
			return { data: { status: "ok", project: { agent: "codex", config: {} } } };
		});

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("GPT-5 Codex");
	});

	it("clears a stale model while the newly selected agent catalog resolves", async () => {
		let resolveClaudeCatalog!: (value: {
			data: {
				agent: string;
				selectionMode: "text";
				models: Array<{ id: string; label: string; isDefault: boolean }>;
				allowCustom: boolean;
			};
		}) => void;
		h.get.mockImplementation(async (path: string, request?: { params?: { path?: { agent?: string } } }) => {
			if (path.includes("/models")) {
				if (request?.params?.path?.agent === "claude-code") {
					return new Promise((resolve) => {
						resolveClaudeCatalog = resolve;
					});
				}
				return {
					data: {
						agent: "codex",
						selectionMode: "text",
						models: [{ id: "gpt-5.6-sol", label: "GPT-5.6 Sol", isDefault: true }],
						allowCustom: true,
					},
				};
			}
			return { data: { status: "ok", project: { agent: "codex", config: {} } } };
		});

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("GPT-5.6 Sol");
		fireEvent.click(screen.getByTestId("agent-field"));

		expect(screen.getByLabelText("Model")).not.toHaveTextContent("GPT-5.6 Sol");
		expect(screen.getByRole("status", { name: "Loading models…" })).toBeInTheDocument();

		await act(async () => {
			resolveClaudeCatalog({
				data: {
					agent: "claude-code",
					selectionMode: "text",
					models: [{ id: "opus[1m]", label: "opus[1m]", isDefault: true }],
					allowCustom: true,
				},
			});
		});
		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("opus[1m]");
	});

	it("preselects the first catalog model when none is marked default", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return {
					data: {
						agent: "codex",
						selectionMode: "catalog",
						models: [{ id: "gpt-5", label: "GPT-5" }],
						allowCustom: true,
					},
				};
			}
			return { data: { status: "ok", project: { agent: "codex", config: {} } } };
		});

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		const picker = await screen.findByRole("button", { name: "Model" });
		expect(picker).toHaveTextContent("GPT-5");
		expect(picker).not.toHaveTextContent("Use codex's default");

		await userEvent.click(picker);
		expect(screen.queryByRole("menuitem", { name: "Use codex's default" })).not.toBeInTheDocument();
		expect(await screen.findByRole("menuitem", { name: "GPT-5" })).toBeInTheDocument();
	});

	it("spawns with the project worker model even when the user never opens the picker", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return {
					data: {
						agent: "codex",
						selectionMode: "catalog",
						models: [
							{ id: "gpt-5", label: "GPT-5" },
							{ id: "gpt-5-codex", label: "GPT-5 Codex", isDefault: true },
						],
						allowCustom: true,
						refreshRecommended: false,
					},
				};
			}
			return {
				data: {
					status: "ok",
					project: {
						agent: "codex",
						config: { worker: { agent: "codex", agentConfig: { model: "gpt-5" } } },
					},
				},
			};
		});
		h.post.mockResolvedValueOnce({ data: { workerId: "sess-default-model" } });

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		expect(await screen.findByRole("button", { name: "Model" })).toHaveTextContent("GPT-5");
		fireEvent.change(task(), { target: { value: "Use project default model" } });
		fireEvent.click(screen.getByText("Start task"));

		await waitFor(() =>
			expect(h.post).toHaveBeenCalledWith(
				"/api/v1/orchestrators/delegate",
				expect.objectContaining({
					body: expect.objectContaining({ agent: "codex", model: "gpt-5" }),
				}),
			),
		);
	});

	it("forwards model refresh metadata to the task picker", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return {
					data: {
						agent: "codex",
						selectionMode: "catalog",
						models: [{ id: "gpt-5", label: "GPT-5", isDefault: true }],
						allowCustom: true,
						lastSuccessAt: "2026-09-23T08:00:00Z",
						refreshState: "error",
						refreshError: "Provider temporarily unavailable",
						retryAt: "2026-09-23T08:05:00Z",
					},
				};
			}
			return { data: { status: "ok", project: { agent: "codex", config: {} } } };
		});

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		await userEvent.click(await screen.findByRole("button", { name: "Model" }));
		expect(await screen.findByTitle("Provider temporarily unavailable")).toBeInTheDocument();
	});

	it("does not render free text when models must be configured in the agent", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return {
					data: {
						agentId: "opencode",
						selectionMode: "catalog",
						models: [],
						customModelEntry: "configured",
						allowCustom: false,
					},
				};
			}
			return { data: { status: "ok", project: { agent: "opencode", config: {} } } };
		});

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		const picker = await screen.findByRole("button", { name: "Model" });
		expect(screen.queryByRole("textbox", { name: "Model" })).not.toBeInTheDocument();
		await userEvent.click(picker);
		expect(screen.getByText("Configure the model in opencode, then refresh.")).toBeInTheDocument();
	});

	it("uses the project worker model as the new task model default", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return {
					data: {
						agent: "codex",
						selectionMode: "text",
						models: [],
						allowCustom: true,
						refreshRecommended: false,
					},
				};
			}
			return {
				data: {
					status: "ok",
					project: {
						config: { worker: { agent: "codex", agentConfig: { model: "gpt-5" } } },
					},
				},
			};
		});
		h.post.mockResolvedValueOnce({ data: { workerId: "sess-2" } });

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		const model = await screen.findByRole("button", { name: "Model" });
		await userEvent.click(model);
		await userEvent.type(screen.getByRole("searchbox", { name: "Search model" }), "gpt-5.1");
		await userEvent.click(screen.getByRole("menuitem", { name: "Use “gpt-5.1” as a custom model" }));
		fireEvent.change(task(), { target: { value: "Use the selected model" } });
		fireEvent.click(screen.getByText("Start task"));

		await waitFor(() =>
			expect(h.post).toHaveBeenCalledWith(
				"/api/v1/orchestrators/delegate",
				expect.objectContaining({
					body: expect.objectContaining({ model: "gpt-5.1" }),
				}),
			),
		);
	});

	it("inherits worker effort visually but sends only explicit task overrides", async () => {
		h.get.mockImplementation(async (path: string) => {
			if (path.includes("/models")) {
				return {
					data: {
						agent: "codex",
						selectionMode: "catalog",
						models: [{
							id: "gpt-test",
							label: "GPT Test",
							isDefault: true,
							efforts: ["low", "high"],
						}],
						allowCustom: true,
						refreshRecommended: false,
					},
				};
			}
			return {
				data: { status: "ok", project: { config: { worker: { agent: "codex", agentConfig: {
					model: "gpt-test", effort: "high",
				} } } } },
			};
		});
		h.post.mockResolvedValue({ data: { workerId: "sess-tuned" } });

		render(<Wrap><TaskComposer projectId="proj-1" onCreated={vi.fn()} /></Wrap>);
		const picker = await screen.findByRole("button", { name: "Model" });
		expect(picker).toHaveTextContent("GPT Test");
		const effortPicker = await screen.findByRole("button", { name: "Effort" });
		expect(effortPicker).toHaveTextContent("High");

		fireEvent.click(screen.getByText("Start task"));
		await waitFor(() => expect(h.post).toHaveBeenCalledTimes(1));
		expect(h.post.mock.calls[0][1].body).not.toHaveProperty("effort");

		await userEvent.click(effortPicker);
		await userEvent.click(await screen.findByRole("menuitem", { name: "Low" }));
		fireEvent.click(screen.getByText("Start task"));
		await waitFor(() => expect(h.post).toHaveBeenCalledTimes(2));
		expect(h.post.mock.calls[1][1].body).toEqual(expect.objectContaining({ effort: "low" }));

		await userEvent.click(effortPicker);
		await userEvent.click(await screen.findByRole("menuitem", { name: "Default" }));
		fireEvent.click(screen.getByText("Start task"));
		await waitFor(() => expect(h.post).toHaveBeenCalledTimes(3));
		expect(h.post.mock.calls[2][1].body).toEqual(expect.objectContaining({ effort: "" }));
	});
});

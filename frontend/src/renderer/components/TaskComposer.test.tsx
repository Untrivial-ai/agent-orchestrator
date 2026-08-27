import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({
	get: vi.fn(),
	post: vi.fn(),
	capture: vi.fn(),
	agentValues: [] as string[],
}));

vi.mock("../hooks/useAgentsQuery", () => ({
	agentsQueryKey: ["agents"],
	agentsQueryOptions: { queryKey: ["agents"], queryFn: async () => ({}) },
	refreshAgents: vi.fn(),
	refreshAgentsIfStale: vi.fn(async () => undefined),
}));

vi.mock("./CreateProjectAgentSheet", () => ({
	RequiredAgentField: ({
		value,
		onChange,
		triggerClassName,
	}: {
		value: string;
		onChange: (value: string) => void;
		triggerClassName?: string;
	}) => {
		h.agentValues.push(value);
		return (
			<button
				type="button"
				aria-label="Agent"
				className={triggerClassName}
				data-testid="agent-field"
				data-value={value}
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

function Wrap({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

const task = () => screen.getByRole("textbox", { name: "Task" });

beforeEach(() => {
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
	vi.unstubAllGlobals();
	h.agentValues.length = 0;
});

describe("TaskComposer", () => {
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
		expect(screen.getByRole("button", { name: "Start task" })).toBeEnabled();
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

	it("keeps agent and model in equal stable toolbar tracks", () => {
		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={vi.fn()} />
			</Wrap>,
		);

		const runControls = screen.getByRole("group", { name: "Runs with" });
		expect(runControls).toHaveClass("composer-run-controls");
		expect(runControls.closest(".composer-toolbar")).not.toBeNull();
		expect(runControls.querySelectorAll(".composer-toolbar-slot")).toHaveLength(2);
		expect(screen.getByTestId("agent-field").closest(".composer-toolbar-slot")).not.toBeNull();
		expect(screen.getByLabelText("Model").closest(".composer-toolbar-slot")).not.toBeNull();
		expect(runControls.querySelector(".composer-toolbar-divider")).not.toBeNull();
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
		fireEvent.click(screen.getByText("Start task"));

		await waitFor(() => expect(h.post).toHaveBeenCalledTimes(1));
		const body = h.post.mock.calls[0][1].body as {
			attachments?: Array<{ mimeType: string; data: string }>;
		};
		expect(body.attachments).toHaveLength(1);
		expect(body.attachments?.[0].mimeType).toBe("text/plain");
		expect(body.attachments?.[0].data.length).toBeGreaterThan(0);
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
		fireEvent.click(screen.getByText("Start task"));

		expect(h.post).not.toHaveBeenCalled();

		await act(async () => finishRead());
		await waitFor(() => expect(h.post).toHaveBeenCalledTimes(1));
		expect(h.post.mock.calls[0][1].body).toMatchObject({
			attachments: [{ mimeType: "text/plain", data: "AQID" }],
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
		fireEvent.click(screen.getByText("Start task"));

		await waitFor(() => expect(screen.getByText("nope")).toBeInTheDocument());
		expect(onSubmittingChange).toHaveBeenLastCalledWith(false);
	});

	it("offers an explicit Terminal UI retry after Chat preflight fails", async () => {
		h.post
			.mockResolvedValueOnce({ error: { code: "CHAT_DRIVER_UNAVAILABLE" } })
			.mockResolvedValueOnce({ data: { workerId: "sess-tui" } });
		const onCreated = vi.fn();

		render(
			<Wrap>
				<TaskComposer projectId="proj-1" onCreated={onCreated} />
			</Wrap>,
		);
		fireEvent.change(task(), { target: { value: "Do the thing" } });
		fireEvent.click(screen.getByText("Start task"));

		const fallback = await screen.findByRole("button", { name: "Create as Terminal UI" });
		fireEvent.click(fallback);
		await waitFor(() => expect(onCreated).toHaveBeenCalledWith("sess-tui"));
		expect(h.post).toHaveBeenLastCalledWith(
			"/api/v1/orchestrators/delegate",
			expect.objectContaining({ body: expect.objectContaining({ mode: "tui" }) }),
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

		expect(await screen.findByDisplayValue("gpt-5.6-sol")).toBeInTheDocument();
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

		expect(await screen.findByDisplayValue("gpt-5-codex")).toBeInTheDocument();
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

		expect(await screen.findByDisplayValue("gpt-5.6-sol")).toBeInTheDocument();
		fireEvent.click(screen.getByTestId("agent-field"));

		expect(screen.queryByDisplayValue("gpt-5.6-sol")).not.toBeInTheDocument();
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
		expect(await screen.findByDisplayValue("opus[1m]")).toBeInTheDocument();
	});

	it("shows the same no-override label on the trigger and in the menu", async () => {
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
		expect(picker).toHaveTextContent("Use codex's default");

		await userEvent.click(picker);
		expect(await screen.findByRole("menuitem", { name: "Use codex's default" })).toBeInTheDocument();
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

		const model = await screen.findByDisplayValue("gpt-5");
		fireEvent.change(model, { target: { value: "gpt-5.1" } });
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
});

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createElement, type ComponentProps } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
	TaskComposerView,
	type TaskComposerViewProps,
} from "./TaskComposerView";

const useReducedMotionMock = vi.hoisted(() => vi.fn(() => false));
const lastAttachmentTransition = vi.hoisted(() => ({
	current: undefined as { duration?: number } | undefined,
}));

vi.mock("motion/react", async (importOriginal) => {
	const actual = await importOriginal<typeof import("motion/react")>();
	function MotionDiv(props: ComponentProps<typeof actual.motion.div>) {
		lastAttachmentTransition.current = props.transition as { duration?: number } | undefined;
		return createElement(actual.motion.div, props);
	}
	return {
		...actual,
		useReducedMotion: useReducedMotionMock,
		motion: { ...actual.motion, div: MotionDiv },
	};
});

function viewProps(overrides: Partial<TaskComposerViewProps> = {}): TaskComposerViewProps {
	return {
		canSubmit: true,
		initialPrompt: "",
		onPromptChange: vi.fn(),
		labels: {
			addFile: "Add file",
			fallbackAction: "Create as Terminal UI",
			removeFile: (name) => `Remove ${name}`,
			runsWith: "Runs with",
			start: "Start task",
			starting: "Starting...",
			task: "Task",
			taskPlaceholder: "Describe the task (optional)…",
		},
		agent: {
			value: "codex",
			label: "Agent",
			placeholder: "Select agent",
			disabled: false,
			agents: [{
				id: "codex",
				label: "Codex",
				installation: { state: "installed", freshness: "fresh" },
				authentication: { state: "authorized", freshness: "fresh" },
				effectiveReadiness: "ready",
				usageCount: 0,
			}],
			onChange: vi.fn(),
		},
		model: {
			agentId: "codex",
			agentLabel: "Codex",
			projectId: "project-1",
			disabled: false,
			value: "gpt-5",
			mode: "",
			catalog: {
				allowCustom: true,
				customModelEntry: "direct",
				models: [{ id: "gpt-5", label: "GPT-5" }],
				selectionMode: "catalog",
			},
			fetching: false,
			loading: false,
			onModelChange: vi.fn(),
			onModeChange: vi.fn(),
		},
		attachments: {
			items: [],
			onAddFiles: vi.fn(),
			onRemove: vi.fn(),
		},
		submission: {
			showFallbackAction: false,
			isSubmitting: false,
			onFallbackAction: vi.fn(),
			onSubmit: vi.fn(),
		},
		renderAgentControl: (control) => (
			<button type="button" aria-label={control.label} onClick={() => control.onChange("claude-code")}>
				{control.value}
			</button>
		),
		renderModelControl: (control) => (
			<input
				aria-label="Model"
				value={control.value}
				onChange={(event) => control.onModelChange(event.target.value)}
			/>
		),
		...overrides,
	};
}

describe("TaskComposerView", () => {
	beforeEach(() => {
		useReducedMotionMock.mockReturnValue(false);
		lastAttachmentTransition.current = undefined;
	});

	it("renders controlled project, agent, model, and prompt state", () => {
		const props = viewProps();
		render(<TaskComposerView {...props} />);

		const prompt = screen.getByRole("textbox", { name: "Task" });
		expect(prompt).toHaveAttribute("placeholder", "Describe the task (optional)…");
		fireEvent.change(prompt, { target: { value: "Investigate the failure" } });
		expect(props.onPromptChange).toHaveBeenCalledWith("Investigate the failure");

		fireEvent.click(screen.getByRole("button", { name: "Agent" }));
		expect(props.agent.onChange).toHaveBeenCalledWith("claude-code");
		fireEvent.change(screen.getByRole("textbox", { name: "Model" }), { target: { value: "gpt-5.1" } });
		expect(props.model.onModelChange).toHaveBeenCalledWith("gpt-5.1");
		expect(screen.getByRole("group", { name: "Runs with" })).toHaveClass("composer-run-controls");
	});

	it("claims the caret when asked to autofocus, and reclaims it from a surface that steals it", async () => {
		render(<TaskComposerView {...viewProps({ autoFocusPrompt: true })} />);
		const prompt = screen.getByRole("textbox", { name: "Task" });
		expect(document.activeElement).toBe(prompt);

		// A Radix menu closing behind the dialog pulls focus back to itself for a
		// frame. The prompt has to win that race or the user gets no caret.
		const thief = document.createElement("button");
		document.body.append(thief);
		thief.focus();
		expect(document.activeElement).toBe(thief);

		await waitFor(() => expect(document.activeElement).toBe(prompt));
		thief.remove();
	});

	it("leaves the caret alone when no autofocus was asked for", () => {
		const before = document.createElement("button");
		document.body.append(before);
		before.focus();

		render(<TaskComposerView {...viewProps()} />);

		expect(document.activeElement).toBe(before);
		before.remove();
	});

	it("keeps the surrounding controls stable while typing", () => {
		const renderAgentControl = vi.fn((control) => <button type="button">{control.value}</button>);
		render(<TaskComposerView {...viewProps({ renderAgentControl })} />);
		renderAgentControl.mockClear();

		fireEvent.change(screen.getByRole("textbox", { name: "Task" }), { target: { value: "Fast draft" } });

		expect(renderAgentControl).not.toHaveBeenCalled();
	});

	it("submits on the button or unmodified Enter and respects project availability", () => {
		const props = viewProps();
		const { rerender } = render(<TaskComposerView {...props} />);

		fireEvent.click(screen.getByRole("button", { name: "Start task" }));
		expect(props.submission.onSubmit).toHaveBeenCalledTimes(1);

		fireEvent.keyDown(screen.getByRole("textbox", { name: "Task" }), {
			key: "Enter",
			shiftKey: false,
			altKey: false,
		});
		expect(props.submission.onSubmit).toHaveBeenCalledTimes(2);

		rerender(<TaskComposerView {...viewProps({ canSubmit: false })} />);
		expect(screen.getByRole("button", { name: "Start task" })).toBeDisabled();
	});

	it("forwards picked, pasted, dropped, and removed attachments", () => {
		const onAddFiles = vi.fn();
		const onRemove = vi.fn();
		const file = new File(["notes"], "notes.txt", { type: "text/plain" });
		const { container } = render(
			<TaskComposerView
				{...viewProps({
					attachments: {
						items: [{ id: "attachment-1", name: "notes.txt" }],
						onAddFiles,
						onRemove,
					},
				})}
			/>,
		);

		fireEvent.click(screen.getByRole("button", { name: "Remove notes.txt" }));
		expect(onRemove).toHaveBeenCalledWith("attachment-1");

		const input = container.querySelector('input[type="file"]') as HTMLInputElement;
		fireEvent.change(input, { target: { files: [file] } });
		expect(onAddFiles).toHaveBeenCalledWith([file]);

		fireEvent.paste(screen.getByRole("textbox", { name: "Task" }), {
			clipboardData: { files: [file] },
		});
		expect(onAddFiles).toHaveBeenLastCalledWith([file]);

		fireEvent.drop(container.querySelector("form") as HTMLFormElement, {
			dataTransfer: { files: [file] },
		});
		expect(onAddFiles).toHaveBeenLastCalledWith([file]);
	});

	it("locks attachment editing while submitting and restores it for retry", () => {
		const onAddFiles = vi.fn();
		const onRemove = vi.fn();
		const file = new File(["notes"], "notes.txt", { type: "text/plain" });
		const attachments = {
			items: [{ id: "attachment-1", name: "notes.txt" }],
			onAddFiles,
			onRemove,
		};
		const { container, rerender } = render(
			<TaskComposerView
				{...viewProps({
					attachments,
					submission: {
						showFallbackAction: false,
						isSubmitting: true,
						onFallbackAction: vi.fn(),
						onSubmit: vi.fn(),
					},
				})}
			/>,
		);

		const form = container.querySelector("form") as HTMLFormElement;
		const input = container.querySelector('input[type="file"]') as HTMLInputElement;
		const addFile = screen.getByRole("button", { name: "Add file" });
		const removeFile = screen.getByRole("button", { name: "Remove notes.txt" });
		expect(input).toBeDisabled();
		expect(addFile).toBeDisabled();
		expect(removeFile).toBeDisabled();

		fireEvent.change(input, { target: { files: [file] } });
		fireEvent.paste(screen.getByRole("textbox", { name: "Task" }), {
			clipboardData: { files: [file] },
		});
		fireEvent.drop(form, { dataTransfer: { files: [file] } });
		fireEvent.click(removeFile);
		expect(onAddFiles).not.toHaveBeenCalled();
		expect(onRemove).not.toHaveBeenCalled();

		rerender(<TaskComposerView {...viewProps({ attachments })} />);
		expect(input).toBeEnabled();
		expect(addFile).toBeEnabled();
		expect(removeFile).toBeEnabled();

		fireEvent.change(input, { target: { files: [file] } });
		fireEvent.click(removeFile);
		expect(onAddFiles).toHaveBeenCalledWith([file]);
		expect(onRemove).toHaveBeenCalledWith("attachment-1");
	});

	it("keeps attachment previews hidden until a file is selected", () => {
		const { container, rerender } = render(<TaskComposerView {...viewProps()} />);

		const addFile = screen.getByRole("button", { name: "Add file" });
		expect(addFile.closest(".composer-toolbar")).not.toBeNull();
		expect(container.querySelector("ul")).toBeNull();

		rerender(
			<TaskComposerView
				{...viewProps({
					attachments: {
						items: [{ id: "attachment-1", name: "notes.txt" }],
						onAddFiles: vi.fn(),
						onRemove: vi.fn(),
					},
				})}
			/>,
		);

		expect(container.querySelector("ul")).not.toBeNull();
	});

	it("makes the attachment transition instant when reduced motion is preferred", () => {
		useReducedMotionMock.mockReturnValue(true);
		render(
			<TaskComposerView
				{...viewProps({
					attachments: {
						items: [{ id: "attachment-1", name: "notes.txt" }],
						onAddFiles: vi.fn(),
						onRemove: vi.fn(),
					},
				})}
			/>,
		);

		expect(lastAttachmentTransition.current).toEqual({ duration: 0 });
	});

	it("shows attachment and submission errors with a fallback action", () => {
		const onFallbackAction = vi.fn();
		render(
			<TaskComposerView
				{...viewProps({
					attachments: {
						items: [],
						error: "File is too large",
						onAddFiles: vi.fn(),
						onRemove: vi.fn(),
					},
					submission: {
						showFallbackAction: true,
						error: "Chat unavailable",
						isSubmitting: false,
						modelWarning: "Hidden while the submission error is visible",
						onFallbackAction,
						onSubmit: vi.fn(),
					},
				})}
			/>,
		);

		expect(screen.getByText("File is too large")).toBeInTheDocument();
		expect(screen.getByText("Chat unavailable")).toBeInTheDocument();
		expect(screen.queryByText("Hidden while the submission error is visible")).not.toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Create as Terminal UI" }));
		expect(onFallbackAction).toHaveBeenCalledOnce();
	});
});

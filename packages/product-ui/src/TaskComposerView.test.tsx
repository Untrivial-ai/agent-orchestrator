import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import {
	TaskComposerView,
	type TaskComposerViewProps,
} from "./TaskComposerView";

function viewProps(overrides: Partial<TaskComposerViewProps> = {}): TaskComposerViewProps {
	return {
		canSubmit: true,
		prompt: "",
		onPromptChange: vi.fn(),
		labels: {
			addFile: "Add file",
			createAsTui: "Create as Terminal UI",
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
			supported: [{ id: "codex", label: "Codex" }],
			onChange: vi.fn(),
		},
		model: {
			agentId: "codex",
			agentLabel: "Codex",
			projectId: "project-1",
			value: "gpt-5",
			mode: "",
			catalog: {
				allowCustom: true,
				models: [{ id: "gpt-5", label: "GPT-5" }],
				selectionMode: "catalog",
			},
			fetching: false,
			loading: false,
			refreshing: false,
			onModelChange: vi.fn(),
			onModeChange: vi.fn(),
			onRefresh: vi.fn(),
		},
		attachments: {
			items: [],
			onAddFiles: vi.fn(),
			onRemove: vi.fn(),
		},
		submission: {
			canCreateAsTui: false,
			isSubmitting: false,
			onSubmit: vi.fn(),
			onSubmitAsTui: vi.fn(),
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

	it("shows attachment and submission errors with the TUI retry", () => {
		const onSubmitAsTui = vi.fn();
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
						canCreateAsTui: true,
						error: "Chat unavailable",
						isSubmitting: false,
						modelWarning: "Hidden while the submission error is visible",
						onSubmit: vi.fn(),
						onSubmitAsTui,
					},
				})}
			/>,
		);

		expect(screen.getByText("File is too large")).toBeInTheDocument();
		expect(screen.getByText("Chat unavailable")).toBeInTheDocument();
		expect(screen.queryByText("Hidden while the submission error is visible")).not.toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Create as Terminal UI" }));
		expect(onSubmitAsTui).toHaveBeenCalledOnce();
	});
});

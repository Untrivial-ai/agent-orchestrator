import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { DiffSelectionMenu, type DiffSelectionMenuProps } from "./DiffSelectionMenu";
import type { DiffSelectionLine } from "../../shared/diff-selection";

const postMock = vi.hoisted(() => vi.fn());

vi.mock("../lib/api-client", () => ({
	apiClient: { POST: postMock },
	apiErrorMessage: (error: unknown, fallback = "Request failed") =>
		typeof error === "object" && error !== null && "message" in error
			? String((error as { message: unknown }).message)
			: fallback,
}));

const writeTextMock = vi.hoisted(() => vi.fn());

const SAMPLE_LINES: DiffSelectionLine[] = [
	{ kind: "context", oldNo: 10, newNo: 10, text: "function foo() {" },
	{ kind: "add", oldNo: null, newNo: 11, text: "  return 42;" },
];

function renderMenu(overrides: Partial<DiffSelectionMenuProps> = {}) {
	const onOpenChange = overrides.onOpenChange ?? vi.fn();
	const props: DiffSelectionMenuProps = {
		sessionId: "sess-1",
		filePath: "src/app.ts",
		lines: SAMPLE_LINES,
		selectedText: "  return 42;",
		open: true,
		position: { x: 10, y: 20 },
		onOpenChange,
		...overrides,
	};
	const view = render(<DiffSelectionMenu {...props} />);
	return { onOpenChange, props, ...view };
}

describe("DiffSelectionMenu", () => {
	beforeEach(() => {
		postMock.mockReset();
		postMock.mockResolvedValue({ data: {} });
		writeTextMock.mockReset();
		Object.defineProperty(navigator, "clipboard", {
			configurable: true,
			value: { writeText: writeTextMock },
		});
	});

	it("shows Copy, Explain, and Make changes when open", () => {
		renderMenu();

		expect(screen.getByRole("menuitem", { name: "Copy" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Explain" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Make changes" })).toBeInTheDocument();
	});

	it("disables Copy when there is no selected text", () => {
		renderMenu({ selectedText: "" });

		expect(screen.getByRole("menuitem", { name: "Copy" })).toHaveAttribute("data-disabled");
	});

	it("copies the selected text and closes the menu", async () => {
		const { onOpenChange } = renderMenu();

		await userEvent.click(screen.getByRole("menuitem", { name: "Copy" }));

		expect(writeTextMock).toHaveBeenCalledWith("  return 42;");
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(postMock).not.toHaveBeenCalled();
	});

	it("sends the canned Explain instruction and shows a sending -> sent progression", async () => {
		let resolvePost: (value: unknown) => void = () => undefined;
		postMock.mockReturnValueOnce(
			new Promise((resolve) => {
				resolvePost = resolve;
			}),
		);
		const onOpenChange = vi.fn();
		renderMenu({ onOpenChange });

		await userEvent.click(screen.getByRole("menuitem", { name: "Explain" }));

		expect(await screen.findByText("Sending")).toBeInTheDocument();
		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/send", {
			params: { path: { sessionId: "sess-1" } },
			body: { message: expect.stringContaining("Explain what these lines do and why."), userAuthored: true },
		});
		const body = postMock.mock.calls[0][1].body as { message: string };
		expect(body.message).toContain("File: src/app.ts");
		expect(body.message).toContain("return 42;");

		await act(async () => {
			resolvePost({ data: {} });
		});

		expect(await screen.findByText("Sent")).toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalledWith(false);
	});

	it("auto-closes shortly after a successful send", async () => {
		vi.useFakeTimers();
		try {
			let resolvePost: (value: unknown) => void = () => undefined;
			postMock.mockReturnValueOnce(
				new Promise((resolve) => {
					resolvePost = resolve;
				}),
			);
			const onOpenChange = vi.fn();
			renderMenu({ onOpenChange });

			fireEvent.click(screen.getByRole("menuitem", { name: "Explain" }));

			await act(async () => {
				resolvePost({ data: {} });
				await Promise.resolve();
				await Promise.resolve();
			});

			expect(screen.getByText("Sent")).toBeInTheDocument();
			expect(onOpenChange).not.toHaveBeenCalledWith(false);

			act(() => {
				vi.advanceTimersByTime(2_000);
			});

			expect(onOpenChange).toHaveBeenCalledWith(false);
		} finally {
			vi.useRealTimers();
		}
	});

	it("ignores a send result from a previous menu opening", async () => {
		vi.useFakeTimers();
		try {
			let resolvePost: (value: unknown) => void = () => undefined;
			postMock.mockReturnValueOnce(
				new Promise((resolve) => {
					resolvePost = resolve;
				}),
			);
			const onOpenChange = vi.fn();
			const { rerender, props } = renderMenu({ onOpenChange });

			fireEvent.click(screen.getByRole("menuitem", { name: "Explain" }));
			rerender(<DiffSelectionMenu {...props} onOpenChange={onOpenChange} open={false} />);
			rerender(<DiffSelectionMenu {...props} filePath="src/new.ts" onOpenChange={onOpenChange} open />);

			await act(async () => {
				resolvePost({ data: {} });
				await Promise.resolve();
			});

			expect(screen.queryByText("Sent")).not.toBeInTheDocument();
			act(() => vi.advanceTimersByTime(2_000));
			expect(onOpenChange).not.toHaveBeenCalledWith(false);
		} finally {
			vi.useRealTimers();
		}
	});

	it("clears the pending sent auto-close timer and resets status when switching to Make changes", async () => {
		vi.useFakeTimers();
		try {
			postMock.mockResolvedValue({ data: {} });
			const onOpenChange = vi.fn();
			renderMenu({ onOpenChange });

			fireEvent.click(screen.getByRole("menuitem", { name: "Explain" }));

			await act(async () => {
				await Promise.resolve();
				await Promise.resolve();
			});

			expect(screen.getByText("Sent")).toBeInTheDocument();

			fireEvent.click(screen.getByRole("menuitem", { name: "Make changes" }));

			expect(screen.getByRole("textbox", { name: "Describe the change" })).toBeInTheDocument();
			expect(screen.queryByText("Sent")).not.toBeInTheDocument();

			act(() => {
				vi.advanceTimersByTime(2_000);
			});

			expect(onOpenChange).not.toHaveBeenCalledWith(false);
			expect(screen.getByRole("textbox", { name: "Describe the change" })).toBeInTheDocument();
		} finally {
			vi.useRealTimers();
		}
	});

	it("swaps to an input on Make changes and sends the typed instruction on Enter", async () => {
		renderMenu();

		await userEvent.click(screen.getByRole("menuitem", { name: "Make changes" }));
		const input = await screen.findByRole("textbox", { name: "Describe the change" });
		await userEvent.type(input, "Rename this to bar{Enter}");

		expect(await screen.findByText("Sent")).toBeInTheDocument();
		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/send", {
			params: { path: { sessionId: "sess-1" } },
			body: { message: expect.stringContaining("Rename this to bar"), userAuthored: true },
		});
		const body = postMock.mock.calls[0][1].body as { message: string };
		expect(body.message).not.toContain("Explain what these lines do and why.");
	});

	it("does not send on Enter with an empty or whitespace-only input", async () => {
		renderMenu();

		await userEvent.click(screen.getByRole("menuitem", { name: "Make changes" }));
		const input = await screen.findByRole("textbox", { name: "Describe the change" });
		await userEvent.type(input, "   {Enter}");

		expect(postMock).not.toHaveBeenCalled();
	});

	it("returns to the action list on Escape from the input without closing the menu", async () => {
		const { onOpenChange } = renderMenu();

		await userEvent.click(screen.getByRole("menuitem", { name: "Make changes" }));
		const input = await screen.findByRole("textbox", { name: "Describe the change" });
		await userEvent.type(input, "partial text");
		await userEvent.keyboard("{Escape}");

		expect(screen.getByRole("menuitem", { name: "Copy" })).toBeInTheDocument();
		expect(screen.queryByRole("textbox", { name: "Describe the change" })).not.toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalledWith(false);
	});

	it("clears the pending sent auto-close timer and resets status when pressing Escape from the input", async () => {
		vi.useFakeTimers();
		try {
			postMock.mockResolvedValue({ data: {} });
			const onOpenChange = vi.fn();
			renderMenu({ onOpenChange });

			fireEvent.click(screen.getByRole("menuitem", { name: "Make changes" }));
			const input = screen.getByRole("textbox", { name: "Describe the change" });

			fireEvent.change(input, { target: { value: "Rename this to bar" } });
			fireEvent.keyDown(input, { key: "Enter" });

			await act(async () => {
				await Promise.resolve();
				await Promise.resolve();
			});

			expect(screen.getByText("Sent")).toBeInTheDocument();

			fireEvent.keyDown(input, { key: "Escape" });

			expect(screen.getByRole("menuitem", { name: "Copy" })).toBeInTheDocument();
			expect(screen.queryByRole("textbox", { name: "Describe the change" })).not.toBeInTheDocument();
			expect(screen.queryByText("Sent")).not.toBeInTheDocument();

			act(() => {
				vi.advanceTimersByTime(2_000);
			});

			expect(onOpenChange).not.toHaveBeenCalledWith(false);
		} finally {
			vi.useRealTimers();
		}
	});

	it("shows an error status and does not auto-close when the send fails", async () => {
		postMock.mockResolvedValue({ error: { message: "AO daemon is not ready." } });
		const onOpenChange = vi.fn();
		renderMenu({ onOpenChange });

		await userEvent.click(screen.getByRole("menuitem", { name: "Explain" }));

		expect(await screen.findByText("AO daemon is not ready.")).toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalledWith(false);
	});

	it("shows an error status when the send promise rejects", async () => {
		postMock.mockRejectedValue({ message: "Network unreachable." });
		const onOpenChange = vi.fn();
		renderMenu({ onOpenChange });

		await userEvent.click(screen.getByRole("menuitem", { name: "Explain" }));

		expect(await screen.findByText("Network unreachable.")).toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalledWith(false);
	});

	it("clears the stale error text when switching to Make changes after a failed send", async () => {
		postMock.mockResolvedValue({ error: { message: "AO daemon is not ready." } });
		renderMenu();

		await userEvent.click(screen.getByRole("menuitem", { name: "Explain" }));
		expect(await screen.findByText("AO daemon is not ready.")).toBeInTheDocument();

		await userEvent.click(screen.getByRole("menuitem", { name: "Make changes" }));

		expect(screen.getByRole("textbox", { name: "Describe the change" })).toBeInTheDocument();
		expect(screen.queryByText("AO daemon is not ready.")).not.toBeInTheDocument();
	});

	it("clears the stale error text when pressing Escape from the input after a failed send", async () => {
		postMock.mockResolvedValue({ error: { message: "AO daemon is not ready." } });
		renderMenu();

		await userEvent.click(screen.getByRole("menuitem", { name: "Make changes" }));
		const input = await screen.findByRole("textbox", { name: "Describe the change" });
		await userEvent.type(input, "Rename this to bar{Enter}");

		expect(await screen.findByText("AO daemon is not ready.")).toBeInTheDocument();

		await userEvent.keyboard("{Escape}");

		expect(screen.getByRole("menuitem", { name: "Copy" })).toBeInTheDocument();
		expect(screen.queryByText("AO daemon is not ready.")).not.toBeInTheDocument();
	});

	it("resets to the action list when reopened after a prior error", async () => {
		postMock.mockResolvedValue({ error: { message: "AO daemon is not ready." } });
		const onOpenChange = vi.fn();
		const { rerender, props } = renderMenu({ onOpenChange });

		await userEvent.click(screen.getByRole("menuitem", { name: "Explain" }));
		expect(await screen.findByText("AO daemon is not ready.")).toBeInTheDocument();

		rerender(<DiffSelectionMenu {...props} onOpenChange={onOpenChange} open={false} />);
		rerender(<DiffSelectionMenu {...props} onOpenChange={onOpenChange} open />);

		expect(screen.getByRole("menuitem", { name: "Copy" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Explain" })).toBeInTheDocument();
		expect(screen.queryByText("AO daemon is not ready.")).not.toBeInTheDocument();
	});

	it("resets to the action list when reopened after switching to input mode", async () => {
		const onOpenChange = vi.fn();
		const { rerender, props } = renderMenu({ onOpenChange });

		await userEvent.click(screen.getByRole("menuitem", { name: "Make changes" }));
		expect(await screen.findByRole("textbox", { name: "Describe the change" })).toBeInTheDocument();

		rerender(<DiffSelectionMenu {...props} onOpenChange={onOpenChange} open={false} />);
		rerender(<DiffSelectionMenu {...props} onOpenChange={onOpenChange} open />);

		expect(screen.getByRole("menuitem", { name: "Make changes" })).toBeInTheDocument();
		expect(screen.queryByRole("textbox", { name: "Describe the change" })).not.toBeInTheDocument();
	});
});

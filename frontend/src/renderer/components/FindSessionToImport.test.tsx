import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { FindSessionToImport } from "./FindSessionToImport";
const get = vi.hoisted(() => vi.fn());
const post = vi.hoisted(() => vi.fn());
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: get, POST: post },
	apiErrorMessage: (error: { message: string }) => error.message,
}));
const result: components["schemas"]["SessionImportSearchResult"] = {
	id: "one",
	title: "Fix payment retries",
	provider: "codex",
	lastActivity: "2026-09-01T00:00:00Z",
};
const page: components["schemas"]["SessionImportSearchPage"] = {
	results: [result],
	status: { running: false, scanned: 1, updated: 1, errors: [] },
};
const destination: components["schemas"]["SessionImportDestination"] = {
	id: "one",
	title: result.title,
	provider: "codex",
	action: "import",
	path: "/code/app",
	projectId: "app",
	confirmationToken: "confirmed",
};
function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (reason: Error) => void;
	const promise = new Promise<T>((yes, no) => {
		resolve = yes;
		reject = no;
	});
	return { promise, resolve, reject };
}
beforeEach(() => {
	get.mockReset();
	post.mockReset();
	get.mockImplementation((path: string) =>
		Promise.resolve({
			data: path.endsWith("destination") ? destination : page,
		}),
	);
	post.mockResolvedValue({ data: page.status });
});

describe("selected session import", () => {
	it("carries the query, selects by keyboard without importing, and opens saved history after explicit import", async () => {
		const onOpen = vi.fn();
		render(<FindSessionToImport initialQuery="payment" onOpen={onOpen} />);
		expect(screen.getByRole("combobox")).toHaveValue("payment");
		await screen.findByRole("option");
		expect(get).toHaveBeenCalledWith(
			"/api/v1/session-import/search",
			expect.objectContaining({
				params: { query: { query: "payment", limit: 50, cursor: undefined } },
			}),
		);
		fireEvent.keyDown(screen.getByRole("combobox"), { key: "Enter" });
		await screen.findByRole("button", { name: "Import session" });
		expect(
			post.mock.calls.filter(([path]) => path.endsWith("/import")),
		).toHaveLength(0);
		post.mockResolvedValue({
			data: {
				sessionId: "saved",
				projectId: "app",
				alreadyImported: false,
				projectCreated: false,
			},
		});
		fireEvent.click(screen.getByRole("button", { name: "Import session" }));
		await waitFor(() => expect(onOpen).toHaveBeenCalledWith("app", "saved"));
		expect(post).toHaveBeenLastCalledWith(
			"/api/v1/session-import/search/{resultId}/import",
			expect.objectContaining({
				body: {
					confirmationToken: "confirmed",
					addProject: false,
					locateFolder: undefined,
				},
			}),
		);
	});
	it("requires explicit project addition and blocks duplicate clicks", async () => {
		get.mockImplementation((path: string) =>
			Promise.resolve({
				data: path.endsWith("destination")
					? { ...destination, action: "add_project" }
					: page,
			}),
		);
		const pending = deferred<object>();
		const onOpen = vi.fn();
		render(<FindSessionToImport initialQuery="" onOpen={onOpen} />);
		fireEvent.click(await screen.findByRole("option"));
		const button = await screen.findByRole("button", {
			name: "Add project & import session",
		});
		post.mockReturnValue(pending.promise);
		fireEvent.click(button);
		fireEvent.click(button);
		expect(
			post.mock.calls.filter(([path]) => path.endsWith("/import")),
		).toHaveLength(1);
		expect(post).toHaveBeenLastCalledWith(
			expect.anything(),
			expect.objectContaining({
				body: expect.objectContaining({ addProject: true }),
			}),
		);
		await act(async () =>
			pending.resolve({
				data: {
					projectId: "app",
					error: "storage failed",
					projectCreated: true,
					alreadyImported: false,
				},
			}),
		);
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"The project was added and can be reused.",
		);
		expect(onOpen).not.toHaveBeenCalled();
	});
	it("opens an already imported session without a write", async () => {
		get.mockImplementation((path: string) =>
			Promise.resolve({
				data: path.endsWith("destination")
					? { ...destination, action: "open", sessionId: "saved" }
					: page,
			}),
		);
		const onOpen = vi.fn();
		render(<FindSessionToImport initialQuery="" onOpen={onOpen} />);
		fireEvent.click(await screen.findByRole("option"));
		fireEvent.click(await screen.findByRole("button", { name: "Open" }));
		expect(onOpen).toHaveBeenCalledWith("app", "saved");
		expect(
			post.mock.calls.filter(([path]) => path.endsWith("/import")),
		).toHaveLength(0);
	});
	it.each([false, true])(
		"ignores stale search completion (error=%s)",
		async (fails) => {
			const old = deferred<object>();
			get.mockImplementation(
				(_path: string, options: { params: { query: { query: string } } }) =>
					options.params.query.query === "old"
						? old.promise
						: Promise.resolve({ data: page }),
			);
			render(<FindSessionToImport initialQuery="old" onOpen={vi.fn()} />);
			await waitFor(() => expect(get).toHaveBeenCalled());
			fireEvent.change(screen.getByRole("combobox"), {
				target: { value: "payment" },
			});
			await screen.findByRole("option");
			await act(async () => {
				if (fails) old.reject(new Error("stale error"));
				else
					old.resolve({
						data: { ...page, results: [{ ...result, title: "Old result" }] },
					});
			});
			expect(screen.getByRole("option")).toHaveTextContent(
				"Fix payment retries",
			);
			expect(screen.queryByRole("alert")).not.toBeInTheDocument();
		},
	);
	it("shows partial provider errors alongside usable results and restores the search on back", async () => {
		get.mockImplementation((path: string) =>
			Promise.resolve({
				data: path.endsWith("destination")
					? destination
					: {
							...page,
							status: {
								...page.status,
								errors: ["Claude history is unreadable"],
							},
						},
			}),
		);
		render(<FindSessionToImport initialQuery="payment" onOpen={vi.fn()} />);
		fireEvent.click(await screen.findByRole("option"));
		await screen.findByRole("button", { name: "Import session" });
		expect(
			screen.getByText("Claude history is unreadable"),
		).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Back to results" }));
		expect(screen.getByRole("combobox")).toHaveValue("payment");
		expect(screen.getByRole("combobox")).toHaveFocus();
	});
});

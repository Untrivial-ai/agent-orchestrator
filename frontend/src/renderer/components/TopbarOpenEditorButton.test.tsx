import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { TopbarOpenEditorButton } from "./TopbarOpenEditorButton";

const { getMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		if (error instanceof Error) return error.message;
		if (typeof error === "object" && error !== null && "message" in error) {
			return String((error as { message: unknown }).message);
		}
		return fallback;
	},
	hasTrustedApiBaseUrl: () => true,
}));
vi.mock("../lib/telemetry", () => ({ captureRendererEvent: vi.fn() }));

function editorsResponse(editors: { id: string; name: string }[]) {
	return async (path: string) => {
		if (path === "/api/v1/editors") return { data: { editors } };
		return { data: { sessionId: "sess-1", files: [], truncated: false } };
	};
}

function renderButton() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={client}>
			<TopbarOpenEditorButton sessionId="sess-1" projectId="proj-1" />
		</QueryClientProvider>,
	);
}

describe("TopbarOpenEditorButton", () => {
	beforeEach(() => {
		getMock.mockReset();
		postMock.mockReset();
		postMock.mockResolvedValue({ data: { ok: true, editorId: "vscode", file: "src/a.ts", scope: "workspace" } });
	});

	it("stays hidden when no editor is installed", async () => {
		getMock.mockImplementation(editorsResponse([]));
		renderButton();
		await waitFor(() => expect(getMock).toHaveBeenCalled());
		expect(screen.queryByRole("button", { name: /open in/i })).toBeNull();
	});

	it("labels itself with the detected editor and opens with no explicit path", async () => {
		getMock.mockImplementation(editorsResponse([{ id: "vscode", name: "VS Code" }]));
		renderButton();

		const open = await screen.findByRole("button", { name: "Open in VS Code" });
		await userEvent.click(open);

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		// No path in the body: the daemon picks the most recently changed file.
		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/open-editor", {
			params: { path: { sessionId: "sess-1" } },
			body: {},
		});
	});

	it("prefers Cursor's label when it is the only editor found", async () => {
		getMock.mockImplementation(editorsResponse([{ id: "cursor", name: "Cursor" }]));
		renderButton();
		expect(await screen.findByRole("button", { name: "Open in Cursor" })).toBeTruthy();
	});

	it("sends path '.' for the folder-only menu entry", async () => {
		getMock.mockImplementation(editorsResponse([{ id: "vscode", name: "VS Code" }]));
		renderButton();

		await userEvent.click(await screen.findByRole("button", { name: "Open in editor options" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: /open folder only/i }));

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		expect(postMock.mock.calls[0][1].body).toEqual({ path: "." });
	});

	it("lists only the folder open and the installed editors, never changed files", async () => {
		getMock.mockImplementation(editorsResponse([{ id: "vscode", name: "VS Code" }, { id: "cursor", name: "Cursor" }]));
		renderButton();

		await userEvent.click(await screen.findByRole("button", { name: "Open in editor options" }));
		const items = (await screen.findAllByRole("menuitem")).map((el) => el.textContent);
		expect(items).toEqual(["Open folder only", "VS Code", "Cursor"]);
		// The workspace-files endpoint must not be probed for this menu.
		expect(getMock.mock.calls.every((call) => call[0] === "/api/v1/editors")).toBe(true);
	});

	it("gives each editor its own mark rather than reusing one logo", async () => {
		getMock.mockImplementation(editorsResponse([{ id: "vscode", name: "VS Code" }, { id: "cursor", name: "Cursor" }]));
		renderButton();

		await userEvent.click(await screen.findByRole("button", { name: "Open in editor options" }));
		const items = await screen.findAllByRole("menuitem");
		const vscodePath = items[1].querySelector("svg path")?.getAttribute("d");
		const cursorPath = items[2].querySelector("svg path")?.getAttribute("d");
		expect(vscodePath).toBeTruthy();
		expect(cursorPath).toBeTruthy();
		expect(cursorPath).not.toEqual(vscodePath);
	});

	it("tints editors that have a brand colour and leaves monochrome brands alone", async () => {
		getMock.mockImplementation(editorsResponse([{ id: "vscode", name: "VS Code" }, { id: "cursor", name: "Cursor" }]));
		renderButton();

		await userEvent.click(await screen.findByRole("button", { name: "Open in editor options" }));
		const items = await screen.findAllByRole("menuitem");
		// VS Code's mark is genuinely blue; Cursor's real logo is greyscale, so it
		// must inherit the menu text colour rather than get an invented tint.
		expect(items[1].querySelector("svg")?.style.color).toBe("rgb(31, 156, 240)");
		expect(items[2].querySelector("svg")?.style.color).toBe("");
	});

	it("surfaces a launch failure instead of failing silently", async () => {
		getMock.mockImplementation(editorsResponse([{ id: "vscode", name: "VS Code" }]));
		postMock.mockResolvedValue({ error: { message: "Could not launch VS Code" }, response: { status: 500 } });
		renderButton();

		await userEvent.click(await screen.findByRole("button", { name: "Open in VS Code" }));
		expect(await screen.findByRole("alert")).toHaveTextContent("Could not launch VS Code");
	});
});

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { ProjectEnvironmentSettings } from "./ProjectEnvironmentSettings";

const { getMock, putMock } = vi.hoisted(() => ({ getMock: vi.fn(), putMock: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: getMock, PUT: putMock }, apiErrorMessage: () => "request failed" }));

beforeEach(() => {
	getMock.mockReset().mockResolvedValue({ data: { status: "ok", project: { id: "p", name: "Example", config: { env: { EXISTING: "old" }, autoReview: true } } } });
	putMock.mockReset().mockResolvedValue({ data: { status: "ok" } });
});

it("saves validated variables without dropping other project settings", async () => {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	render(<QueryClientProvider client={client}><ProjectEnvironmentSettings projectId="p" /></QueryClientProvider>);
	const value = await screen.findByLabelText("Value 1");
	expect(value).toHaveAttribute("type", "password");
	await userEvent.clear(value);
	await userEvent.type(value, "new");
	await userEvent.click(screen.getByRole("button", { name: "Add variable" }));
	await userEvent.type(screen.getByLabelText("Name 2"), "SECOND");
	await userEvent.type(screen.getByLabelText("Value 2"), "another");
	fireEvent.submit(document.getElementById("project-settings-form")!);
	await waitFor(() => expect(putMock).toHaveBeenCalledOnce());
	expect(putMock.mock.calls[0][1].body).toEqual({ displayName: "Example", config: { env: { EXISTING: "new", SECOND: "another" }, autoReview: true } });
});

it("imports pasted .env entries into the draft, replacing matching names without saving early", async () => {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	render(<QueryClientProvider client={client}><ProjectEnvironmentSettings projectId="p" /></QueryClientProvider>);
	await screen.findByLabelText("Value 1");
	await userEvent.click(screen.getByRole("button", { name: "Paste .env" }));
	fireEvent.change(screen.getByLabelText("Paste .env"), { target: { value: "# copied config\nexisting=replaced\nexport NEW_KEY=\"hello world\"\nURL=https://example.com/?a=b" } });
	fireEvent.submit(document.getElementById("project-settings-form")!);
	expect(screen.getByRole("alert")).toHaveTextContent("Add the pasted entries to the draft before saving");
	expect(putMock).not.toHaveBeenCalled();
	await userEvent.click(screen.getByRole("button", { name: "Add to draft" }));
	expect(putMock).not.toHaveBeenCalled();
	expect(screen.getByLabelText("Value 1")).toHaveValue("replaced");
	expect(screen.getByLabelText("Value 2")).toHaveAttribute("type", "password");
	fireEvent.submit(document.getElementById("project-settings-form")!);
	await waitFor(() => expect(putMock).toHaveBeenCalledOnce());
	expect(putMock.mock.calls[0][1].body.config.env).toEqual({ EXISTING: "replaced", NEW_KEY: "hello world", URL: "https://example.com/?a=b" });
});

it("rejects a pasted reserved name without partially changing the draft", async () => {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	const onSaveState = vi.fn();
	render(<QueryClientProvider client={client}><ProjectEnvironmentSettings projectId="p" onSaveState={onSaveState} /></QueryClientProvider>);
	await screen.findByLabelText("Value 1");
	await userEvent.click(screen.getByRole("button", { name: "Paste .env" }));
	fireEvent.change(screen.getByLabelText("Paste .env"), { target: { value: "NEW=good\nAO_SESSION_ID=spoof" } });
	await userEvent.click(screen.getByRole("button", { name: "Add to draft" }));
	expect(screen.getByRole("alert")).toHaveTextContent("Check line 2");
	expect(screen.queryByLabelText("Value 2")).not.toBeInTheDocument();
	expect(screen.getByLabelText("Value 1")).toHaveValue("old");
	expect(putMock).not.toHaveBeenCalled();
	expect(onSaveState).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "idle", dirty: false }));
});

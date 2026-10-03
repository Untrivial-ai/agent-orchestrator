import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { ProjectSetupSettings } from "./ProjectSetupSettings";

const { getMock, putMock } = vi.hoisted(() => ({ getMock: vi.fn(), putMock: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: getMock, PUT: putMock }, apiErrorMessage: () => "request failed" }));

beforeEach(() => {
	getMock.mockReset().mockResolvedValue({ data: { status: "ok", project: { id: "p", name: "Example", kind: "single_repo", config: { postCreate: ["echo first", "echo second"], autoReview: true } } } });
	putMock.mockReset().mockResolvedValue({ data: { status: "ok" } });
});

it("keeps setup steps ordered and saves the rest of the config", async () => {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	render(<QueryClientProvider client={client}><ProjectSetupSettings projectId="p" /></QueryClientProvider>);
	expect(await screen.findByLabelText("Step 1")).toHaveValue("echo first");
	expect(screen.getByLabelText("Step 2")).toHaveValue("echo second");
	await userEvent.clear(screen.getByLabelText("Step 2"));
	await userEvent.type(screen.getByLabelText("Step 2"), "echo updated");
	fireEvent.submit(document.getElementById("project-settings-form")!);
	await waitFor(() => expect(putMock).toHaveBeenCalledOnce());
	expect(putMock.mock.calls[0][1].body.config).toEqual({ postCreate: ["echo first", "echo updated"], autoReview: true });
});

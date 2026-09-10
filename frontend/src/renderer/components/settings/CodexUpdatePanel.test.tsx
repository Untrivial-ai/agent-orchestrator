import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../../api/schema";
import { apiClient } from "../../lib/api-client";
import { CodexUpdatePanel } from "./CodexUpdatePanel";

const advisory: components["schemas"]["CodexUpdateAdvisory"] = {
	path: "/selected/codex", realPath: "/owning/npm/codex.js", source: "npm", version: "1.0.0",
	versionSource: "npm", availableVersion: "1.1.0", updateAvailable: true, canUpdate: true,
	token: "verified-owner", stale: false, checkedAt: "2026-09-10T00:00:00Z", runningSessions: 0,
};
function setup(value = advisory, job?: components["schemas"]["InstallJob"]) {
	vi.spyOn(apiClient, "GET").mockResolvedValue({ data: value } as never);
	const post = vi.spyOn(apiClient, "POST").mockResolvedValue({ data: { target: "codex", method: "update:npm", status: "queued" } } as never);
	const onJob = vi.fn();
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(<QueryClientProvider client={client}><CodexUpdatePanel job={job} onJob={onJob} /></QueryClientProvider>);
	return { post, onJob };
}
afterEach(() => vi.restoreAllMocks());
describe("Codex update", () => {
	it("requires a user click and sends only verified ownership, with shared-installation context", async () => {
		const { post, onJob } = setup();
		const button = await screen.findByRole("button", { name: "Update now" });
		expect(post).not.toHaveBeenCalled();
		expect(screen.getByText(/shared Codex installation/)).toBeInTheDocument();
		await userEvent.click(button);
		expect(post).toHaveBeenCalledWith("/api/v1/agents/codex/update", { body: { token: "verified-owner" } });
		expect(onJob).toHaveBeenCalledWith(expect.objectContaining({ status: "queued" }));
	});
	it("keeps unknown ownership manual and exposes diagnostics", async () => {
		setup({ ...advisory, source: "unknown", canUpdate: false, warning: "Update this selected installation manually." });
		await screen.findByText(/manually/);
		expect(screen.queryByRole("button", { name: "Update now" })).not.toBeInTheDocument();
		expect(screen.getByText("Selected: /selected/codex")).toBeInTheDocument();
	});
	it("blocks running provider processes without offering termination", async () => {
		setup({ ...advisory, runningSessions: 2 });
		expect(await screen.findByRole("button", { name: "Update now" })).toBeDisabled();
		expect(screen.getByText(/Stop Codex workers or reviewers/)).toBeInTheDocument();
		expect(screen.getByText(/Restarting AO can leave old provider processes running/)).toBeInTheDocument();
	});
	it("shows failed verification even when the installed CLI remains usable", async () => {
		setup(advisory, { target: "codex", method: "update:npm", status: "failed", error: "CLI unchanged; refresh and try again.", output: "installer exited 0" });
		await screen.findByText("CLI unchanged; refresh and try again.");
		expect(screen.queryByText("Updated and verified for new provider processes.")).not.toBeInTheDocument();
	});
	it("reports stale ownership and permits an explicit retry of advisory reads", async () => {
		setup({ ...advisory, stale: true, canUpdate: false, warning: "Offline; cached version information." });
		await userEvent.click(await screen.findByRole("button", { name: "Check Codex for updates" }));
		await waitFor(() => expect(apiClient.GET).toHaveBeenCalledWith("/api/v1/agents/codex/update", { params: { query: { refresh: true } } }));
		expect(screen.getByText(/Last check failed/)).toBeInTheDocument();
	});
});

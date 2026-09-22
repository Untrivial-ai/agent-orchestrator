import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { DetectedHarnesses } from "./DetectedHarnesses";
import { agentReadiness } from "../test/agent-readiness-fixtures";

const getMock = vi.hoisted(() => vi.fn());
const postMock = vi.hoisted(() => vi.fn());

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	apiErrorMessage: (error: unknown) => (error instanceof Error ? error.message : "error"),
}));

function renderHarnesses() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={client}>
			<DetectedHarnesses />
		</QueryClientProvider>,
	);
}

describe("DetectedHarnesses", () => {
	beforeEach(() => {
		getMock.mockReset();
		postMock.mockReset();
		postMock.mockResolvedValue({ data: { agents: [] }, error: undefined });
	});

	it("lists ready harnesses and install hints for missing CLIs", async () => {
		getMock.mockResolvedValue({
			data: {
				agents: [
					agentReadiness("claude-code", "Claude Code", { installation: "installed", authentication: "authorized" }),
					agentReadiness("codex", "Codex", { installation: "not_installed", authentication: "not_applicable" }),
				],
			},
			error: undefined,
		});

		renderHarnesses();

		expect(await screen.findByTestId("detected-harnesses")).toBeInTheDocument();
		await waitFor(() => expect(screen.getByText(/Detected/i)).toBeInTheDocument());
		expect(screen.getByText("Claude Code")).toBeInTheDocument();
		expect(screen.getByText("Ready")).toBeInTheDocument();
		expect(screen.getByText("Codex")).toBeInTheDocument();
		expect(screen.getByText("Install CLI to use")).toBeInTheDocument();
	});
});

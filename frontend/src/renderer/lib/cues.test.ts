import { beforeEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "./api-client";
import { invokeCue } from "./cues";

vi.mock("./api-client", () => ({
	apiClient: { POST: vi.fn() },
	apiErrorMessage: vi.fn(),
}));

describe("invokeCue", () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	it("preserves an explicitly supplied empty session id", async () => {
		(apiClient.POST as ReturnType<typeof vi.fn>).mockResolvedValue({
			data: { sessionId: "unused" },
			error: undefined,
		});

		await invokeCue("cue-1", "");

		expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/cues/{cueId}/invoke", {
			params: { path: { cueId: "cue-1" } },
			body: { sessionId: "" },
		});
	});
});

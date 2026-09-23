import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { postMock, showToastMock } = vi.hoisted(() => ({ postMock: vi.fn(), showToastMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { POST: postMock },
	apiErrorMessage: (error: unknown, fallback: string) => (error instanceof Error ? error.message : fallback),
}));
vi.mock("../stores/ui-store", () => ({
	useUiStore: { getState: () => ({ showGlobalToast: showToastMock }) },
}));

import { useReapplyPreservedEdits } from "./useReapplyPreservedEdits";

function wrapper() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return function Wrapper({ children }: { children: ReactNode }) {
		return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
	};
}

describe("useReapplyPreservedEdits", () => {
	beforeEach(() => postMock.mockReset());
	beforeEach(() => showToastMock.mockReset());

	it("converts transport rejections into a resolved failure and an error toast", async () => {
		postMock
			.mockRejectedValueOnce(new Error("daemon unavailable"))
			.mockResolvedValue({ data: { ok: true }, error: undefined });
		let reapply: ((sessionId: string) => Promise<{ ok: boolean; message: string }>) | undefined;
		function CaptureReapply() {
			reapply = useReapplyPreservedEdits();
			return null;
		}
		const Wrapper = wrapper();
		render(
			<Wrapper>
				<CaptureReapply />
			</Wrapper>,
		);
		const outcome = await reapply?.("session-1");

		expect(outcome).toEqual({ ok: false, message: "daemon unavailable" });
		expect(showToastMock).toHaveBeenCalledWith("daemon unavailable", undefined, "error");
	});
});

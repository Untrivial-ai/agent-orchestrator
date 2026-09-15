import { act, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import type { OrchestratorReplacementFailure } from "../stores/ui-store";
import { restartProjectOrchestrator } from "../lib/restart-orchestrator";
import { OrchestratorReplacementDialog } from "./OrchestratorReplacementDialog";

const { spawnMock } = vi.hoisted(() => ({ spawnMock: vi.fn() }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("../lib/spawn-orchestrator", () => ({
	spawnOrchestrator: spawnMock,
	OrchestratorSpawnError: class extends Error {},
	isChatPreflightCode: () => false,
}));

describe("replacement retry focus", () => {
	it.each([true, false])("keeps Retry focused while pending, success=%s", async (success) => {
		let resolve!: (id: string) => void;
		let reject!: (error: Error) => void;
		spawnMock.mockReset().mockImplementation(() => new Promise<string>((res, rej) => { resolve = res; reject = rej; }));
		const queryClient = new QueryClient();
		vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue();
		const navigate = vi.fn();
		function Harness() {
			const [error, setError] = useState<OrchestratorReplacementFailure | undefined>({ message: "Restart failed" });
			const [pending, setPending] = useState(false);
			return <OrchestratorReplacementDialog projectId="proj-1" error={error} pending={pending} workspaces={[]}
				onOpenChange={() => setError(undefined)} onRetryAsTui={vi.fn()}
				onRetry={() => void restartProjectOrchestrator({ projectId: "proj-1", queryClient, navigate,
					setProjectRestarting: (_, value) => setPending(value),
					setOrchestratorReplacementError: (_, value) => setError(value ?? undefined),
				})} />;
		}
		render(<Harness />);
		const retry = screen.getByRole("button", { name: /retry/i });
		retry.focus();
		fireEvent.click(retry);
		expect(screen.getByRole("dialog")).toHaveAttribute("aria-busy", "true");
		expect(retry).toHaveFocus();
		expect(retry).toHaveAttribute("aria-disabled", "true");
		fireEvent.click(retry);
		expect(spawnMock).toHaveBeenCalledOnce();
		fireEvent.keyDown(retry, { key: "Escape" });
		expect(screen.getByRole("dialog")).toBeInTheDocument();
		expect(retry).toHaveFocus();
		await act(async () => { if (success) resolve("replacement"); else reject(new Error("Retry failed")); });
		if (success) {
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
			expect(navigate).toHaveBeenCalledOnce();
		} else {
			expect(screen.getByText("Retry failed")).toBeInTheDocument();
			expect(retry).toHaveFocus();
			expect(retry).toHaveAttribute("aria-disabled", "false");
			expect(navigate).not.toHaveBeenCalled();
		}
	});
});

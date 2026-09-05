import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";

const { spawnMock } = vi.hoisted(() => ({
	spawnMock: vi.fn(),
}));

vi.mock("./spawn-orchestrator", () => ({
	OrchestratorSpawnError: class OrchestratorSpawnError extends Error {
		constructor(
			message: string,
			readonly code?: string,
			readonly requestId?: string,
			readonly status?: number,
		) {
			super(message);
		}
	},
	spawnOrchestrator: spawnMock,
}));

import { OrchestratorSpawnError } from "./spawn-orchestrator";
import { restartProjectOrchestrator } from "./restart-orchestrator";

describe("restartProjectOrchestrator", () => {
	beforeEach(() => {
		spawnMock.mockReset();
	});

	it("invalidates workspace state and records an error when clean restart fails", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue();
		const navigate = vi.fn();
		const setProjectRestarting = vi.fn();
		const setOrchestratorReplacementError = vi.fn();
		const onError = vi.fn();
		const failure = new Error("missing goose binary");
		spawnMock.mockRejectedValue(failure);

		await restartProjectOrchestrator({
			projectId: "proj-1",
			queryClient,
			navigate,
			setProjectRestarting,
			setOrchestratorReplacementError,
			onError,
		});

		expect(spawnMock).toHaveBeenCalledWith("proj-1", "restart", true, undefined);
		expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: workspaceQueryKey });
		expect(setOrchestratorReplacementError).toHaveBeenNthCalledWith(1, "proj-1", null);
		expect(setOrchestratorReplacementError).toHaveBeenNthCalledWith(2, "proj-1", {
			message: "missing goose binary",
		});
		expect(setProjectRestarting).toHaveBeenNthCalledWith(1, "proj-1", true);
		expect(setProjectRestarting).toHaveBeenLastCalledWith("proj-1", false);
		expect(onError).toHaveBeenCalledWith(failure);
		expect(navigate).not.toHaveBeenCalled();
	});

	it("blurs the restart control so the replacement terminal can reclaim focus", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue();
		const restartButton = document.createElement("button");
		document.body.appendChild(restartButton);
		restartButton.focus();
		spawnMock.mockResolvedValue("session-2");

		await restartProjectOrchestrator({
			projectId: "proj-1",
			queryClient,
			navigate: vi.fn(),
			setProjectRestarting: vi.fn(),
			setOrchestratorReplacementError: vi.fn(),
		});

		expect(restartButton).not.toHaveFocus();
		restartButton.remove();
	});

	it("still records the replacement error when workspace invalidation fails", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		vi.spyOn(queryClient, "invalidateQueries").mockRejectedValue(new Error("refetch failed"));
		const navigate = vi.fn();
		const setProjectRestarting = vi.fn();
		const setOrchestratorReplacementError = vi.fn();
		const onError = vi.fn();
		const failure = new Error("missing goose binary");
		spawnMock.mockRejectedValue(failure);

		await restartProjectOrchestrator({
			projectId: "proj-1",
			queryClient,
			navigate,
			setProjectRestarting,
			setOrchestratorReplacementError,
			onError,
		});

		expect(setOrchestratorReplacementError).toHaveBeenLastCalledWith("proj-1", {
			message: "missing goose binary",
		});
		expect(setProjectRestarting).toHaveBeenLastCalledWith("proj-1", false);
		expect(onError).toHaveBeenCalledWith(failure);
		expect(navigate).not.toHaveBeenCalled();
	});

	it("preserves typed preflight details and retries with an explicit TUI mode", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue();
		const setOrchestratorReplacementError = vi.fn();
		spawnMock.mockRejectedValue(
			new OrchestratorSpawnError(
				"Claude Code is unavailable",
				"CHAT_DRIVER_UNAVAILABLE",
				"request-42",
				400,
			),
		);

		await restartProjectOrchestrator({
			projectId: "proj-1",
			queryClient,
			navigate: vi.fn(),
			setProjectRestarting: vi.fn(),
			setOrchestratorReplacementError,
			mode: "tui",
		});

		expect(spawnMock).toHaveBeenCalledWith("proj-1", "restart", true, "tui");
		expect(setOrchestratorReplacementError).toHaveBeenLastCalledWith("proj-1", {
			message: "Claude Code is unavailable",
			code: "CHAT_DRIVER_UNAVAILABLE",
			requestId: "request-42",
		});
	});
});

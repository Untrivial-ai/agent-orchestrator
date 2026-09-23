import { describe, expect, it, vi, beforeEach } from "vitest";
import {
	canBypassOrchestratorApprovals,
	isChatPreflightError,
	OrchestratorSpawnError,
	resumeOrchestrator,
	spawnOrchestrator,
} from "./spawn-orchestrator";
import { apiClient } from "./api-client";
import { captureRendererEvent } from "./telemetry";

const { notificationShowMock } = vi.hoisted(() => ({
	notificationShowMock: vi.fn(),
}));

vi.mock("./api-client", () => ({
	apiClient: { POST: vi.fn() },
	apiErrorCode: (error: unknown) =>
		typeof error === "object" && error !== null && "code" in error
			? String((error as { code: unknown }).code)
			: undefined,
	apiErrorRequestId: (error: unknown) =>
		typeof error === "object" && error !== null && "requestId" in error
			? String((error as { requestId: unknown }).requestId)
			: undefined,
	apiErrorDetails: (error: unknown) =>
		typeof error === "object" && error !== null && "details" in error
			? (error as { details: Record<string, unknown> }).details
			: undefined,
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		if (typeof error === "object" && error !== null && "message" in error) {
			const body = error as { code?: unknown; message: unknown };
			const message = String(body.message);
			return typeof body.code === "string" && body.code !== "" ? `${message} (${body.code})` : message;
		}
		return fallback;
	},
}));

vi.mock("./telemetry", () => ({
	captureRendererEvent: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("./bridge", () => ({
	aoBridge: { notifications: { show: notificationShowMock } },
}));

const captureMock = vi.mocked(captureRendererEvent);

describe("spawnOrchestrator", () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	it("sends clean:true through to the request body when asked", async () => {
		(apiClient.POST as ReturnType<typeof vi.fn>).mockResolvedValue({
			data: { orchestrator: { id: "proj-9" } },
			error: undefined,
			response: { status: 201 },
		});
		const id = await spawnOrchestrator("proj", "restore_dialog", true);
		expect(id).toBe("proj-9");
		expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/orchestrators", {
			body: { projectId: "proj", clean: true },
		});
	});

	it("defaults clean to false / omitted for the existing call sites", async () => {
		(apiClient.POST as ReturnType<typeof vi.fn>).mockResolvedValue({
			data: { orchestrator: { id: "proj-1" } },
			error: undefined,
			response: { status: 201 },
		});
		await spawnOrchestrator("proj", "board");
		expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/orchestrators", {
			body: { projectId: "proj", clean: false },
		});
	});

	it("sends mode only when the user explicitly chooses it", async () => {
		(apiClient.POST as ReturnType<typeof vi.fn>).mockResolvedValue({
			data: { orchestrator: { id: "proj-2" } },
			error: undefined,
			response: { status: 201 },
		});
		await spawnOrchestrator("proj", "board", false, "tui");
		expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/orchestrators", {
			body: { projectId: "proj", clean: false, mode: "tui" },
		});
	});

	it("emits the requested + succeeded triad keyed by source", async () => {
		(apiClient.POST as ReturnType<typeof vi.fn>).mockResolvedValue({
			data: { orchestrator: { id: "proj-7" } },
			error: undefined,
			response: { status: 201 },
		});
		await spawnOrchestrator("proj", "sidebar");
		expect(captureMock).toHaveBeenCalledWith("ao.renderer.orchestrator_spawn_requested", {
			project_id: "proj",
			source: "sidebar",
		});
		expect(captureMock).toHaveBeenCalledWith("ao.renderer.orchestrator_spawn_succeeded", {
			project_id: "proj",
			source: "sidebar",
		});
	});

	it("accepts project_clone as a first-class orchestrator spawn source", async () => {
		(apiClient.POST as ReturnType<typeof vi.fn>).mockResolvedValue({
			data: { orchestrator: { id: "proj-8" } },
			error: undefined,
			response: { status: 201 },
		});
		await spawnOrchestrator("proj", "project_clone");
		expect(captureMock).toHaveBeenCalledWith("ao.renderer.orchestrator_spawn_requested", {
			project_id: "proj",
			source: "project_clone",
		});
	});

	it("emits the failed event and rethrows when the daemon rejects the spawn", async () => {
		(apiClient.POST as ReturnType<typeof vi.fn>).mockResolvedValue({
			data: undefined,
			error: { message: "boom" },
			response: { status: 500 },
		});
		await expect(spawnOrchestrator("proj", "topbar")).rejects.toThrow("boom");
		expect(captureMock).toHaveBeenCalledWith("ao.renderer.orchestrator_spawn_failed", {
			project_id: "proj",
			source: "topbar",
		});
		expect(captureMock).not.toHaveBeenCalledWith("ao.renderer.orchestrator_spawn_succeeded", expect.anything());
	});

	it("surfaces daemon spawn error messages and codes", async () => {
		(apiClient.POST as ReturnType<typeof vi.fn>).mockResolvedValue({
			data: undefined,
			error: {
				code: "CHAT_DRIVER_UNAVAILABLE",
				message: "chat driver is unavailable",
				requestId: "request-42",
			},
			response: { status: 400 },
		});

		const error = await spawnOrchestrator("proj", "board").catch((caught: unknown) => caught);
		expect(error).toBeInstanceOf(OrchestratorSpawnError);
		expect(error).toMatchObject({
			code: "CHAT_DRIVER_UNAVAILABLE",
			requestId: "request-42",
			status: 400,
		});
		expect((error as Error).message).toBe("chat driver is unavailable (CHAT_DRIVER_UNAVAILABLE)");
		expect(isChatPreflightError(error)).toBe(true);
	});

	it("sends an approval override only when the caller passes one", async () => {
		(apiClient.POST as ReturnType<typeof vi.fn>).mockResolvedValue({
			data: { orchestrator: { id: "proj-3" } },
			error: undefined,
			response: { status: 201 },
		});
		await spawnOrchestrator("proj", "board", false, undefined, "bypass-permissions");
		expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/orchestrators", {
			body: { projectId: "proj", clean: false, approvalMode: "bypass-permissions" },
		});
	});

	it("carries daemon error details for the approvals fallback", async () => {
		const details = { missingCapabilities: ["approvals"], allowedApprovalModes: ["bypass-permissions"] };
		(apiClient.POST as ReturnType<typeof vi.fn>).mockResolvedValue({
			data: undefined,
			error: {
				code: "SESSION_MODE_UNSUPPORTED",
				message: "chat needs approvals",
				requestId: "request-7",
				details,
			},
			response: { status: 400 },
		});

		const error = await spawnOrchestrator("proj", "board").catch((caught: unknown) => caught);
		expect(error).toMatchObject({ code: "SESSION_MODE_UNSUPPORTED", details });
		expect(canBypassOrchestratorApprovals(
			(error as OrchestratorSpawnError).code,
			(error as OrchestratorSpawnError).details,
		)).toBe(true);
	});

	it("refuses the bypass fallback for non-approvals preflight failures", () => {
		expect(canBypassOrchestratorApprovals("CHAT_AUTH_REQUIRED", undefined)).toBe(false);
		expect(canBypassOrchestratorApprovals("SESSION_MODE_UNSUPPORTED", { missingCapabilities: ["models"] })).toBe(
			false,
		);
		expect(
			canBypassOrchestratorApprovals("SESSION_MODE_UNSUPPORTED", {
				missingCapabilities: ["approvals"],
				allowedApprovalModes: ["accept-edits"],
			}),
		).toBe(false);
	});
});

describe("resumeOrchestrator", () => {
	const postMock = vi.mocked(apiClient.POST);

	beforeEach(() => {
		notificationShowMock.mockReset().mockResolvedValue(undefined);
	});

	it("posts resume-agent for the session", async () => {
		postMock.mockResolvedValue({ data: {}, error: undefined, response: { status: 200 } } as never);
		await resumeOrchestrator("proj-1-orch");
		expect(postMock).toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/resume-agent", {
			params: { path: { sessionId: "proj-1-orch" } },
		});
	});

	it("warns when resume falls back to the saved prompt", async () => {
		postMock.mockResolvedValue({
			data: { resumeMode: "saved_prompt" },
			error: undefined,
			response: { status: 200 },
		} as never);

		await resumeOrchestrator("proj-1-orch");

		expect(notificationShowMock).toHaveBeenCalledWith({
			id: expect.stringMatching(/^resume-agent-fallback:proj-1-orch:/),
			title: "Started from saved prompt",
			body: "AO could not resume the native agent session, so it started a new conversation from the saved prompt.",
		});
	});

	// The caller asked for a working orchestrator; one that is already running
	// satisfies that, so the 409 must not surface as a failure.
	it("treats AGENT_NOT_EXITED as success", async () => {
		postMock.mockResolvedValue({
			data: undefined,
			error: { code: "AGENT_NOT_EXITED", message: "still running" },
			response: { status: 409 },
		} as never);
		await expect(resumeOrchestrator("proj-1-orch")).resolves.toBeUndefined();
	});

	it("throws on any other error", async () => {
		postMock.mockResolvedValue({
			data: undefined,
			error: { code: "SESSION_NOT_FOUND", message: "Unknown session" },
			response: { status: 404 },
		} as never);
		await expect(resumeOrchestrator("proj-1-orch")).rejects.toThrow(/Unknown session/);
	});
});

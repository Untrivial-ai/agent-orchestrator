import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { captureRendererEvent } from "./telemetry";

vi.mock("./telemetry", () => ({ captureRendererEvent: vi.fn() }));

const capture = vi.mocked(captureRendererEvent);

beforeEach(() => {
	vi.resetModules();
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
	vi.spyOn(Math, "random").mockReturnValue(0);
	capture.mockClear();
});

afterEach(() => {
	vi.useRealTimers();
	vi.restoreAllMocks();
});

it("measures session navigation through the painted chat surface", async () => {
	const now = vi.spyOn(performance, "now").mockReturnValue(1_000);
	const timing = await import("./journey-timing");
	timing.startSessionOpen("session-a");
	now.mockReturnValue(1_150);
	timing.sessionUsable("session-b", "chat");
	expect(capture).not.toHaveBeenCalled();
	now.mockReturnValue(1_250);
	timing.sessionUsable("session-a", "chat");
	timing.sessionUsable("session-a", "chat");
	expect(capture).toHaveBeenCalledExactlyOnceWith("ao.renderer.session_open_timing", {
		duration_ms: 250,
		outcome: "ready",
		surface: "chat",
	});
});

it("keeps task timing open across the API response and route navigation", async () => {
	const now = vi.spyOn(performance, "now").mockReturnValue(1_000);
	const timing = await import("./journey-timing");
	const attempt = timing.startTaskCreate("local");
	now.mockReturnValue(1_200);
	timing.taskCreateReturned(attempt, "new-session");
	timing.startSessionOpen("new-session");
	expect(capture).not.toHaveBeenCalled();
	now.mockReturnValue(1_600);
	timing.sessionUsable("new-session", "tui");
	expect(capture).toHaveBeenCalledExactlyOnceWith("ao.renderer.task_create_timing", {
		duration_ms: 600,
		outcome: "ready",
		surface: "tui",
		scope: "local",
	});
});

it("records failures and timeouts without late success", async () => {
	const now = vi.spyOn(performance, "now").mockReturnValue(1_000);
	const timing = await import("./journey-timing");
	const attempt = timing.startTaskCreate("standalone");
	now.mockReturnValue(1_080);
	timing.taskCreateFailed(attempt);
	timing.startSessionOpen("stalled-session");
	now.mockReturnValue(121_080);
	vi.advanceTimersByTime(120_000);
	timing.sessionUsable("stalled-session", "chat");
	expect(capture.mock.calls).toEqual([
		["ao.renderer.task_create_timing", { duration_ms: 80, outcome: "failed", scope: "standalone" }],
		["ao.renderer.session_open_timing", { duration_ms: 120_000, outcome: "timeout" }],
	]);
});

it("cancels a hidden session and samples completed journeys", async () => {
	const now = vi.spyOn(performance, "now").mockReturnValue(1_000);
	const timing = await import("./journey-timing");
	timing.startSessionOpen("file-session");
	now.mockReturnValue(1_100);
	timing.cancelHiddenSession("file-session");
	timing.sessionUsable("file-session", "tui");
	expect(capture).toHaveBeenCalledExactlyOnceWith("ao.renderer.session_open_timing", {
		duration_ms: 100,
		outcome: "cancelled",
	});
	capture.mockClear();
	vi.mocked(Math.random).mockReturnValue(0.5);
	timing.startSessionOpen("unsampled-session");
	timing.sessionUsable("unsampled-session", "chat");
	expect(capture).not.toHaveBeenCalled();
	timing.recordStartupTiming(500, "ready");
	expect(capture).toHaveBeenCalledExactlyOnceWith("ao.renderer.startup_timing", {
		duration_ms: 500,
		outcome: "ready",
	});
});

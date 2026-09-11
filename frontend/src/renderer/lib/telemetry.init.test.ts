import { beforeEach, describe, expect, it, vi } from "vitest";

// initTelemetry memoizes its result in a module-scoped promise, so each test
// resets the module registry and re-mocks the heavy boundaries (posthog, the
// Electron bridge) before importing. This exercises the real init control flow.

const posthogStub = {
	init: vi.fn(),
	register: vi.fn(),
	capture: vi.fn(() => ({})),
	captureException: vi.fn(),
	addExceptionStep: vi.fn(),
};

function mockPosthog() {
	vi.doMock("posthog-js/dist/module.full.no-external", () => ({
		default: posthogStub,
		PostHog: class {},
	}));
}

describe("initTelemetry recovery", () => {
	beforeEach(() => {
		vi.resetModules();
		vi.clearAllMocks();
	});

	it("retries init after a transient thrown failure instead of caching it", async () => {
		let calls = 0;
		mockPosthog();
		vi.doMock("./bridge", () => ({
			aoBridge: {
				telemetry: {
					getBootstrap: vi.fn(async () => {
						calls++;
						if (calls === 1) throw new Error("transient bootstrap failure");
						return { appVersion: "1.2.3", platform: "darwin", distinctId: "ins_test", disabledEvents: [], eventsEnabled: true, consentGeneration: "generation-1" };
					}),
				},
				updateSettings: { get: vi.fn(async () => ({})) },
			},
		}));

		const { initTelemetry } = await import("./telemetry");
		// First attempt throws -> caught -> false, and the memoized promise is dropped.
		expect(await initTelemetry()).toBe(false);
		// A later call retries and succeeds, rather than the whole session staying dark.
		expect(await initTelemetry()).toBe(true);
		expect(calls).toBe(2);
	});

	it("keeps a deliberately withheld result memoized (no retry, no re-bootstrap)", async () => {
		// Null bootstrap = telemetry withheld (no key / not opted in). That is a
		// deliberate false, not an error, so it must stay cached and NOT retry.
		const getBootstrap = vi.fn(async () => null);
		mockPosthog();
		vi.doMock("./bridge", () => ({
			aoBridge: {
				telemetry: { getBootstrap },
				updateSettings: { get: vi.fn(async () => ({})) },
			},
		}));

		const { initTelemetry } = await import("./telemetry");
		expect(await initTelemetry()).toBe(false);
		expect(await initTelemetry()).toBe(false);
		// Memoized: the withheld result is not re-attempted.
		expect(getBootstrap).toHaveBeenCalledTimes(1);
	});

	it("initializes PostHog when failure reporting is disabled", async () => {
		mockPosthog();
		vi.doMock("./bridge", () => ({
			aoBridge: {
				telemetry: {
					getBootstrap: vi.fn(async () => ({
						appVersion: "1.2.3", platform: "darwin", distinctId: "ins_test",
						disabledEvents: [], eventsEnabled: false, consentGeneration: "generation-off",
					})),
				},
				updateSettings: { get: vi.fn(async () => ({})) },
			},
		}));

		const { applyRendererTelemetryPolicy, clearRendererTelemetryQueues, initTelemetry } = await import("./telemetry");
		expect(await initTelemetry()).toBe(true);
		expect(posthogStub.init).toHaveBeenCalledOnce();
		expect(() => clearRendererTelemetryQueues()).not.toThrow();
		expect(() => applyRendererTelemetryPolicy(false)).not.toThrow();
	});
});

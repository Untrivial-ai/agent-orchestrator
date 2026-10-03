import { describe, expect, it, vi } from "vitest";
import {
	failPendingMobileBrowserNavigation,
	schedulePendingMobileBrowserNavigationSuccess,
	type PendingMobileBrowserNavigation,
} from "./mobileBrowserNavigation";

describe("mobile browser navigation commands", () => {
	it("fails a started navigation instead of allowing load-end success", () => {
		const resolve = vi.fn();
		const pending: PendingMobileBrowserNavigation = {
			requestId: "open-1",
			started: true,
			resolve,
			timer: setTimeout(() => undefined, 60_000),
		};

		expect(failPendingMobileBrowserNavigation(pending, {
			code: "BROWSER_NAVIGATION_HTTP_ERROR",
			message: "Preview returned HTTP 503.",
		})).toBe(true);
		expect(resolve).toHaveBeenCalledWith({
			ok: false,
			error: {
				code: "BROWSER_NAVIGATION_HTTP_ERROR",
				message: "Preview returned HTTP 503.",
			},
		});
	});

	it("does not attach an old load failure to a navigation that has not started", () => {
		const resolve = vi.fn();
		const timer = setTimeout(() => undefined, 60_000);
		const pending: PendingMobileBrowserNavigation = {
			requestId: "open-2",
			started: false,
			resolve,
			timer,
		};

		expect(failPendingMobileBrowserNavigation(pending, {
			code: "BROWSER_NAVIGATION_FAILED",
			message: "The previous page failed.",
		})).toBe(false);
		expect(resolve).not.toHaveBeenCalled();
		clearTimeout(timer);
	});

	it("lets Android's finish-then-error sequence reject before deferred success", () => {
		vi.useFakeTimers();
		try {
			const resolve = vi.fn();
			const pending: PendingMobileBrowserNavigation = {
				requestId: "open-3",
				started: true,
				resolve,
				timer: setTimeout(() => undefined, 60_000),
			};
			expect(schedulePendingMobileBrowserNavigationSuccess(
				pending,
				() => resolve({ ok: true, result: { url: "https://unreachable.invalid" } }),
				true,
			)).toBe(true);

			expect(failPendingMobileBrowserNavigation(pending, {
				code: "BROWSER_NAVIGATION_FAILED",
				message: "net::ERR_NAME_NOT_RESOLVED",
			})).toBe(true);
			vi.runAllTimers();

			expect(resolve).toHaveBeenCalledTimes(1);
			expect(resolve).toHaveBeenCalledWith({
				ok: false,
				error: {
					code: "BROWSER_NAVIGATION_FAILED",
					message: "net::ERR_NAME_NOT_RESOLVED",
				},
			});
		} finally {
			vi.useRealTimers();
		}
	});
});

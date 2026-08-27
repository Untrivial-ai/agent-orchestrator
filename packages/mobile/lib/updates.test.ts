import { describe, expect, it, vi } from "vitest";
import {
	checkAndDownload,
	describeUpdateRow,
	MIN_BACKGROUND_MS,
	onForeground,
	type UpdatesApi,
} from "./updates";

function api(over: Partial<UpdatesApi> = {}): UpdatesApi {
	return {
		isEnabled: true,
		checkForUpdateAsync: vi.fn(async () => ({ isAvailable: false, isRollBackToEmbedded: false }) as never),
		fetchUpdateAsync: vi.fn(async () => ({ isNew: false, isRollBackToEmbedded: false }) as never),
		...over,
	};
}

describe("onForeground", () => {
	const away = 1_000_000;

	it("does nothing until the app has been away long enough", () => {
		expect(onForeground(false, null, away)).toBe("none");
		expect(onForeground(true, null, away)).toBe("none");
		expect(onForeground(true, away, away + MIN_BACKGROUND_MS - 1)).toBe("none");
	});

	it("applies a pending update after a long absence, otherwise checks", () => {
		expect(onForeground(true, away, away + MIN_BACKGROUND_MS)).toBe("reload");
		expect(onForeground(false, away, away + MIN_BACKGROUND_MS)).toBe("check");
	});
});

describe("checkAndDownload", () => {
	it("does nothing in builds where updates are off", async () => {
		const a = api({ isEnabled: false });
		expect(await checkAndDownload(a)).toEqual({ kind: "disabled" });
		expect(a.checkForUpdateAsync).not.toHaveBeenCalled();
	});

	it("reports up to date without downloading", async () => {
		const a = api();
		expect(await checkAndDownload(a)).toEqual({ kind: "up-to-date" });
		expect(a.fetchUpdateAsync).not.toHaveBeenCalled();
	});

	it("downloads an available update but never reloads", async () => {
		const a = api({
			checkForUpdateAsync: vi.fn(async () => ({ isAvailable: true, isRollBackToEmbedded: false }) as never),
			fetchUpdateAsync: vi.fn(async () => ({ isNew: true, isRollBackToEmbedded: false }) as never),
		});
		expect(await checkAndDownload(a)).toEqual({ kind: "downloaded" });
	});

	it("treats a rollback directive as something to restart into", async () => {
		const a = api({
			checkForUpdateAsync: vi.fn(async () => ({ isAvailable: false, isRollBackToEmbedded: true }) as never),
			fetchUpdateAsync: vi.fn(async () => ({ isNew: false, isRollBackToEmbedded: true }) as never),
		});
		expect(await checkAndDownload(a)).toEqual({ kind: "downloaded" });
	});

	it("is up to date when the server's update was already downloaded", async () => {
		const a = api({
			checkForUpdateAsync: vi.fn(async () => ({ isAvailable: true, isRollBackToEmbedded: false }) as never),
		});
		expect(await checkAndDownload(a)).toEqual({ kind: "up-to-date" });
	});

	it("returns errors instead of throwing", async () => {
		const boom = new Error("offline");
		const a = api({ checkForUpdateAsync: vi.fn(async () => { throw boom; }) });
		expect(await checkAndDownload(a)).toEqual({ kind: "error", error: boom });
		const b = api({
			checkForUpdateAsync: vi.fn(async () => ({ isAvailable: true, isRollBackToEmbedded: false }) as never),
			fetchUpdateAsync: vi.fn(async () => { throw boom; }),
		});
		expect(await checkAndDownload(b)).toEqual({ kind: "error", error: boom });
	});

	it("shares one request between concurrent callers", async () => {
		let release: (v: never) => void = () => {};
		const a = api();
		// First call hangs until released; later calls resolve normally.
		vi.mocked(a.checkForUpdateAsync).mockImplementationOnce(() => new Promise<never>((r) => { release = r; }));
		const first = checkAndDownload(a);
		const second = checkAndDownload(a);
		expect(second).toBe(first);
		release({ isAvailable: false, isRollBackToEmbedded: false } as never);
		expect(await first).toEqual({ kind: "up-to-date" });
		expect(a.checkForUpdateAsync).toHaveBeenCalledTimes(1);
		// Once settled, the next call is a fresh request.
		await checkAndDownload(a);
		expect(a.checkForUpdateAsync).toHaveBeenCalledTimes(2);
	});
});

describe("describeUpdateRow", () => {
	const idle = { enabled: true, pending: false, phase: "idle" as const, lastManual: null };

	it("is inert in development builds, whatever else is going on", () => {
		expect(describeUpdateRow({ ...idle, enabled: false, pending: true })).toEqual({
			value: "Off in development builds",
			tone: "default",
			busy: false,
			action: null,
		});
	});

	it("offers a restart as soon as an update is downloaded", () => {
		expect(describeUpdateRow({ ...idle, pending: true, phase: "checking" }).action).toBe("restart");
	});

	it("shows progress without an action", () => {
		expect(describeUpdateRow({ ...idle, phase: "checking" })).toMatchObject({ value: "Checking…", busy: true, action: null });
		expect(describeUpdateRow({ ...idle, phase: "downloading" })).toMatchObject({ value: "Downloading…", busy: true, action: null });
	});

	it("reports the last manual check and stays tappable", () => {
		expect(describeUpdateRow({ ...idle, lastManual: { kind: "up-to-date" } })).toEqual({
			value: "Up to date",
			tone: "good",
			busy: false,
			action: "check",
		});
		expect(describeUpdateRow({ ...idle, lastManual: { kind: "error", error: null } })).toEqual({
			value: "Couldn't check",
			tone: "bad",
			busy: false,
			action: "check",
		});
		expect(describeUpdateRow(idle)).toEqual({ value: "Check now", tone: "default", busy: false, action: "check" });
	});
});

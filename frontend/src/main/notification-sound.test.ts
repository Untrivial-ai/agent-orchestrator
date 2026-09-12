// @vitest-environment node
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { mkdtemp, readdir, rm, writeFile, stat } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import {
	MAX_NOTIFICATION_SOUND_BYTES,
	NOTIFICATION_SOUND_DIR_NAME,
	NotificationSoundImportError,
	clearNotificationSound,
	importNotificationSound,
	notificationSoundMimeType,
	readNotificationSound,
} from "./notification-sound";

describe("notification-sound", () => {
	let stateDir: string;
	let sourceDir: string;
	beforeEach(async () => {
		stateDir = await mkdtemp(path.join(os.tmpdir(), "ao-sound-state-"));
		sourceDir = await mkdtemp(path.join(os.tmpdir(), "ao-sound-src-"));
	});
	afterEach(async () => {
		await rm(stateDir, { recursive: true, force: true });
		await rm(sourceDir, { recursive: true, force: true });
	});

	async function writeSource(name: string, bytes: Uint8Array | string): Promise<string> {
		const file = path.join(sourceDir, name);
		await writeFile(file, bytes);
		return file;
	}

	it("maps supported extensions to MIME types case-insensitively", () => {
		expect(notificationSoundMimeType("/x/ding.mp3")).toBe("audio/mpeg");
		expect(notificationSoundMimeType("/x/DING.WAV")).toBe("audio/wav");
		expect(notificationSoundMimeType("/x/ding.txt")).toBeNull();
		expect(notificationSoundMimeType("/x/ding")).toBeNull();
	});

	it("copies the chosen file under the state dir and keeps its name", async () => {
		const source = await writeSource("ding.wav", new Uint8Array([1, 2, 3]));
		const copied = await importNotificationSound(stateDir, source);
		expect(copied).toBe(path.join(stateDir, NOTIFICATION_SOUND_DIR_NAME, "ding.wav"));
		expect((await stat(copied)).size).toBe(3);

		// The original is no longer needed once imported.
		await rm(source);
		expect(await readNotificationSound(copied)).toEqual({ bytes: new Uint8Array([1, 2, 3]), mimeType: "audio/wav" });
	});

	it("replaces a previously imported sound instead of accumulating files", async () => {
		await importNotificationSound(stateDir, await writeSource("one.mp3", "a"));
		await importNotificationSound(stateDir, await writeSource("two.ogg", "b"));
		expect(await readdir(path.join(stateDir, NOTIFICATION_SOUND_DIR_NAME))).toEqual(["two.ogg"]);
	});

	it("rejects unsupported formats before touching the state dir", async () => {
		const source = await writeSource("notes.txt", "hello");
		await expect(importNotificationSound(stateDir, source)).rejects.toMatchObject({ code: "unsupported_type" });
		await expect(readdir(path.join(stateDir, NOTIFICATION_SOUND_DIR_NAME))).rejects.toThrow();
	});

	it("rejects files over the size cap", async () => {
		const source = await writeSource("huge.wav", new Uint8Array(MAX_NOTIFICATION_SOUND_BYTES + 1));
		await expect(importNotificationSound(stateDir, source)).rejects.toBeInstanceOf(NotificationSoundImportError);
		await expect(importNotificationSound(stateDir, source)).rejects.toMatchObject({ code: "too_large" });
	});

	it("reports a missing source as unreadable", async () => {
		await expect(importNotificationSound(stateDir, path.join(sourceDir, "gone.mp3"))).rejects.toMatchObject({
			code: "unreadable",
		});
	});

	it("reads null for no sound, a vanished file, or an unsupported path so callers fall back to the beep", async () => {
		expect(await readNotificationSound(null)).toBeNull();
		expect(await readNotificationSound(path.join(stateDir, "missing.mp3"))).toBeNull();
		expect(await readNotificationSound(await writeSource("x.txt", "nope"))).toBeNull();
	});

	it("clears the imported sound and tolerates nothing being there", async () => {
		await importNotificationSound(stateDir, await writeSource("one.mp3", "a"));
		await clearNotificationSound(stateDir);
		await expect(readdir(path.join(stateDir, NOTIFICATION_SOUND_DIR_NAME))).rejects.toThrow();
		await expect(clearNotificationSound(stateDir)).resolves.toBeUndefined();
	});
});

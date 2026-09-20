import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../assets/notification.mp3", () => ({ default: "/assets/notification.mp3" }));

import { playNotificationSound } from "./notification-sound-player";

type FakeAudio = {
	src: string;
	listeners: Record<string, () => void>;
	play: () => Promise<void>;
};

describe("playNotificationSound", () => {
	let audio: FakeAudio;

	beforeEach(() => {
		audio = { src: "", listeners: {}, play: () => Promise.resolve() };
		vi.stubGlobal(
			"Audio",
			class {
				constructor(src: string) {
					audio.src = src;
				}
				addEventListener(name: string, listener: () => void) {
					audio.listeners[name] = listener;
				}
				play() {
					return audio.play();
				}
			},
		);
	});
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("plays the bundled asset without reporting failure", async () => {
		const onFailure = vi.fn();
		playNotificationSound(onFailure);
		await Promise.resolve();
		expect(audio.src).toBe("/assets/notification.mp3");
		expect(onFailure).not.toHaveBeenCalled();
	});

	it("reports once when play() rejects and the element also errors", async () => {
		audio.play = () => Promise.reject(new Error("NotSupportedError"));
		const onFailure = vi.fn();
		playNotificationSound(onFailure);
		await Promise.resolve();
		await Promise.resolve();
		audio.listeners.error();
		expect(onFailure).toHaveBeenCalledTimes(1);
	});

	it("reports when the asset cannot be decoded", () => {
		const onFailure = vi.fn();
		playNotificationSound(onFailure);
		audio.listeners.error();
		expect(onFailure).toHaveBeenCalledTimes(1);
	});

	it("reports when the renderer has no audio output at all", () => {
		vi.stubGlobal("Audio", undefined);
		const onFailure = vi.fn();
		playNotificationSound(onFailure);
		expect(onFailure).toHaveBeenCalledTimes(1);
	});
});

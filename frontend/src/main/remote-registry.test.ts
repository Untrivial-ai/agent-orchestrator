import { describe, expect, it } from "vitest";
import { RemoteRegistry } from "./remote-registry";

const workbox = { label: "workbox", url: "http://192.0.2.1:3011", password: "secret", hostId: "h_workbox" };

describe("connected remote hosts", () => {
	it("keeps two different machines connected at once", async () => {
		const closed: string[] = [];
		const registry = new RemoteRegistry(async (entry) => ({
			base: `http://127.0.0.1:7654/${entry.hostId}`,
			previewUrl: (_sessionId, sourceUrl) => sourceUrl,
			resolvePreviewUrl: (_sessionId, viewedUrl) => viewedUrl,
			close: async () => { closed.push(entry.url); },
		}));
		await registry.connect(workbox);
		await registry.connect({ hostId: "h_mini", label: "mini", url: "http://192.0.2.9:3011", password: "other" });
		expect(closed).toEqual([]);
		await registry.closeAll();
		expect(closed).toEqual([workbox.url, "http://192.0.2.9:3011"]);
	});

	it("closes an in-flight connection before app shutdown finishes", async () => {
		let finishStart!: (value: { base: string; previewUrl: (_sessionId: string, sourceUrl: string) => string; resolvePreviewUrl: (_sessionId: string, viewedUrl: string) => string; close: () => Promise<void> }) => void;
		let closeCount = 0;
		const registry = new RemoteRegistry(async () => new Promise((resolve) => { finishStart = resolve; }));
		const connecting = registry.connect(workbox);
		const shutdown = registry.closeAll();
		await Promise.resolve();
		finishStart({
			base: "http://127.0.0.1:7654/token",
			previewUrl: (_sessionId, sourceUrl) => sourceUrl,
			resolvePreviewUrl: (_sessionId, viewedUrl) => viewedUrl,
			close: async () => { closeCount++; },
		});
		await Promise.all([connecting, shutdown]);
		expect(closeCount).toBe(1);
		await expect(registry.connect(workbox)).rejects.toThrow(/closing/);
	});

	it("replaces a LAN proxy when the same host connects by Tailscale", async () => {
		const closed: string[] = [];
		const registry = new RemoteRegistry(async (entry) => ({
			base: `http://127.0.0.1:7654/${entry.url.includes("https") ? "tailscale" : "lan"}`,
			previewUrl: (_sessionId, sourceUrl) => sourceUrl,
			resolvePreviewUrl: (_sessionId, viewedUrl) => viewedUrl,
			close: async () => { closed.push(entry.url); },
		}));
		await registry.connect(workbox);
		const updated = await registry.connect({ ...workbox, url: "https://workbox.tailnet" });
		expect(updated).toEqual({
			hostId: "h_workbox",
			label: "workbox",
			url: "https://workbox.tailnet",
			base: "http://127.0.0.1:7654/tailscale",
		});
		expect(closed).toEqual([workbox.url]);
		await registry.closeAll();
		expect(closed).toEqual([workbox.url, "https://workbox.tailnet"]);
	});
	it("does not leave a proxy serving after a connect and disconnect overlap", async () => {
		let finishStart!: (value: { base: string; previewUrl: (_sessionId: string, sourceUrl: string) => string; resolvePreviewUrl: (_sessionId: string, viewedUrl: string) => string; close: () => Promise<void> }) => void;
		let startCount = 0;
		let closeCount = 0;
		const registry = new RemoteRegistry(async () => {
			startCount++;
			return new Promise((resolve) => {
				finishStart = resolve;
			});
		});
		const first = registry.connect(workbox);
		const second = registry.connect(workbox);
		const disconnect = registry.disconnect(workbox.url);
		await Promise.resolve();
		finishStart({
			base: "http://127.0.0.1:7654/token",
			previewUrl: (_sessionId, sourceUrl) => sourceUrl,
			resolvePreviewUrl: (_sessionId, viewedUrl) => viewedUrl,
			close: async () => { closeCount++; },
		});
		await Promise.all([first, second, disconnect]);
		expect(startCount).toBe(1);
		expect(closeCount).toBe(1);
	});
});

import { afterEach, describe, expect, it, vi } from "vitest";
import { createEngine } from "./code-highlight-engine";

class FakeWorker {
	static instances: FakeWorker[] = [];
	onmessage: ((event: MessageEvent) => void) | null = null;
	onerror: (() => void) | null = null;
	onmessageerror: (() => void) | null = null;
	postMessage = vi.fn();
	terminate = vi.fn();
	constructor() { FakeWorker.instances.push(this); }
}
const code = 'const message = "hello 👋";\n'.repeat(900);
async function setup() {
	vi.resetModules();
	FakeWorker.instances = [];
	vi.stubGlobal("Worker", FakeWorker);
	return import("./code-highlight");
}
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers(); });

describe("large code worker highlighting", () => {
	it("starts lazily, shares requests and caches exactly the engine's tree", async () => {
		const api = await setup();
		expect(api.canonicalLanguage("text")).toBeUndefined();
		await api.highlight("const x = 1", "javascript");
		expect(api.highlightSync("const y = 2", "javascript")).toBeDefined();
		expect(FakeWorker.instances).toHaveLength(0);
		const first = api.highlight(code, "javascript");
		const duplicate = api.highlight(code, "javascript");
		const worker = FakeWorker.instances[0];
		expect(worker).toBeDefined();
		expect(worker.postMessage).toHaveBeenCalledTimes(1);
		const request = worker.postMessage.mock.calls[0][0];
		const tree = createEngine().highlight(request.language, request.code);
		worker.onmessage!({ data: { id: request.id, tree } } as MessageEvent);
		expect(await first).toEqual(tree);
		expect(await duplicate).toBe(await first);
		expect(api.highlightSync(code, "javascript")).toBe(await first);
	});

	it("keeps text readable when workers are unsupported", async () => {
		const api = await setup();
		vi.stubGlobal("Worker", undefined);
		expect(await api.highlight(code, "javascript")).toBeUndefined();
	});

	it.each(["onerror", "onmessageerror"] as const)("settles all queued requests on %s and avoids restart loops", async (event) => {
		const api = await setup();
		const first = api.highlight(code, "javascript");
		const second = api.highlight(code + "\n", "javascript");
		const worker = FakeWorker.instances[0];
		worker[event]!();
		expect(await first).toBeUndefined();
		expect(await second).toBeUndefined();
		expect(worker.terminate).toHaveBeenCalledOnce();
		expect(await api.highlight(code, "javascript")).toBeUndefined();
		expect(FakeWorker.instances).toHaveLength(1);
	});

	it("bounds queued requests and recovers capacity as results arrive", async () => {
		const api = await setup();
		const requests = Array.from({ length: 32 }, (_, i) => api.highlight(code + i, "javascript"));
		const worker = FakeWorker.instances[0];
		expect(await api.highlight(code + "overflow", "javascript")).toBeUndefined();
		expect(worker.postMessage).toHaveBeenCalledTimes(32);
		const { id } = worker.postMessage.mock.calls[0][0];
		worker.onmessage!({ data: { id } } as MessageEvent);
		const replacement = api.highlight(code + "replacement", "javascript");
		expect(worker.postMessage).toHaveBeenCalledTimes(33);
		worker.onerror!();
		await Promise.all([...requests, replacement]);
	});

	it("cleans up a failed postMessage without rejecting the caller", async () => {
		const api = await setup();
		const pending = api.highlight(code, "javascript");
		const worker = FakeWorker.instances[0];
		worker.postMessage.mockImplementationOnce(() => { throw new Error("clone failed"); });
		expect(await api.highlight(code + "second", "javascript")).toBeUndefined();
		expect(await pending).toBeUndefined();
		expect(worker.terminate).toHaveBeenCalledOnce();
	});

	it("times out stuck parsing and bounds source queued for the worker", async () => {
		vi.useFakeTimers();
		const api = await setup();
		expect(await api.highlight("x".repeat(1_000_001), "javascript")).toBeUndefined();
		expect(FakeWorker.instances).toHaveLength(0);
		const pending = api.highlight(code, "javascript");
		await vi.advanceTimersByTimeAsync(10_000);
		expect(await pending).toBeUndefined();
		expect(FakeWorker.instances[0].terminate).toHaveBeenCalledOnce();
	});
});

it("the worker returns identical token trees and source text, including Unicode", async () => {
	vi.resetModules();
	const scope = { onmessage: null as ((event: MessageEvent) => void) | null, postMessage: vi.fn() };
	vi.stubGlobal("self", scope);
	await import("./code-highlight.worker");
	scope.onmessage!({ data: { id: 7, code, language: "javascript" } } as MessageEvent);
	const response = scope.postMessage.mock.calls[0][0];
	expect(response).toEqual({ id: 7, tree: createEngine().highlight("javascript", code) });
	function text(node: { type: string; value?: string; children?: unknown[] }): string {
		return node.type === "text" ? node.value! : (node.children ?? []).map((child) => text(child as typeof node)).join("");
	}
	expect(text(response.tree)).toBe(code);
	scope.onmessage!({ data: { id: 8, code, language: "unknown" } } as MessageEvent);
	expect(scope.postMessage.mock.calls[1][0]).toEqual({ id: 8, tree: undefined });
});

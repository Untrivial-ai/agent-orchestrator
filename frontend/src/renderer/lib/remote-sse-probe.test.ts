import { afterEach, expect, it, vi } from "vitest";
import { probeRemoteSse } from "./remote-sse-probe";

class StubEventSource extends EventTarget {
	static instances: StubEventSource[] = [];
	onmessage: ((event: MessageEvent) => void) | null = null;
	onerror: (() => void) | null = null;
	closed = false;
	constructor(readonly url: string) {
		super();
		StubEventSource.instances.push(this);
	}
	close() { this.closed = true; }
	frame(name: string) { this.dispatchEvent(new MessageEvent(name, { data: "{}" })); }
}

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
	StubEventSource.instances = [];
});

it("does not trust an opened but buffered stream; falls back once after the probe timeout", () => {
	vi.useFakeTimers();
	vi.stubGlobal("EventSource", StubEventSource);
	const verified = vi.fn();
	const failed = vi.fn();
	probeRemoteSse("https://host/events", ["changed"], vi.fn(), verified, failed);
	const source = StubEventSource.instances[0];
	expect(source.url).toBe("https://host/events");
	vi.advanceTimersByTime(15_000);
	expect(verified).not.toHaveBeenCalled();
	expect(failed).toHaveBeenCalledOnce();
	expect(source.closed).toBe(true);
	source.frame("ready");
	expect(verified).not.toHaveBeenCalled();
});

it("accepts a delivered frame and falls back if the verified stream fails", () => {
	vi.stubGlobal("EventSource", StubEventSource);
	const event = vi.fn();
	const verified = vi.fn();
	const failed = vi.fn();
	const close = probeRemoteSse("https://host/events", ["changed"], event, verified, failed);
	const source = StubEventSource.instances[0];
	source.frame("ready");
	source.frame("changed");
	expect(verified).toHaveBeenCalledOnce();
	expect(event).toHaveBeenCalledOnce();
	source.onerror?.();
	expect(failed).toHaveBeenCalledOnce();
	close();
	expect(source.closed).toBe(true);
});

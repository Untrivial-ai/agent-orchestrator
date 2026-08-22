import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MuxConnectionState, TerminalMux } from "../lib/terminal-mux";
import type { WorkspaceSession } from "../types/workspace";
import { useTerminalSession, type AttachableTerminal } from "./useTerminalSession";
import { workspaceQueryKey } from "./useWorkspaceQuery";

const session: WorkspaceSession = {
	id: "sess-1",
	terminalHandleId: "handle-1",
	workspaceId: "ws-1",
	workspaceName: "demo",
	title: "fix the tests",
	provider: "claude-code",
	branch: "main",
	status: "working",
	updatedAt: "now",
	prs: [],
};

type FakeMux = {
	mux: TerminalMux;
	opens: Array<[string, number, number]>;
	resizes: Array<[string, number, number]>;
	inputs: Array<[string, string]>;
	closes: string[];
	events: string[];
	disposed: boolean;
	emitData(id: string, text: string): void;
	emitOpened(id: string): void;
	emitExit(id: string): void;
	emitError(id: string, message: string): void;
	emitConnection(state: MuxConnectionState): void;
};

function subscribe<T>(map: Map<string, Set<T>>, id: string, listener: T): () => void {
	const set = map.get(id) ?? new Set<T>();
	set.add(listener);
	map.set(id, set);
	return () => set.delete(listener);
}

function createFakeMux(): FakeMux {
	const data = new Map<string, Set<(bytes: Uint8Array) => void>>();
	const exit = new Map<string, Set<() => void>>();
	const opened = new Map<string, Set<() => void>>();
	const error = new Map<string, Set<(message: string) => void>>();
	const connection = new Set<(state: MuxConnectionState) => void>();

	const fake: FakeMux = {
		opens: [],
		resizes: [],
		inputs: [],
		closes: [],
		events: [],
		disposed: false,
		mux: {
			open: (id, cols, rows) => fake.opens.push([id, cols, rows]),
			sendInput: (id, input) => fake.inputs.push([id, input]),
			resize: (id, cols, rows) => fake.resizes.push([id, cols, rows]),
			close: (id) => {
				fake.closes.push(id);
				fake.events.push(`close:${id}`);
			},
			onData: (id, listener) => subscribe(data, id, listener),
			onExit: (id, listener) => subscribe(exit, id, listener),
			onOpened: (id, listener) => subscribe(opened, id, listener),
			onError: (id, listener) => subscribe(error, id, listener),
			onConnectionChange: (listener) => {
				connection.add(listener);
				return () => connection.delete(listener);
			},
			dispose: () => {
				fake.disposed = true;
				fake.events.push("dispose");
			},
		},
		emitData: (id, text) => data.get(id)?.forEach((listener) => listener(new TextEncoder().encode(text))),
		emitOpened: (id) => opened.get(id)?.forEach((listener) => listener()),
		emitExit: (id) => exit.get(id)?.forEach((listener) => listener()),
		emitError: (id, message) => error.get(id)?.forEach((listener) => listener(message)),
		emitConnection: (state) => connection.forEach((listener) => listener(state)),
	};
	return fake;
}

type FakeTerminal = AttachableTerminal & {
	autoCompleteWrites: boolean;
	lines: string[];
	pendingWriteCallbacks: Array<() => void>;
	latestOutputRequests: number;
	typeKeys(data: string): void;
	paste(data: string): void;
	compose(data: string): void;
	shortcut(data: string): void;
	wheel(data: string): void;
	emitResize(cols: number, rows: number): void;
	completeWrites(): void;
};

function createFakeTerminal(): FakeTerminal {
	const inputListeners = new Set<Parameters<AttachableTerminal["onUserInput"]>[0]>();
	const resizeListeners = new Set<(size: { cols: number; rows: number }) => void>();
	const terminal: FakeTerminal = {
		cols: 80,
		rows: 24,
		focus: () => undefined,
		autoCompleteWrites: true,
		lines: [],
		pendingWriteCallbacks: [],
		latestOutputRequests: 0,
		// Mirrors xterm: the callback fires once the chunk has been parsed.
		write: (bytes, done) => {
			terminal.lines.push(new TextDecoder().decode(bytes));
			if (done) {
				if (terminal.autoCompleteWrites) done();
				else terminal.pendingWriteCallbacks.push(done);
			}
		},
		writeln: (line) => terminal.lines.push(line),
		showLatestOutput: () => {
			terminal.latestOutputRequests += 1;
		},
		prepareForActivation: async () => undefined,
		onUserInput: (listener) => {
			inputListeners.add(listener);
			return { dispose: () => inputListeners.delete(listener) };
		},
		onResize: (listener) => {
			resizeListeners.add(listener);
			return { dispose: () => resizeListeners.delete(listener) };
		},
		typeKeys: (data) => inputListeners.forEach((listener) => listener(data, "keyboard")),
		paste: (data) => inputListeners.forEach((listener) => listener(data, "paste")),
		compose: (data) => inputListeners.forEach((listener) => listener(data, "composition")),
		shortcut: (data) => inputListeners.forEach((listener) => listener(data, "shortcut")),
		wheel: (data) => inputListeners.forEach((listener) => listener(data, "wheel")),
		emitResize: (cols, rows) => resizeListeners.forEach((listener) => listener({ cols, rows })),
		completeWrites: () => {
			for (const callback of terminal.pendingWriteCallbacks.splice(0)) callback();
		},
	};
	return terminal;
}

function setup({
	coverInitialReplay = true,
	daemonReady = true,
	attachedSession = session as WorkspaceSession | undefined,
	isVisible = true,
	inputDisabled = false,
} = {}) {
	const muxes: FakeMux[] = [];
	const createMux = () => {
		const fake = createFakeMux();
		muxes.push(fake);
		return fake.mux;
	};
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries");
	const wrapper = ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
	);
	const initialProps: { daemonReady: boolean; isVisible?: boolean; inputDisabled?: boolean } = {
		daemonReady,
		isVisible,
		inputDisabled,
	};
	const view = renderHook(
		({ daemonReady: ready, isVisible: visible = true, inputDisabled: blocked = false }: { daemonReady: boolean; isVisible?: boolean; inputDisabled?: boolean }) =>
			useTerminalSession(attachedSession, {
				coverInitialReplay,
				daemonReady: ready,
				createMux,
				inputDisabled: blocked,
				isVisible: visible,
			}),
		{ initialProps, wrapper },
	);
	const terminal = createFakeTerminal();
	let detach: () => void = () => undefined;
	act(() => {
		detach = view.result.current.attach(terminal);
	});
	return { view, terminal, muxes, invalidateSpy, detach: () => detach() };
}

beforeEach(() => {
	vi.useFakeTimers();
});

afterEach(() => {
	vi.useRealTimers();
	vi.restoreAllMocks();
});

describe("useTerminalSession", () => {
	it("opens the pane at the terminal's size and reaches attached on the server ack", () => {
		const { view, muxes } = setup();
		expect(view.result.current.state).toBe("connecting");
		expect(muxes).toHaveLength(1);
		expect(muxes[0].opens).toEqual([["handle-1", 80, 24]]);
		act(() => muxes[0].emitOpened("handle-1"));
		expect(view.result.current.state).toBe("attached");
	});

	it("stays idle when the session has no terminal handle", () => {
		const { view, muxes } = setup({ attachedSession: { ...session, terminalHandleId: undefined } });
		expect(view.result.current.state).toBe("idle");
		expect(muxes).toHaveLength(0);
	});

	it("forwards PTY output, keystrokes, and resizes across the attachment", () => {
		const { terminal, muxes } = setup();
		act(() => muxes[0].emitOpened("handle-1"));
		act(() => muxes[0].emitData("handle-1", "hello"));
		// The first burst is held by the replay gate; it lands once the stream
		// goes quiet (see REPLAY_QUIET_MS).
		act(() => void vi.advanceTimersByTime(60));
		expect(terminal.lines).toContain("hello");
		terminal.typeKeys("ls\r");
		expect(muxes[0].inputs).toEqual([["handle-1", "ls\r"]]);
		terminal.paste("echo pasted\r");
		expect(muxes[0].inputs).toEqual([
			["handle-1", "ls\r"],
			["handle-1", "echo pasted\r"],
		]);
		terminal.emitResize(120, 40);
		act(() => void vi.advanceTimersByTime(100));
		expect(muxes[0].resizes).toContainEqual(["handle-1", 120, 40]);
	});

	it("forwards every explicit input source after the attachment opens", () => {
		const { terminal, muxes } = setup();
		act(() => muxes[0].emitOpened("handle-1"));

		terminal.typeKeys("a");
		terminal.paste("paste\r");
		terminal.compose("é");
		terminal.shortcut("\x1b[1;5D");
		terminal.wheel("\x1b[<64;1;1M");

		expect(muxes[0].inputs).toEqual([
			["handle-1", "a"],
			["handle-1", "paste\r"],
			["handle-1", "é"],
			["handle-1", "\x1b[1;5D"],
			["handle-1", "\x1b[<64;1;1M"],
		]);
	});

	it("keeps the attachment live but refuses input while a controller handoff owns it", () => {
		const { view, terminal, muxes } = setup({ inputDisabled: true });
		act(() => muxes[0].emitOpened("handle-1"));

		terminal.typeKeys("must not run\r");
		expect(muxes[0].inputs).toEqual([]);
		expect(view.result.current.state).toBe("attached");

		view.rerender({ daemonReady: true, isVisible: true, inputDisabled: false });
		terminal.typeKeys("safe now\r");
		expect(muxes[0].inputs).toEqual([["handle-1", "safe now\r"]]);
	});

	it("keeps receiving output while hidden without accepting input or resizing the PTY", () => {
		const { view, terminal, muxes } = setup();
		act(() => muxes[0].emitOpened("handle-1"));
		const initialResizes = muxes[0].resizes.length;

		// Queue resize work while visible, then park the terminal before the
		// debounce fires. Hiding must cancel the pending publication.
		terminal.emitResize(120, 40);
		view.rerender({ daemonReady: true, isVisible: false });
		terminal.typeKeys("hidden input");
		terminal.paste("hidden paste");
		terminal.emitResize(140, 50);
		act(() => muxes[0].emitData("handle-1", "output while hidden"));
		act(() => void vi.advanceTimersByTime(500));

		expect(muxes[0].inputs).toEqual([]);
		expect(muxes[0].resizes).toHaveLength(initialResizes);
		expect(terminal.lines).toContain("output while hidden");
		expect(muxes).toHaveLength(1);

		// The same attachment becomes interactive again on return.
		view.rerender({ daemonReady: true, isVisible: true });
		terminal.typeKeys("visible\r");
		terminal.emitResize(150, 55);
		act(() => void vi.advanceTimersByTime(100));
		expect(muxes[0].inputs).toEqual([["handle-1", "visible\r"]]);
		expect(muxes[0].resizes.slice(initialResizes)).toEqual([["handle-1", 150, 55]]);
	});

	it("publishes a locally refitted parked grid when the terminal becomes visible", () => {
		const { view, terminal, muxes } = setup();
		act(() => muxes[0].emitOpened("handle-1"));
		const initialResizes = muxes[0].resizes.length;

		view.rerender({ daemonReady: true, isVisible: false });
		// prepareForActivation refits xterm while resize forwarding is suppressed.
		// Model the resulting live getters without emitting onResize.
		terminal.cols = 132;
		terminal.rows = 47;
		view.rerender({ daemonReady: true, isVisible: true });
		act(() => view.result.current.syncVisibleSize(terminal.cols, terminal.rows));

		expect(muxes[0].resizes.slice(initialResizes)).toEqual([["handle-1", 132, 47]]);
	});

	it("collapses a drag's burst into one resize and does not re-send the settled grid", () => {
		const { terminal, muxes } = setup();
		const initialResizes = muxes[0].resizes.length;
		terminal.emitResize(100, 30);
		terminal.emitResize(110, 34);
		terminal.emitResize(120, 40);
		act(() => void vi.advanceTimersByTime(100));
		expect(muxes[0].resizes.slice(initialResizes)).toEqual([["handle-1", 120, 40]]);
		act(() => void vi.advanceTimersByTime(250));
		expect(muxes[0].resizes.slice(initialResizes)).toEqual([["handle-1", 120, 40]]);
	});

	it("deduplicates the same visible grid across independent synchronization paths", () => {
		const { terminal, muxes } = setup();
		const initialResizes = muxes[0].resizes.length;
		terminal.emitResize(120, 40);
		act(() => void vi.advanceTimersByTime(100));
		terminal.emitResize(120, 40);
		act(() => void vi.advanceTimersByTime(100));

		expect(muxes[0].resizes.slice(initialResizes)).toEqual([["handle-1", 120, 40]]);
	});

	it("does not forward input until the server opens the current attachment", () => {
		const { terminal, muxes } = setup();
		terminal.typeKeys("too early");
		expect(muxes[0].inputs).toEqual([]);
		act(() => muxes[0].emitOpened("handle-1"));
		terminal.typeKeys("ready\r");
		expect(muxes[0].inputs).toEqual([["handle-1", "ready\r"]]);
	});

	it("forwards each distinct settled grid once", () => {
		const { terminal, muxes } = setup();
		const initialResizes = muxes[0].resizes.length;
		terminal.emitResize(100, 30);
		act(() => void vi.advanceTimersByTime(100));
		terminal.emitResize(120, 40);
		act(() => void vi.advanceTimersByTime(100 + 250));
		expect(muxes[0].resizes.slice(initialResizes)).toEqual([
			["handle-1", 100, 30],
			["handle-1", 120, 40],
		]);
	});

	// Initial-replay gate (issue #3160). The pane must appear already drawn at
	// the tail; writing the burst frame-by-frame paints every intermediate
	// scroll position on the way down.
	describe("initial replay gate", () => {
		it("never reveals before xterm finishes parsing the buffered replay", () => {
			const { view, terminal, muxes } = setup();
			terminal.autoCompleteWrites = false;
			act(() => muxes[0].emitOpened("handle-1"));
			act(() => muxes[0].emitData("handle-1", "large replay"));
			act(() => void vi.advanceTimersByTime(60 + 750));
			expect(view.result.current.replaySettled).toBe(false);
			expect(terminal.latestOutputRequests).toBe(0);
			act(() => terminal.completeWrites());
			expect(view.result.current.replaySettled).toBe(true);
			expect(terminal.latestOutputRequests).toBe(1);
		});

		it("streams reviewer-style attachments without the replay gate", () => {
			const { view, terminal, muxes } = setup({ coverInitialReplay: false });
			expect(view.result.current.replaySettled).toBe(true);
			act(() => muxes[0].emitData("handle-1", "review output"));
			expect(terminal.lines).toEqual(["review output"]);
			expect(view.result.current.replaySettled).toBe(true);
		});

		it("keeps the parsed replay covered through a quiet tail, then reveals at latest", () => {
			const { view, terminal, muxes } = setup();
			expect(view.result.current.replaySettled).toBe(false);

			act(() => muxes[0].emitOpened("handle-1"));
			act(() => muxes[0].emitData("handle-1", "line one\r\n"));
			act(() => void vi.advanceTimersByTime(30));
			act(() => muxes[0].emitData("handle-1", "line two\r\n"));
			act(() => void vi.advanceTimersByTime(30));
			act(() => muxes[0].emitData("handle-1", "line three\r\n"));

			// Still buffered: each frame restarted the quiet window.
			expect(terminal.lines).toEqual([]);
			expect(view.result.current.replaySettled).toBe(false);

			act(() => void vi.advanceTimersByTime(60));
			expect(terminal.lines).toEqual(["line one\r\nline two\r\nline three\r\n"]);
			expect(view.result.current.replaySettled).toBe(false);
			expect(terminal.latestOutputRequests).toBe(0);

			act(() => void vi.advanceTimersByTime(180));
			expect(view.result.current.replaySettled).toBe(true);
			expect(terminal.latestOutputRequests).toBe(1);
		});

		it("restarts the hidden-tail window when a late replay frame arrives", () => {
			const { view, terminal, muxes } = setup();
			act(() => muxes[0].emitOpened("handle-1"));
			act(() => muxes[0].emitData("handle-1", "initial replay"));
			act(() => void vi.advanceTimersByTime(60)); // coalesced write lands

			act(() => void vi.advanceTimersByTime(170));
			act(() => muxes[0].emitData("handle-1", " late tail"));
			act(() => void vi.advanceTimersByTime(10)); // original tail deadline
			expect(view.result.current.replaySettled).toBe(false);
			expect(terminal.lines).toEqual(["initial replay", " late tail"]);

			act(() => void vi.advanceTimersByTime(170));
			expect(view.result.current.replaySettled).toBe(true);
			expect(terminal.latestOutputRequests).toBe(1);
		});

		it("streams live output straight through once the gate has closed", () => {
			const { terminal, muxes } = setup();
			act(() => muxes[0].emitOpened("handle-1"));
			act(() => muxes[0].emitData("handle-1", "replay"));
			act(() => void vi.advanceTimersByTime(60 + 180));

			act(() => muxes[0].emitData("handle-1", "live-1"));
			expect(terminal.lines).toEqual(["replay", "live-1"]);
			act(() => muxes[0].emitData("handle-1", "live-2"));
			expect(terminal.lines).toEqual(["replay", "live-1", "live-2"]);
		});

		it("reveals a pane that replays nothing instead of holding the cover to the cap", () => {
			const { view, muxes } = setup();
			act(() => muxes[0].emitOpened("handle-1"));

			// A pane that has produced nothing has no walk to hide, so the cover
			// must come off on the first-byte grace rather than the full cap —
			// otherwise the pane sits blank behind a "Loading latest output…"
			// label with nothing to load.
			act(() => void vi.advanceTimersByTime(250));
			expect(view.result.current.replaySettled).toBe(true);

			act(() => void vi.advanceTimersByTime(500));
			expect(view.result.current.replaySettled).toBe(true);
		});

		it("keeps coalescing after an empty-replay reveal, so a late burst still lands as one write", () => {
			const { terminal, muxes } = setup();
			act(() => muxes[0].emitOpened("handle-1"));

			// Uncovering early must NOT end the gate. Flushing an empty buffer here
			// would clear replayBuffering and leave every later frame to go straight
			// to xterm one at a time — the frame-by-frame walk this gate exists to
			// remove, now with no cover over it either.
			act(() => void vi.advanceTimersByTime(250));

			act(() => muxes[0].emitData("handle-1", "late one "));
			act(() => muxes[0].emitData("handle-1", "late two "));
			act(() => muxes[0].emitData("handle-1", "late three"));
			expect(terminal.lines).toEqual([]);

			act(() => void vi.advanceTimersByTime(60));
			expect(terminal.lines).toEqual(["late one late two late three"]);
		});

		it("flushes at the cap but keeps late replay frames covered until quiet", () => {
			const { view, terminal, muxes } = setup();
			act(() => muxes[0].emitOpened("handle-1"));
			// A stream trickling faster than the first quiet window would keep the
			// coalesced buffer growing. The cap flushes it during chunk-18's gap;
			// chunk-19 streams into xterm but remains behind the same cover.
			for (let i = 0; i < 20; i += 1) {
				act(() => muxes[0].emitData("handle-1", `chunk-${i} `));
				act(() => void vi.advanceTimersByTime(40));
			}
			expect(view.result.current.replaySettled).toBe(false);
			act(() => void vi.advanceTimersByTime(180));
			expect(view.result.current.replaySettled).toBe(true);
			// Everything buffered up to the cap goes out as one write; the late tail
			// is a separate write, but neither intermediate state is exposed.
			expect(terminal.lines).toHaveLength(2);
			expect(terminal.lines[0]).toContain("chunk-0 ");
			expect(terminal.lines[0]).toContain("chunk-18 ");
			expect(terminal.lines[1]).toBe("chunk-19 ");
		});

		it("bounds the hidden tail when the worker is continuously producing output", () => {
			const { view, terminal, muxes } = setup();
			act(() => muxes[0].emitOpened("handle-1"));

			for (let i = 0; i < 40; i += 1) {
				act(() => muxes[0].emitData("handle-1", `chunk-${i} `));
				act(() => void vi.advanceTimersByTime(40));
			}

			// The first cap ends buffering at 750ms; the tail cap reveals at
			// 1500ms even though the worker never produced a quiet window.
			expect(view.result.current.replaySettled).toBe(true);
			expect(terminal.latestOutputRequests).toBe(1);
		});

		it("lands buffered output and lifts the cover when the pane exits mid-replay", () => {
			const { view, terminal, muxes } = setup();
			act(() => muxes[0].emitOpened("handle-1"));
			act(() => muxes[0].emitData("handle-1", "half a replay"));
			act(() => muxes[0].emitExit("handle-1"));
			expect(view.result.current.replaySettled).toBe(true);
			expect(terminal.lines[0]).toBe("half a replay");
			expect(terminal.lines.some((line) => line.includes("[process exited]"))).toBe(true);
		});

		it("lands buffered output and lifts the cover when the pane errors mid-replay", () => {
			const { view, terminal, muxes } = setup();
			act(() => muxes[0].emitOpened("handle-1"));
			act(() => muxes[0].emitData("handle-1", "half a replay"));
			act(() => muxes[0].emitError("handle-1", "pane died"));
			expect(view.result.current.replaySettled).toBe(true);
			expect(terminal.lines[0]).toBe("half a replay");
			expect(terminal.lines.some((line) => line.includes("[terminal error] pane died"))).toBe(true);
		});

		it("re-arms the gate on reattach so the replayed screen is covered again", () => {
			const { view, terminal, muxes } = setup();
			act(() => muxes[0].emitOpened("handle-1"));
			act(() => muxes[0].emitData("handle-1", "first"));
			act(() => void vi.advanceTimersByTime(60 + 180));
			expect(view.result.current.replaySettled).toBe(true);

			act(() => muxes[0].emitConnection("closed"));
			act(() => void vi.advanceTimersByTime(500));
			expect(muxes).toHaveLength(2);
			expect(view.result.current.replaySettled).toBe(false);

			act(() => muxes[1].emitOpened("handle-1"));
			act(() => muxes[1].emitData("handle-1", "second"));
			act(() => void vi.advanceTimersByTime(60 + 180));
			expect(view.result.current.replaySettled).toBe(true);
			expect(terminal.lines).toContain("second");
		});

		it("does not duplicate a settled resize while replay is in flight", () => {
			const { terminal, muxes } = setup();
			const initial = muxes[0].resizes.length;
			act(() => muxes[0].emitOpened("handle-1"));

			// The resize debounce (100ms) outlasts the quiet window (60ms), so the
			// deferral only engages when the replay is genuinely still streaming
			// when the resize settles — the long-replay case this protects.
			terminal.emitResize(120, 40);
			for (let elapsed = 0; elapsed < 150; elapsed += 30) {
				act(() => muxes[0].emitData("handle-1", "x"));
				act(() => void vi.advanceTimersByTime(30));
			}
			expect(muxes[0].resizes.slice(initial)).toEqual([["handle-1", 120, 40]]);

			act(() => void vi.advanceTimersByTime(30)); // quiet window elapses -> flush
			expect(muxes[0].resizes.slice(initial)).toEqual([["handle-1", 120, 40]]);

			act(() => void vi.advanceTimersByTime(250));
			expect(muxes[0].resizes.slice(initial)).toEqual([["handle-1", 120, 40]]);
		});

		it("keeps the gate armed when the server acks slowly, instead of spending the cap on the handshake", () => {
			const { view, terminal, muxes } = setup();

			// The daemon probes liveness and spawns the runtime client before the
			// first byte — OPEN_TIMEOUT_MS budgets 3s for it. If the cap ran from
			// connect() it would expire here, flush nothing, and leave the gate
			// permanently off, so the replay would land frame-by-frame.
			act(() => void vi.advanceTimersByTime(1200));
			act(() => muxes[0].emitOpened("handle-1"));

			act(() => muxes[0].emitData("handle-1", "one "));
			act(() => muxes[0].emitData("handle-1", "two "));
			act(() => muxes[0].emitData("handle-1", "three"));
			expect(terminal.lines).toEqual([]);

			act(() => void vi.advanceTimersByTime(60 + 180));
			expect(terminal.lines).toEqual(["one two three"]);
			expect(view.result.current.replaySettled).toBe(true);
		});

		it("ends the gate as soon as the user types, so their echo is not held behind the cover", () => {
			const { view, terminal, muxes } = setup();
			act(() => muxes[0].emitOpened("handle-1"));
			act(() => muxes[0].emitData("handle-1", "prompt$ "));
			expect(view.result.current.replaySettled).toBe(false);

			act(() => terminal.typeKeys("l"));

			expect(view.result.current.replaySettled).toBe(true);
			expect(terminal.lines).toEqual(["prompt$ "]);
			expect(muxes[0].inputs).toEqual([["handle-1", "l"]]);
			// Subsequent output is live, so the echo shows up immediately.
			act(() => muxes[0].emitData("handle-1", "l"));
			expect(terminal.lines).toEqual(["prompt$ ", "l"]);
		});

		it("does not resend a stale grid after a newer resize supersedes it", () => {
			const { terminal, muxes } = setup();
			const initial = muxes[0].resizes.length;
			act(() => muxes[0].emitOpened("handle-1"));

			// Resize A settles while a burst is in flight.
			terminal.emitResize(120, 40);
			for (let elapsed = 0; elapsed < 150; elapsed += 30) {
				act(() => muxes[0].emitData("handle-1", "x"));
				act(() => void vi.advanceTimersByTime(30));
			}
			expect(muxes[0].resizes.slice(initial)).toEqual([["handle-1", 120, 40]]);

			// The user keeps dragging: B must supersede A as the final grid.
			terminal.emitResize(100, 30);
			act(() => void vi.advanceTimersByTime(100)); // B settles
			act(() => void vi.advanceTimersByTime(300)); // replay flushes

			// A's stale 120x40 must never be sent again — landing it after B would
			// cost a SIGWINCH repaint at the wrong size just as the cover lifts.
			expect(muxes[0].resizes.slice(initial)).toEqual([
				["handle-1", 120, 40],
				["handle-1", 100, 30],
			]);
		});

		it("flushes early once the buffered burst passes the byte cap", () => {
			const { view, terminal, muxes } = setup();
			act(() => muxes[0].emitOpened("handle-1"));

			// Attaching to a session that is actively spewing output: frames keep
			// restarting the quiet window, so time-based bounds alone would hold
			// everything until the cap and then land it as one multi-MB parse —
			// a visible main-thread freeze, worse than the scroll being fixed.
			const chunk = "x".repeat(64 * 1024);
			for (let i = 0; i < 24; i += 1) {
				act(() => muxes[0].emitData("handle-1", chunk));
				act(() => void vi.advanceTimersByTime(5));
			}

			expect(view.result.current.replaySettled).toBe(false);
			act(() => void vi.advanceTimersByTime(180));
			expect(view.result.current.replaySettled).toBe(true);
			// Never hand xterm one unbounded parser task, even when the replay gate
			// collected a full megabyte before flushing.
			for (const line of terminal.lines) {
				expect(line.length).toBeLessThanOrEqual(256 * 1024);
			}
		});

		it("preserves wire order when output arrives between bounded replay writes", () => {
			const { terminal, muxes } = setup();
			act(() => muxes[0].emitOpened("handle-1"));
			const replay = "a".repeat(300 * 1024);
			act(() => muxes[0].emitData("handle-1", replay));

			// The quiet flush writes the first bounded batch and yields before the
			// remainder. A live PTY frame arriving in that yield must queue behind it.
			act(() => void vi.advanceTimersByTime(60));
			expect(terminal.lines).toEqual(["a".repeat(256 * 1024)]);
			act(() => muxes[0].emitData("handle-1", "TAIL"));
			act(() => void vi.runOnlyPendingTimers());

			expect(terminal.lines.join("")).toBe(`${replay}TAIL`);
		});

		it("queues an unfinished replay before an exit marker and attachment teardown", () => {
			const { terminal, muxes } = setup();
			act(() => muxes[0].emitOpened("handle-1"));
			const replay = "a".repeat(300 * 1024);
			act(() => muxes[0].emitData("handle-1", replay));
			act(() => void vi.advanceTimersByTime(60));

			// Exit lands while the next replay batch is waiting for its event-loop
			// turn. The unwritten remainder must be submitted before the marker and
			// before teardown invalidates this attachment's generation.
			act(() => muxes[0].emitExit("handle-1"));
			const output = terminal.lines.join("");
			expect(output.startsWith(replay)).toBe(true);
			expect(output).toContain("[process exited]");
		});

		it("lifts the cover when the attachment is torn down with no reconnect scheduled", () => {
			const { view, muxes } = setup({ daemonReady: true });
			expect(view.result.current.replaySettled).toBe(false);
			expect(muxes).toHaveLength(1);

			// Daemon goes away before the server ever acks the open.
		view.rerender({ daemonReady: false, isVisible: true });

			// Well past REPLAY_CAP_MS and REPLAY_FIRST_BYTE_MS, and still covered:
			// both are armed from `opened`, so neither is running. This is what
			// fails if either one is ever moved back to connect().
			act(() => void vi.advanceTimersByTime(2_999));
			expect(view.result.current.replaySettled).toBe(false);

			act(() => void vi.advanceTimersByTime(1)); // OPEN_TIMEOUT_MS

			// The cap plays no part here: it is armed from `opened`, which never
			// fired, so no replay timer ever existed. What lifts the cover is
			// teardownMux, which settles unconditionally — and since
			// scheduleReattach bailed out (no daemon ready), nothing re-arms the
			// gate afterwards. That is the whole guarantee for a pane whose server
			// never acks, so pin the timing rather than just the end state.
			expect(view.result.current.state).toBe("reattaching");
			expect(view.result.current.replaySettled).toBe(true);
		});

		it("lifts the cover when the socket dies before the server ever acks", () => {
			const { view, muxes } = setup();
			expect(view.result.current.replaySettled).toBe(false);

			// The cap is armed from `opened`, so it never started. The `closed`
			// handler clears the open timeout and scheduleReattach declines to
			// retry while the daemon is down — leaving no timer to lift the cover.
		view.rerender({ daemonReady: false, isVisible: true });
			act(() => muxes[0].emitConnection("closed"));
			act(() => void vi.advanceTimersByTime(120_000));

			expect(view.result.current.state).toBe("reattaching");
			expect(view.result.current.replaySettled).toBe(true);
		});

		it("does not hold the cover down across a reconnect storm", () => {
			const { view, muxes } = setup();
			for (let cycle = 0; cycle < 6; cycle += 1) {
				const latest = muxes[muxes.length - 1];
				act(() => latest.emitConnection("closed"));
				expect(view.result.current.replaySettled).toBe(true);
				act(() => void vi.advanceTimersByTime(10_000)); // outlast the backoff
			}
			expect(muxes.length).toBeGreaterThan(1);
		});

		it("lands buffered bytes on teardown rather than discarding them", () => {
			const { terminal, muxes } = setup();
			act(() => muxes[0].emitOpened("handle-1"));
			act(() => muxes[0].emitData("handle-1", "https://example.com/pr/1"));

			// Socket drops mid-burst and the backoff reconnects. Bytes already
			// received must still reach the terminal rather than being discarded.
			act(() => muxes[0].emitConnection("closed"));
			act(() => void vi.advanceTimersByTime(500));

			expect(muxes).toHaveLength(2);
			expect(terminal.lines).toContain("https://example.com/pr/1");
		});

		it("drops a superseded connection's frames instead of folding them into the new replay", () => {
			const { view, terminal, muxes } = setup();
			act(() => muxes[0].emitOpened("handle-1"));
			act(() => muxes[0].emitConnection("closed"));
			act(() => void vi.advanceTimersByTime(500));
			expect(muxes).toHaveLength(2);

			// A late frame from the dead socket must not be concatenated into the
			// new attachment's replay, nor uncover it.
			act(() => muxes[0].emitData("handle-1", "stale"));
			act(() => muxes[1].emitData("handle-1", "fresh"));
			act(() => void vi.advanceTimersByTime(60 + 180));

			expect(terminal.lines).toEqual(["fresh"]);
			expect(view.result.current.replaySettled).toBe(true);
		});
	});

	it("marks exit in the terminal and refetches workspace state instead of writing status", () => {
		const { view, terminal, muxes, invalidateSpy } = setup();
		act(() => muxes[0].emitExit("handle-1"));
		expect(view.result.current.state).toBe("exited");
		expect(terminal.lines.some((line) => line.includes("[process exited]"))).toBe(true);
		expect(muxes[0].disposed).toBe(true);
		expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: workspaceQueryKey });
	});

	it("reconnects when a merged terminated session is restored with the same terminal handle", () => {
		const muxes: FakeMux[] = [];
		const createMux = () => {
			const fake = createFakeMux();
			muxes.push(fake);
			return fake.mux;
		};
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const wrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const view = renderHook(
			({ attachedSession }) => useTerminalSession(attachedSession, { daemonReady: true, createMux }),
			{ initialProps: { attachedSession: session }, wrapper },
		);
		const terminal = createFakeTerminal();
		act(() => {
			view.result.current.attach(terminal);
		});
		act(() => muxes[0].emitOpened("handle-1"));
		act(() => muxes[0].emitExit("handle-1"));
		expect(view.result.current.state).toBe("exited");

		view.rerender({
			attachedSession: { ...session, status: "merged", isTerminated: true, updatedAt: "terminated" },
		});
		expect(muxes).toHaveLength(1);

		view.rerender({
			attachedSession: { ...session, status: "merged", isTerminated: false, updatedAt: "restored" },
		});
		expect(view.result.current.state).toBe("connecting");
		expect(muxes).toHaveLength(2);
		expect(muxes[0].disposed).toBe(true);
		expect(muxes[1].opens).toEqual([["handle-1", 80, 24]]);
		act(() => muxes[1].emitOpened("handle-1"));
		expect(view.result.current.state).toBe("attached");
		terminal.typeKeys("echo ok\r");
		expect(muxes[1].inputs).toEqual([["handle-1", "echo ok\r"]]);
	});

	it("reconnects when an exited agent is resumed with the same terminal handle", () => {
		const muxes: FakeMux[] = [];
		const createMux = () => {
			const fake = createFakeMux();
			muxes.push(fake);
			return fake.mux;
		};
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const wrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const exitedSession: WorkspaceSession = {
			...session,
			status: "exited",
			activity: { state: "exited", lastActivityAt: "exited" },
		};
		const resumedSession: WorkspaceSession = {
			...exitedSession,
			status: "review_pending",
			activity: { state: "idle", lastActivityAt: "resumed" },
		};
		const view = renderHook(
			({ attachedSession }) => useTerminalSession(attachedSession, { daemonReady: true, createMux }),
			{ initialProps: { attachedSession: exitedSession }, wrapper },
		);
		const terminal = createFakeTerminal();
		act(() => {
			view.result.current.attach(terminal);
		});
		act(() => muxes[0].emitOpened("handle-1"));
		act(() => muxes[0].emitExit("handle-1"));
		expect(view.result.current.state).toBe("exited");

		view.rerender({ attachedSession: resumedSession });

		expect(view.result.current.state).toBe("connecting");
		expect(muxes).toHaveLength(2);
		expect(muxes[0].disposed).toBe(true);
		expect(muxes[1].opens).toEqual([["handle-1", 80, 24]]);
	});

	it("does not reconnect a broken live pane on ordinary session updates", () => {
		const muxes: FakeMux[] = [];
		const createMux = () => {
			const fake = createFakeMux();
			muxes.push(fake);
			return fake.mux;
		};
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const wrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
		const view = renderHook(
			({ attachedSession }) => useTerminalSession(attachedSession, { daemonReady: true, createMux }),
			{ initialProps: { attachedSession: session }, wrapper },
		);
		const terminal = createFakeTerminal();
		act(() => {
			view.result.current.attach(terminal);
		});
		act(() => muxes[0].emitError("handle-1", "no such pane"));
		expect(view.result.current.state).toBe("error");

		view.rerender({ attachedSession: { ...session, status: "idle", updatedAt: "tick-1" } });
		view.rerender({ attachedSession: { ...session, status: "working", updatedAt: "tick-2" } });

		expect(view.result.current.state).toBe("error");
		expect(muxes).toHaveLength(1);
	});

	it("surfaces pane errors and refetches, with no automatic retry", () => {
		const { view, muxes, invalidateSpy } = setup();
		act(() => muxes[0].emitError("handle-1", "no such pane"));
		expect(view.result.current.state).toBe("error");
		expect(view.result.current.error).toBe("no such pane");
		expect(muxes[0].disposed).toBe(true);
		expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: workspaceQueryKey });
		act(() => muxes[0].emitConnection("closed"));
		act(() => void vi.advanceTimersByTime(60_000));
		expect(muxes).toHaveLength(1);
	});

	it("reattaches with a fresh mux after a socket drop", () => {
		const { view, muxes } = setup();
		act(() => muxes[0].emitOpened("handle-1"));
		act(() => muxes[0].emitConnection("closed"));
		expect(view.result.current.state).toBe("reattaching");
		act(() => void vi.advanceTimersByTime(500));
		expect(muxes).toHaveLength(2);
		expect(muxes[0].disposed).toBe(true);
		expect(muxes[1].opens).toEqual([["handle-1", 80, 24]]);
		act(() => muxes[1].emitOpened("handle-1"));
		expect(view.result.current.state).toBe("attached");
	});

	it("ignores stale frames after a reconnect starts", () => {
		const { view, terminal, muxes } = setup();
		act(() => muxes[0].emitConnection("closed"));
		act(() => void vi.advanceTimersByTime(500));
		expect(muxes).toHaveLength(2);
		act(() => muxes[0].emitOpened("handle-1"));
		act(() => muxes[0].emitData("handle-1", "stale"));
		expect(view.result.current.state).toBe("reattaching");
		expect(terminal.lines).not.toContain("stale");
		act(() => muxes[1].emitOpened("handle-1"));
		expect(view.result.current.state).toBe("attached");
	});

	it("hard-reconnects when the server never acknowledges open", () => {
		const { view, muxes } = setup();
		act(() => void vi.advanceTimersByTime(2_999));
		expect(muxes).toHaveLength(1);
		act(() => void vi.advanceTimersByTime(1));
		expect(view.result.current.state).toBe("reattaching");
		expect(muxes[0].disposed).toBe(true);
		act(() => void vi.advanceTimersByTime(500));
		expect(muxes).toHaveLength(2);
		expect(muxes[1].opens).toEqual([["handle-1", 80, 24]]);
		act(() => muxes[1].emitOpened("handle-1"));
		expect(view.result.current.state).toBe("attached");
	});

	it("backs off between failed reconnect attempts", () => {
		const { muxes } = setup();
		act(() => muxes[0].emitConnection("closed"));
		act(() => void vi.advanceTimersByTime(500)); // attempt 1 after 500ms
		expect(muxes).toHaveLength(2);
		act(() => muxes[1].emitConnection("closed"));
		act(() => void vi.advanceTimersByTime(500)); // attempt 2 needs 1000ms
		expect(muxes).toHaveLength(2);
		act(() => void vi.advanceTimersByTime(500));
		expect(muxes).toHaveLength(3);
	});

	it("waits for daemon readiness instead of retrying, then reconnects when it flips", () => {
		const { view, muxes } = setup({ daemonReady: false });
		expect(view.result.current.state).toBe("reattaching");
		act(() => void vi.advanceTimersByTime(60_000));
		expect(muxes).toHaveLength(0); // no initial attach or retries against a dead daemon
		view.rerender({ daemonReady: true, isVisible: true });
		expect(muxes).toHaveLength(1); // connects immediately, without backoff debt
		act(() => muxes[0].emitOpened("handle-1"));
		expect(view.result.current.state).toBe("attached");
	});

	it("detach disposes the mux, stops reattach, and returns to idle", () => {
		const { view, muxes, detach } = setup();
		act(() => detach());
		expect(view.result.current.state).toBe("idle");
		expect(muxes[0].disposed).toBe(true);
		expect(muxes[0].closes).toEqual(["handle-1"]);
		expect(muxes[0].events).toEqual(["close:handle-1", "dispose"]);
		act(() => muxes[0].emitConnection("closed"));
		act(() => void vi.advanceTimersByTime(60_000));
		expect(muxes).toHaveLength(1);
	});
});

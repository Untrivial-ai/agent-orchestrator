import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CloudBrowserSurface, mapCloudBrowserPoint, paintCloudBrowserFrame } from "./CloudBrowserSurface";

afterEach(() => {
	vi.useRealTimers();
	vi.restoreAllMocks();
	vi.unstubAllGlobals();
});

describe("mapCloudBrowserPoint", () => {
	it("maps the displayed canvas back to accepted frame coordinates", () => {
		expect(mapCloudBrowserPoint(300, 200, { left: 100, top: 50, width: 400, height: 300 }, 800, 600))
			.toEqual({ x: 400, y: 300 });
	});

	it("clamps pointer coordinates at the frame edge", () => {
		expect(mapCloudBrowserPoint(0, 1000, { left: 100, top: 50, width: 400, height: 300 }, 800, 600))
			.toEqual({ x: 0, y: 600 });
	});

	it("provides a composition focus sink and forwards dialog decisions", () => {
		const send = vi.fn(() => true);
		render(createElement(CloudBrowserSurface, { model: {
			snapshot: {
				status: "ready", frameUrl: "", frameWidth: 800, frameHeight: 600,
				frameSequence: 1, streamEpoch: 1, url: "https://example.test", title: "Example",
				tabs: [], activeTabId: "tab-1", owner: "idle", canOperate: true,
				canGoBack: false, canGoForward: false, isLoading: false, viewportPending: false,
				dialogOpen: true, dialogType: "prompt", dialogText: "Name?", dialogPrompt: "Ada",
				error: "",
				errorRequestId: "",
			},
			send,
			setViewport: vi.fn(),
			reportPaint: vi.fn(),
			retry: vi.fn(),
		} }));

		expect(screen.getByLabelText("Browser text input")).toBeInTheDocument();
		fireEvent.change(screen.getByDisplayValue("Ada"), { target: { value: "Grace" } });
		fireEvent.click(screen.getByRole("button", { name: "Accept" }));
		expect(send).toHaveBeenCalledWith({ type: "dialog", operation: "accept", text: "Grace" });
	});

	it("maps pointer, wheel, keyboard, and composition input while control is enabled", () => {
		vi.useFakeTimers();
		const send = vi.fn(() => true);
		render(createElement(CloudBrowserSurface, { model: {
			snapshot: {
				status: "ready", frameUrl: "", frameWidth: 800, frameHeight: 600,
				frameSequence: 1, streamEpoch: 1, url: "", title: "",
				tabs: [], activeTabId: "tab-1", owner: "idle", canOperate: true,
				canGoBack: false, canGoForward: false, isLoading: false, viewportPending: false,
				dialogOpen: false, dialogType: "", dialogText: "", dialogPrompt: "", error: "", errorRequestId: "",
			},
			send,
			setViewport: vi.fn(),
			reportPaint: vi.fn(),
			retry: vi.fn(),
		} }));
		const canvas = screen.getByLabelText("Cloud browser") as HTMLCanvasElement;
		canvas.setPointerCapture = vi.fn();
		vi.spyOn(canvas, "getBoundingClientRect").mockReturnValue({
			left: 100, top: 50, width: 400, height: 300, right: 500, bottom: 350,
			x: 100, y: 50, toJSON: () => undefined,
		});
		fireEvent.pointerDown(canvas, {
			clientX: 300, clientY: 200, button: 0, buttons: 1, pointerId: 7, detail: 1,
		});
		expect(send).toHaveBeenCalledWith(expect.objectContaining({
			type: "input", kind: "pointerDown", x: 400, y: 300, button: "left", buttons: 1,
		}));

		fireEvent.pointerMove(canvas, { clientX: 350, clientY: 250, buttons: 1 });
		vi.advanceTimersByTime(33);
		expect(send).toHaveBeenCalledWith(expect.objectContaining({ type: "input", kind: "pointerMove" }));
		fireEvent.wheel(canvas, { clientX: 300, clientY: 200, deltaX: 4, deltaY: 12 });
		vi.advanceTimersByTime(16);
		expect(send).toHaveBeenCalledWith(expect.objectContaining({
			type: "input", kind: "wheel", deltaX: 4, deltaY: 12,
		}));

		fireEvent.keyDown(canvas, { key: "a", code: "KeyA" });
		expect(send).toHaveBeenCalledWith(expect.objectContaining({
			type: "input", kind: "keyDown", key: "a", codeValue: "KeyA", text: "a",
		}));
		const textInput = screen.getByLabelText("Browser text input");
		fireEvent.compositionStart(textInput, { data: "n" });
		fireEvent.compositionUpdate(textInput, { data: "na" });
		fireEvent.compositionEnd(textInput, { data: "name" });
		expect(send).toHaveBeenCalledWith({ type: "input", kind: "compositionStart", text: "n" });
		expect(send).toHaveBeenCalledWith({ type: "input", kind: "compositionUpdate", text: "na" });
		expect(send).toHaveBeenCalledWith({ type: "input", kind: "compositionCommit", text: "name" });
	});

	it("shows a request ID and retries a fatal connection", () => {
		const retry = vi.fn();
		render(createElement(CloudBrowserSurface, { model: {
			snapshot: {
				status: "fatal", frameUrl: "", frameWidth: 0, frameHeight: 0,
				frameSequence: 0, streamEpoch: 0, url: "", title: "",
				tabs: [], activeTabId: "", owner: "idle", canOperate: false,
				canGoBack: false, canGoForward: false, isLoading: false, viewportPending: true,
				dialogOpen: false, dialogType: "", dialogText: "", dialogPrompt: "",
				error: "Browser viewer unavailable", errorRequestId: "request-1",
			},
			send: vi.fn(() => false),
			setViewport: vi.fn(),
			reportPaint: vi.fn(),
			retry,
		} }));

		expect(screen.getByText("Browser viewer unavailable")).toBeInTheDocument();
		expect(screen.getByText("Request ID: request-1")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Retry" }));
		expect(retry).toHaveBeenCalledOnce();
	});

	it("shows the usable empty browser instead of an indefinite startup message", () => {
		const snapshot = {
			status: "waiting" as const, frameUrl: "", frameWidth: 0, frameHeight: 0,
			frameSequence: 0, streamEpoch: 1, url: "about:blank", title: "",
			tabs: [{ id: "tab-1", url: "about:blank", title: "", active: true }],
			activeTabId: "tab-1", owner: "idle" as const, canOperate: true,
			canGoBack: false, canGoForward: false, isLoading: false, viewportPending: false,
			dialogOpen: false, dialogType: "", dialogText: "", dialogPrompt: "", error: "", errorRequestId: "",
		};
		render(createElement(CloudBrowserSurface, { model: {
			snapshot,
			send: vi.fn(() => true),
			setViewport: vi.fn(),
			reportPaint: vi.fn(),
			retry: vi.fn(),
		} }));

		expect(screen.getByText("Enter a URL or click one in the terminal.")).toBeInTheDocument();
		expect(screen.queryByText("Starting browser")).not.toBeInTheDocument();
	});

	it("keeps startup feedback when a nonblank remote page has not produced a frame", () => {
		render(createElement(CloudBrowserSurface, { model: {
			snapshot: {
				status: "waiting", frameUrl: "", frameWidth: 0, frameHeight: 0,
				frameSequence: 0, streamEpoch: 1, url: "https://example.test", title: "Example",
				tabs: [{ id: "tab-1", url: "https://example.test", title: "Example", active: true }],
				activeTabId: "tab-1", owner: "idle", canOperate: true,
				canGoBack: false, canGoForward: false, isLoading: true, viewportPending: false,
				dialogOpen: false, dialogType: "", dialogText: "", dialogPrompt: "", error: "", errorRequestId: "",
			},
			send: vi.fn(() => true),
			setViewport: vi.fn(),
			reportPaint: vi.fn(),
			retry: vi.fn(),
		} }));

		expect(screen.getByText("Starting browser")).toBeInTheDocument();
		expect(screen.queryByText("Enter a URL or click one in the terminal.")).not.toBeInTheDocument();
	});

	it("reports decode and paint timing only after drawing the accepted frame", async () => {
		const drawImage = vi.fn();
		const fetchFrame = vi.fn().mockResolvedValue({ blob: () => Promise.resolve(new Blob(["jpeg"])) });
		const decodeFrame = vi.fn().mockResolvedValue({ close: vi.fn() });
		vi.stubGlobal("fetch", fetchFrame);
		vi.stubGlobal("createImageBitmap", decodeFrame);
		vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({ drawImage } as unknown as CanvasRenderingContext2D);
		const canvas = document.createElement("canvas");
		const timestamps = [10, 18, 20];
		const timing = await paintCloudBrowserFrame(canvas, "blob:frame", 800, 600, () => false, () => timestamps.shift() ?? 20);

		expect(fetchFrame).toHaveBeenCalledWith("blob:frame");
		expect(decodeFrame).toHaveBeenCalledOnce();
		expect(drawImage).toHaveBeenCalledOnce();
		expect(timing).toEqual({ decodeMs: 8, paintMs: 2 });
		expect(canvas.width).toBe(800);
		expect(canvas.height).toBe(600);
	});

	it("paints a decoded frame while coalescing faster arrivals to the latest frame", async () => {
		const drawImage = vi.fn();
		const fetchFrame = vi.fn().mockResolvedValue({
			blob: () => Promise.resolve(new Blob(["jpeg"])),
		});
		const decodes: Array<(bitmap: { close: ReturnType<typeof vi.fn> }) => void> = [];
		const decodeFrame = vi.fn(() => new Promise<{ close: ReturnType<typeof vi.fn> }>((resolve) => {
			decodes.push(resolve);
		}));
		vi.stubGlobal("fetch", fetchFrame);
		vi.stubGlobal("createImageBitmap", decodeFrame);
		vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({ drawImage } as unknown as CanvasRenderingContext2D);
		const reportPaint = vi.fn();
		const snapshot = {
			status: "ready" as const, frameUrl: "blob:frame-1", frameWidth: 800, frameHeight: 600,
			frameSequence: 1, streamEpoch: 1, url: "", title: "",
			tabs: [], activeTabId: "tab-1", owner: "idle" as const, canOperate: true,
			canGoBack: false, canGoForward: false, isLoading: false, viewportPending: false,
			dialogOpen: false, dialogType: "", dialogText: "", dialogPrompt: "", error: "", errorRequestId: "",
		};
		const model = {
			snapshot,
			send: vi.fn(() => true),
			setViewport: vi.fn(),
			reportPaint,
			retry: vi.fn(),
		};
		const view = render(createElement(CloudBrowserSurface, { model }));
		await waitFor(() => expect(fetchFrame).toHaveBeenCalled());
		await waitFor(() => expect(decodeFrame).toHaveBeenCalledTimes(1));

		view.rerender(createElement(CloudBrowserSurface, {
			model: { ...model, snapshot: { ...snapshot, frameUrl: "blob:frame-2", frameSequence: 2 } },
		}));
		view.rerender(createElement(CloudBrowserSurface, {
			model: { ...model, snapshot: { ...snapshot, frameUrl: "blob:frame-3", frameSequence: 3 } },
		}));
		expect(fetchFrame).not.toHaveBeenCalledWith("blob:frame-2");

		await act(async () => decodes.shift()?.({ close: vi.fn() }));
		await waitFor(() => expect(fetchFrame).toHaveBeenCalledWith("blob:frame-3"));
		await act(async () => decodes.shift()?.({ close: vi.fn() }));
		await waitFor(() => expect(reportPaint).toHaveBeenLastCalledWith(3, expect.any(Number), expect.any(Number)));
		expect(fetchFrame).not.toHaveBeenCalledWith("blob:frame-2");
		expect(drawImage).toHaveBeenCalledTimes(2);
	});
});

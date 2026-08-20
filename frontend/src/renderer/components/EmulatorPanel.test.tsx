import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { StreamFrame, StreamState } from "../hooks/useIOSSimulator";
import { EmulatorPanel } from "./EmulatorPanel";

// The panel only mounts on macOS; force the platform check on for every case.
vi.mock("../lib/platform", () => ({
	isMacPlatform: () => true,
}));

const { inputMutate, startMutate, stopMutate, recheckMutate, pointerToFrame, isNearFrameEdge, hookState } = vi.hoisted(() => ({
	inputMutate: vi.fn(),
	startMutate: vi.fn(),
	stopMutate: vi.fn(),
	recheckMutate: vi.fn(),
	pointerToFrame: vi.fn(),
	isNearFrameEdge: vi.fn(),
	hookState: { current: undefined as unknown },
}));

vi.mock("../hooks/useIOSSimulator", () => ({
	useIOSSimulator: () => hookState.current,
}));

vi.mock("../lib/device-viewport", () => ({
	pointerToFrame: (...args: Parameters<typeof pointerToFrame>) => pointerToFrame(...args),
	isNearFrameEdge: (...args: Parameters<typeof isNearFrameEdge>) => isNearFrameEdge(...args),
}));

const query = <T,>(data: T | undefined, overrides: Record<string, unknown> = {}) => ({
	data,
	isPending: false,
	isSuccess: data !== undefined,
	status: data !== undefined ? "success" : "error",
	refetch: vi.fn(),
	...overrides,
});

const mutation = (overrides: Record<string, unknown> = {}) => ({
	mutate: vi.fn(),
	isPending: false,
	...overrides,
});

type StatusData = { state: string; name: string | null; error: string | null };
type ToolchainData = {
	xcodeDetected: boolean;
	guidanceWhyMissing: string | null;
	guidanceAppStoreURL: string | null;
};
type PermissionsData = { screenRecording: boolean; accessibility: boolean };
type QueryResult<T> = ReturnType<typeof query<T>>;
type MutationResult = ReturnType<typeof mutation>;

export type EmulatorMockHook = {
	status: QueryResult<StatusData>;
	devices: QueryResult<{ deviceId: string; name: string }[]>;
	toolchain: QueryResult<ToolchainData>;
	recheck: MutationResult;
	start: MutationResult;
	stop: MutationResult;
	screenshot: QueryResult<{ data: string; mimeType: string; width: number; height: number }>;
	streamFrame: StreamFrame | null;
	streamState: StreamState;
	streamError: string | null;
	permissions: QueryResult<PermissionsData>;
	input: MutationResult;
};

function buildHook(): EmulatorMockHook {
	return {
		status: query<StatusData>({ state: "Shutdown", name: "iPhone 15", error: null }),
		devices: query<{ deviceId: string; name: string }[]>([]),
		toolchain: query<ToolchainData>({ xcodeDetected: true, guidanceWhyMissing: null, guidanceAppStoreURL: null }),
		recheck: mutation({ mutate: recheckMutate }),
		start: mutation({ mutate: startMutate }),
		stop: mutation({ mutate: stopMutate }),
		screenshot: query(undefined),
		streamFrame: null,
		streamState: "idle",
		streamError: null,
		permissions: query<PermissionsData>({ screenRecording: true, accessibility: true }),
		input: mutation({ mutate: inputMutate }),
	};
}

function renderPanel(overrides: Partial<EmulatorMockHook> = {}) {
	hookState.current = { ...buildHook(), ...overrides };
	return render(<EmulatorPanel active />);
}

beforeEach(() => {
	inputMutate.mockReset();
	startMutate.mockReset();
	stopMutate.mockReset();
	recheckMutate.mockReset();
	pointerToFrame.mockReset();
	isNearFrameEdge.mockReset();
	vi.spyOn(window, "open").mockImplementation(() => null);
	hookState.current = buildHook();
});

afterEach(() => {
	vi.restoreAllMocks();
	vi.useRealTimers();
	document.body.innerHTML = "";
});

describe("EmulatorPanel states", () => {
	it("shows the booted device label and device frame", () => {
		renderPanel({
			status: query({ state: "Booted", name: "iPhone 15", error: null }),
			streamFrame: { data: "aGVsbG8=", mimeType: "image/png", width: 1179, height: 2556 },
		});
		expect(screen.getByText("iPhone 15")).toBeInTheDocument();
		expect(screen.getByTestId("emulator-frame")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Start" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Stop" })).toBeEnabled();
	});

	it("renders the stream frame payload in the device frame", () => {
		renderPanel({
			status: query({ state: "Booted", name: "iPhone 15", error: null }),
			streamFrame: { data: "aGVsbG8=", mimeType: "image/png", width: 1179, height: 2556 },
		});
		const frame = screen.getByTestId("emulator-frame");
		expect(frame).toHaveAttribute("src", "data:image/png;base64,aGVsbG8=");
		expect(frame).toHaveStyle({ "aspect-ratio": "1179 / 2556" });
	});

	it("falls back to the screenshot poll when the stream has not sent a frame", () => {
		renderPanel({
			status: query({ state: "Booted", name: "iPhone 15", error: null }),
			screenshot: query({ data: "c2NyZWVu", mimeType: "image/jpeg", width: 1179, height: 2556 }),
		});
		const frame = screen.getByTestId("emulator-frame");
		expect(frame).toHaveAttribute("src", "data:image/jpeg;base64,c2NyZWVu");
	});

	it("shows the connecting message while the stream is starting", () => {
		renderPanel({
			status: query({ state: "Booted", name: "iPhone 15", error: null }),
			streamState: "connecting",
			streamError: null,
		});
		expect(screen.getByText("Connecting to simulator…")).toBeInTheDocument();
	});

	it("surfaces the stream error when the stream stalls", () => {
		renderPanel({
			status: query({ state: "Booted", name: "iPhone 15", error: null }),
			streamState: "stalled",
			streamError: "ScreenCaptureKit stopped",
		});
		expect(screen.getByText("ScreenCaptureKit stopped")).toBeInTheDocument();
	});

	it("shows a generic message when the stream is down without a captured error", () => {
		renderPanel({
			status: query({ state: "Booted", name: "iPhone 15", error: null }),
			streamState: "stalled",
			streamError: null,
		});
		expect(screen.getByText("Simulator screen unavailable.")).toBeInTheDocument();
	});

	it("shows the booting message while a start is pending", () => {
		renderPanel({
			start: mutation({ mutate: startMutate, isPending: true }),
		});
		expect(screen.getByText("Booting simulator…")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Start" })).toBeDisabled();
	});

	it("surfaces the status error when the simulator cannot be reached", () => {
		renderPanel({
			status: query({ state: "Shutdown", name: "iPhone 15", error: "No booted device" }),
		});
		expect(screen.getByText("No booted device")).toBeInTheDocument();
	});

	it("shows the start prompt when nothing is running and no error is known", () => {
		renderPanel({
			status: query({ state: "Shutdown", name: null, error: null }),
		});
		expect(screen.getByText("No device started")).toBeInTheDocument();
		expect(screen.getByText("Start the simulator to see its screen.")).toBeInTheDocument();
	});
});

describe("EmulatorPanel dependencies", () => {
	it("advertises the missing Xcode toolchain with install and recheck actions", () => {
		renderPanel({
			toolchain: query({
				xcodeDetected: false,
				guidanceWhyMissing: "Install Xcode 15 or newer.",
				guidanceAppStoreURL: "https://apps.apple.com/app/xcode/id497799835",
			}),
		});
		expect(screen.getByText("Xcode is required to run the iOS Simulator.")).toBeInTheDocument();
		expect(screen.getByText("Install Xcode 15 or newer.")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Install Xcode" }));
		expect(window.open).toHaveBeenCalledWith(
			"https://apps.apple.com/app/xcode/id497799835",
			"_blank",
		);
		fireEvent.click(screen.getByRole("button", { name: "Recheck" }));
		expect(recheckMutate).toHaveBeenCalled();
	});

	it("lists each missing permission with its settings deep link", () => {
		renderPanel({
			permissions: query({ screenRecording: false, accessibility: false }),
		});
		expect(
			screen.getByText(
				"Grant Screen Recording and Accessibility access to AO in macOS System Settings to enable Simulator capture and input.",
			),
		).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Screen Recording" }));
		expect(window.open).toHaveBeenCalledWith(
			"x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
			"_blank",
		);
		fireEvent.click(screen.getByRole("button", { name: "Accessibility" }));
		expect(window.open).toHaveBeenCalledWith(
			"x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
			"_blank",
		);
	});
});

describe("EmulatorPanel input", () => {
	const booted = {
		status: query({ state: "Booted", name: "iPhone 15", error: null }),
		streamFrame: { data: "aGVsbG8=", mimeType: "image/png", width: 1179, height: 2556 },
	};

	it("maps a pointerdown/up inside the frame to a tap", () => {
		pointerToFrame.mockReturnValue({ x: 200, y: 400 });
		renderPanel(booted);
		const frame = screen.getByTestId("emulator-frame");
		fireEvent.mouseDown(frame, { clientX: 100, clientY: 150 });
		fireEvent.mouseUp(frame, { clientX: 103, clientY: 152 });
		expect(inputMutate).toHaveBeenCalledWith({ action: "tap", x: 200, y: 400 });
	});

	it("maps a longer pointerdown/up drag to a swipe with start and end points", () => {
		pointerToFrame
			.mockReturnValueOnce({ x: 30, y: 300 })
			.mockReturnValueOnce({ x: 600, y: 300 });
		renderPanel(booted);
		const frame = screen.getByTestId("emulator-frame");
		fireEvent.mouseDown(frame, { clientX: 10, clientY: 100 });
		fireEvent.mouseUp(frame, { clientX: 200, clientY: 100 });
		expect(inputMutate).toHaveBeenCalledWith({ action: "swipe", x: 30, y: 300, x2: 600, y2: 300 });
	});

	it("ignores pointer interactions that start or end outside the frame", () => {
		pointerToFrame
			.mockReturnValueOnce(null)
			.mockReturnValueOnce({ x: 10, y: 10 });
		renderPanel(booted);
		const frame = screen.getByTestId("emulator-frame");
		fireEvent.mouseDown(frame, { clientX: 5, clientY: 5 });
		fireEvent.mouseUp(frame, { clientX: 50, clientY: 50 });
		expect(inputMutate).not.toHaveBeenCalled();
	});

	it("clears an in-progress gesture when the pointer leaves the frame", () => {
		pointerToFrame.mockReturnValue({ x: 10, y: 10 });
		renderPanel(booted);
		const frame = screen.getByTestId("emulator-frame");
		fireEvent.mouseDown(frame, { clientX: 10, clientY: 10 });
		fireEvent.mouseLeave(frame);
		fireEvent.mouseUp(frame, { clientX: 50, clientY: 50 });
		expect(inputMutate).not.toHaveBeenCalled();
	});

	it("dispatches home, lock, and orientation shortcuts", () => {
		renderPanel(booted);
		fireEvent.click(screen.getByRole("button", { name: "Home" }));
		expect(inputMutate).toHaveBeenCalledWith({ action: "home" });
		fireEvent.click(screen.getByRole("button", { name: "Lock" }));
		expect(inputMutate).toHaveBeenCalledWith({ action: "lock" });
		fireEvent.click(screen.getByRole("button", { name: "Rotate Left" }));
		expect(inputMutate).toHaveBeenCalledWith({ action: "rotateLeft" });
		fireEvent.click(screen.getByRole("button", { name: "Rotate Right" }));
		expect(inputMutate).toHaveBeenCalledWith({ action: "rotateRight" });
	});

	it("sends typed text and clears the field", () => {
		renderPanel(booted);
		const field = screen.getByRole("textbox", { name: "Simulator text input" });
		fireEvent.change(field, { target: { value: "app store" } });
		fireEvent.submit(field.closest("form") as HTMLFormElement);
		expect(inputMutate).toHaveBeenCalledWith({ action: "text", text: "app store" });
		expect(field).toHaveValue("");
	});

	it("does not send empty text", () => {
		renderPanel(booted);
		const field = screen.getByRole("textbox", { name: "Simulator text input" });
		fireEvent.submit(field.closest("form") as HTMLFormElement);
		expect(inputMutate).not.toHaveBeenCalled();
	});

	it("dispatches enter and backspace keys", () => {
		renderPanel(booted);
		fireEvent.click(screen.getByRole("button", { name: "Enter" }));
		expect(inputMutate).toHaveBeenCalledWith({ action: "key", keyCode: 36 });
		fireEvent.click(screen.getByRole("button", { name: "Backspace" }));
		expect(inputMutate).toHaveBeenCalledWith({ action: "key", keyCode: 51 });
	});

	it("starts and stops the simulator from the header buttons", () => {
		const first = renderPanel();
		fireEvent.click(screen.getByRole("button", { name: "Start" }));
		expect(startMutate).toHaveBeenCalled();
		first.unmount();
		renderPanel(booted);
		fireEvent.click(screen.getByRole("button", { name: "Stop" }));
		expect(stopMutate).toHaveBeenCalled();
	});

	it("shows the live frame-rate badge while frames are arriving", () => {
		renderPanel(booted);
		expect(screen.getByTestId("emulator-fps")).toHaveTextContent("1 fps");
	});
});

describe("EmulatorPanel edge-band warning", () => {
	const booted = {
		status: query({ state: "Booted", name: "iPhone 15", error: null }),
		streamFrame: { data: "aGVsbG8=", mimeType: "image/png", width: 1179, height: 2556 },
	};

	it("warns when a swipe begins inside the device edge band", () => {
		vi.useFakeTimers();
		pointerToFrame
			.mockReturnValueOnce({ x: 2, y: 300 })
			.mockReturnValueOnce({ x: 600, y: 300 });
		isNearFrameEdge.mockReturnValue(true);
		renderPanel(booted);
		const frame = screen.getByTestId("emulator-frame");
		fireEvent.mouseDown(frame, { clientX: 10, clientY: 100 });
		fireEvent.mouseUp(frame, { clientX: 200, clientY: 100 });
		expect(inputMutate).toHaveBeenCalledWith({ action: "swipe", x: 2, y: 300, x2: 600, y2: 300 });
		expect(screen.getByTestId("emulator-edge-hint")).toBeInTheDocument();
	});

	it("does not warn for swipes that begin inside the frame", () => {
		pointerToFrame
			.mockReturnValueOnce({ x: 200, y: 300 })
			.mockReturnValueOnce({ x: 600, y: 300 });
		isNearFrameEdge.mockReturnValue(false);
		renderPanel(booted);
		const frame = screen.getByTestId("emulator-frame");
		fireEvent.mouseDown(frame, { clientX: 10, clientY: 100 });
		fireEvent.mouseUp(frame, { clientX: 200, clientY: 100 });
		expect(inputMutate).toHaveBeenCalledWith({ action: "swipe", x: 200, y: 300, x2: 600, y2: 300 });
		expect(screen.queryByTestId("emulator-edge-hint")).not.toBeInTheDocument();
	});
});

describe("EmulatorPanel physical keyboard", () => {
	const booted = {
		status: query({ state: "Booted", name: "iPhone 15", error: null }),
		streamFrame: { data: "aGVsbG8=", mimeType: "image/png", width: 1179, height: 2556 },
	};

	it("arms on a pointer interaction inside the pane", () => {
		renderPanel(booted);
		expect(screen.queryByTestId("emulator-keyboard-armed")).not.toBeInTheDocument();
		fireEvent.pointerDown(screen.getByTestId("emulator-frame"));
		expect(screen.getByTestId("emulator-keyboard-armed")).toBeInTheDocument();
	});

	it("forwards Home with ⌘⇧H and Lock with ⌘L when armed", () => {
		renderPanel(booted);
		fireEvent.pointerDown(screen.getByTestId("emulator-frame"));
		fireEvent.keyDown(window, { key: "H", metaKey: true, shiftKey: true });
		expect(inputMutate).toHaveBeenCalledWith({ action: "home" });
		fireEvent.keyDown(window, { key: "l", metaKey: true });
		expect(inputMutate).toHaveBeenCalledWith({ action: "lock" });
	});

	it("forwards rotate with ⌘←/⌘→ when armed", () => {
		renderPanel(booted);
		fireEvent.pointerDown(screen.getByTestId("emulator-frame"));
		fireEvent.keyDown(window, { key: "ArrowLeft", metaKey: true });
		expect(inputMutate).toHaveBeenCalledWith({ action: "rotateLeft" });
		fireEvent.keyDown(window, { key: "ArrowRight", metaKey: true });
		expect(inputMutate).toHaveBeenCalledWith({ action: "rotateRight" });
	});

	it("forwards printable text and control keys while armed", () => {
		renderPanel(booted);
		fireEvent.pointerDown(screen.getByTestId("emulator-frame"));
		fireEvent.keyDown(window, { key: "a" });
		expect(inputMutate).toHaveBeenCalledWith({ action: "text", text: "a" });
		fireEvent.keyDown(window, { key: "Enter" });
		expect(inputMutate).toHaveBeenCalledWith({ action: "key", keyCode: 36 });
		fireEvent.keyDown(window, { key: "Backspace" });
		expect(inputMutate).toHaveBeenCalledWith({ action: "key", keyCode: 51 });
		fireEvent.keyDown(window, { key: " " });
		expect(inputMutate).toHaveBeenCalledWith({ action: "key", keyCode: 49 });
	});

	it("does not forward keystrokes when the pane is not armed", () => {
		renderPanel(booted);
		fireEvent.keyDown(window, { key: "a" });
		fireEvent.keyDown(window, { key: "Enter" });
		expect(inputMutate).not.toHaveBeenCalled();
	});

	it("disarms on Escape and stops forwarding", () => {
		renderPanel(booted);
		fireEvent.pointerDown(screen.getByTestId("emulator-frame"));
		fireEvent.keyDown(window, { key: "Escape" });
		expect(screen.queryByTestId("emulator-keyboard-armed")).not.toBeInTheDocument();
		fireEvent.keyDown(window, { key: "a" });
		expect(inputMutate).not.toHaveBeenCalled();
	});

	it("disarms when a pointer interaction happens outside the pane", () => {
		renderPanel(booted);
		fireEvent.pointerDown(screen.getByTestId("emulator-frame"));
		expect(screen.getByTestId("emulator-keyboard-armed")).toBeInTheDocument();
		fireEvent.pointerDown(document.body);
		expect(screen.queryByTestId("emulator-keyboard-armed")).not.toBeInTheDocument();
	});

	it("does not hijack keystrokes aimed at AO's text fields", () => {
		renderPanel(booted);
		fireEvent.pointerDown(screen.getByTestId("emulator-frame"));
		const field = screen.getByRole("textbox", { name: "Simulator text input" });
		fireEvent.focus(field);
		fireEvent.keyDown(field, { key: "a" });
		expect(inputMutate).not.toHaveBeenCalled();
	});
});
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { aoBridge } from "../lib/bridge";
import type { LocalDeviceInventory } from "../../shared/local-device";
import { DevicePanel } from "./DevicePanel";

class FakeWebSocket extends EventTarget {
	static OPEN = 1;
	static instances: FakeWebSocket[] = [];
	binaryType = "blob";
	readyState = FakeWebSocket.OPEN;
	sent: unknown[] = [];
	constructor(readonly url: string) {
		super();
		FakeWebSocket.instances.push(this);
		queueMicrotask(() => this.dispatchEvent(new Event("open")));
	}
	send(value: unknown) { this.sent.push(value); }
	close() { this.readyState = 3; }
}

describe("DevicePanel", () => {
	beforeEach(() => {
		FakeWebSocket.instances = [];
		vi.stubGlobal("WebSocket", FakeWebSocket);
		vi.spyOn(aoBridge.device, "status").mockResolvedValue({
			sessionId: "s1",
			capabilities: [
				{ platform: "ios", available: true },
				{ platform: "android", available: false, code: "ANDROID_SDK_REQUIRED", message: "Install Android Studio" },
			],
		});
		vi.spyOn(aoBridge.device, "list").mockResolvedValue({
			sessionId: "s1",
			devices: [{ id: "ios-1", name: "iPhone 17", platform: "ios", kind: "simulator", booted: false, busy: false }],
		});
		vi.spyOn(aoBridge.device, "setupStatus").mockResolvedValue({
			sessionId: "s1",
			setups: [{ platform: "android", state: "idle", message: "Install Android Studio", progress: 0, requiredBytes: 12 * 2 ** 30, licenseUrl: "https://developer.android.com/studio/terms", licenseAccepted: false, cancelable: false, retryable: false }],
		});
	});

	it("requires license confirmation before starting managed setup", async () => {
		const setup = vi.spyOn(aoBridge.device, "setup").mockResolvedValue({ sessionId: "s1", setup: { platform: "android", state: "queued", progress: 0, licenseAccepted: true, cancelable: true, retryable: false } });
		render(<DevicePanel sessionId="s1" />);
		const button = await screen.findByRole("button", { name: "Set up Android" });
		expect(button).toBeDisabled();
		fireEvent.click(screen.getByRole("checkbox"));
		fireEvent.click(button);
		await waitFor(() => expect(setup).toHaveBeenCalledWith({ sessionId: "s1", platform: "android", action: "start", licenseAccepted: true }));
	});

	it("shows Xcode handoff activity and continues automatically after installation", async () => {
		vi.mocked(aoBridge.device.setupStatus)
			.mockResolvedValueOnce({
				sessionId: "s1",
				setups: [{ platform: "ios", state: "awaiting_action", message: "Waiting for the full Xcode app to finish installing.", progress: 0, licenseAccepted: true, cancelable: false, retryable: true }],
			})
			.mockResolvedValue({
				sessionId: "s1",
				setups: [{ platform: "ios", state: "idle", progress: 0, licenseAccepted: true, cancelable: false, retryable: false }],
			});
		const setup = vi.spyOn(aoBridge.device, "setup").mockResolvedValue({
			sessionId: "s1",
			setup: { platform: "ios", state: "queued", progress: 0, licenseAccepted: true, cancelable: true, retryable: false },
		});

		render(<DevicePanel sessionId="s1" />);

		expect(await screen.findByText("Waiting for the full Xcode app to finish installing.")).toBeInTheDocument();
		expect(screen.getByRole("progressbar", { name: "iOS setup progress" })).not.toHaveAttribute("aria-valuenow");
		expect(screen.getByRole("button", { name: "Retry setup" })).toBeEnabled();
		await waitFor(() => expect(setup).toHaveBeenCalledWith({ sessionId: "s1", platform: "ios", action: "retry", licenseAccepted: true }), { timeout: 2_500 });
	});

	afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

	it("shows independent setup state and opens a selected device", async () => {
		const command = vi.spyOn(aoBridge.device, "command").mockImplementation(async (input) => {
			if (input.action === "shutdown") return { sessionId: "s1", action: input.action };
			return { sessionId: "s1", action: input.action, attachment, result: { streamBaseUrl: "http://127.0.0.1:3015/api/v1/devices/stream/ticket" } };
		});
		const attachment = { sessionId: "s1", deviceId: "ios-1", platform: "ios" as const, name: "iPhone 17" };
		const view = render(<DevicePanel sessionId="s1" />);

		expect(await screen.findByText("iPhone 17")).toBeInTheDocument();
		expect(screen.getByText("Install Android Studio")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Open device" }));

		await waitFor(() => expect(command).toHaveBeenCalledWith({ sessionId: "s1", action: "open", deviceId: "ios-1", platform: "ios" }));
		expect(await screen.findByAltText("Live screen for iPhone 17")).toHaveAttribute("src", "http://127.0.0.1:3015/api/v1/devices/stream/ticket/mjpeg");
		expect(screen.getByRole("button", { name: "Type text" }).querySelector(".lucide-send-horizontal")).not.toBeNull();
		expect(screen.getByRole("button", { name: "Press Enter" }).querySelector(".lucide-corner-down-left")).not.toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "Home" }));
		await waitFor(() => expect(FakeWebSocket.instances[0]?.sent.length).toBeGreaterThan(2));
		fireEvent.click(screen.getByRole("button", { name: "Power off device" }));
		await waitFor(() => expect(command).toHaveBeenCalledWith({ sessionId: "s1", action: "shutdown", confirmed: true }));
		view.unmount();
	});

	it("maps short pointer gestures to taps and drags to swipes", async () => {
		const attachment = { sessionId: "s1", deviceId: "ios-1", platform: "ios" as const, name: "iPhone 17" };
		vi.spyOn(aoBridge.device, "command").mockImplementation(async (input) => ({
			sessionId: "s1",
			action: input.action,
			attachment,
			result: { streamBaseUrl: "http://127.0.0.1:3015/api/v1/devices/stream/ticket" },
		}));
		render(<DevicePanel sessionId="s1" />);
		fireEvent.click(await screen.findByRole("button", { name: "Open device" }));
		const image = await screen.findByAltText("Live screen for iPhone 17");
		Object.defineProperty(image, "setPointerCapture", { configurable: true, value: vi.fn() });
		vi.spyOn(image, "getBoundingClientRect").mockReturnValue({
			bottom: 1_000, height: 1_000, left: 0, right: 500, top: 0, width: 500, x: 0, y: 0, toJSON: () => ({}),
		});

		fireEvent.pointerDown(image, { pointerId: 1, clientX: 100, clientY: 200 });
		fireEvent.pointerUp(image, { pointerId: 1, clientX: 102, clientY: 202 });
		await waitFor(() => expect(FakeWebSocket.instances[0]?.sent.length).toBeGreaterThanOrEqual(4));

		fireEvent.pointerDown(image, { pointerId: 2, clientX: 250, clientY: 700 });
		fireEvent.pointerUp(image, { pointerId: 2, clientX: 250, clientY: 300 });
		await waitFor(() => expect(FakeWebSocket.instances[0]?.sent.length).toBeGreaterThanOrEqual(6));
		const last = FakeWebSocket.instances[0]?.sent.at(-1) as Uint8Array;
		expect(last[0]).toBe(0x03);
		expect(JSON.parse(new TextDecoder().decode(last.subarray(1)))).toMatchObject({ type: "end", x: 0.5, y: 0.3 });
	});

	it("treats a stale daemon null inventory as an empty list", async () => {
		vi.mocked(aoBridge.device.list).mockResolvedValue({
			sessionId: "s1",
			devices: null,
		} as unknown as LocalDeviceInventory);

		render(<DevicePanel sessionId="s1" />);

		expect(await screen.findByText("No virtual devices were found. Create one in Xcode or Android Studio, then refresh.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Open device" })).toBeDisabled();
	});

	it("keeps a completed platform recoverable when live discovery fails", async () => {
		vi.mocked(aoBridge.device.status).mockResolvedValue({
			sessionId: "s1",
			capabilities: [{ platform: "android", available: true }],
		});
		vi.mocked(aoBridge.device.list).mockResolvedValue({
			sessionId: "s1",
			devices: [],
			errors: [{ platform: "android", available: false, code: "DEVICE_TOOLCHAIN_REQUIRED", message: "Android discovery needs to be refreshed." }],
		});
		vi.mocked(aoBridge.device.setupStatus).mockResolvedValue({
			sessionId: "s1",
			setups: [{ platform: "android", state: "succeeded", message: "Setup complete", progress: 100, licenseAccepted: true, cancelable: false, retryable: false }],
		});

		render(<DevicePanel sessionId="s1" />);

		expect(await screen.findByText("Android discovery needs to be refreshed.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Retry setup" })).toBeEnabled();
	});
});

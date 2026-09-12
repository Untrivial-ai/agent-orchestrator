import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { aoBridge } from "../lib/bridge";
import { DevicePanel } from "./DevicePanel";

describe("DevicePanel", () => {
	beforeEach(() => {
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
	});

	afterEach(() => vi.restoreAllMocks());

	it("shows independent setup state and opens a selected device", async () => {
		const command = vi.spyOn(aoBridge.device, "command").mockImplementation(async (input) => {
			if (input.action === "screenshot") {
				return { sessionId: "s1", action: input.action, attachment, result: { pngBase64: "cG5n" } };
			}
			return { sessionId: "s1", action: input.action, attachment };
		});
		const attachment = { sessionId: "s1", deviceId: "ios-1", platform: "ios" as const, name: "iPhone 17" };
		const view = render(<DevicePanel sessionId="s1" />);

		expect(await screen.findByText("iPhone 17")).toBeInTheDocument();
		expect(screen.getByText("Install Android Studio")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Open device" }));

		await waitFor(() => expect(command).toHaveBeenCalledWith({ sessionId: "s1", action: "open", deviceId: "ios-1", platform: "ios" }));
		expect(await screen.findByAltText("Live screen for iPhone 17")).toHaveAttribute("src", "data:image/png;base64,cG5n");
		fireEvent.click(screen.getByRole("button", { name: "Home" }));
		await waitFor(() => expect(command).toHaveBeenCalledWith({ sessionId: "s1", action: "home" }));
		view.unmount();
	});
});

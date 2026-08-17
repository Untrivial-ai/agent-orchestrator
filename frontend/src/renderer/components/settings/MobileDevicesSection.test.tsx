import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "../../lib/api-client";
import { appI18n } from "../../i18n";
import { MobileDevicesSection, mobileDevicesQueryKey } from "./MobileDevicesSection";

function renderSection() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const result = render(
		<QueryClientProvider client={client}>
			<MobileDevicesSection />
		</QueryClientProvider>,
	);
	return { ...result, client };
}

const twoDevices = {
	data: {
		devices: [
			{
				installId: "i1", token: "ExponentPushToken[a]", deviceName: "iPhone", platform: "ios",
				muted: false, live: true, notificationsEnabled: true,
				createdAt: new Date().toISOString(), lastSeenAt: new Date().toISOString(),
			},
			{
				installId: "i2", token: "ExponentPushToken[b]", deviceName: "M31s", platform: "android",
				muted: true, live: false, notificationsEnabled: true, createdAt: new Date().toISOString(),
				lastSeenAt: new Date(Date.now() - 7200_000).toISOString(),
			},
		],
	},
};

describe("MobileDevicesSection", () => {
	afterEach(async () => {
		vi.restoreAllMocks();
		await appI18n.changeLanguage("en");
	});

	it("shows a live device and a last-seen fallback", async () => {
		vi.spyOn(apiClient, "GET").mockResolvedValue(twoDevices as never);
		renderSection();

		expect(await screen.findByText("iPhone")).toBeInTheDocument();
		expect(screen.getByText("Live")).toBeInTheDocument();
		expect(screen.getByText("M31s")).toBeInTheDocument();
		expect(screen.getByText(/2 hours ago/)).toBeInTheDocument();
	});

	it("mutes a device", async () => {
		vi.spyOn(apiClient, "GET").mockResolvedValue(twoDevices as never);
		const patch = vi.spyOn(apiClient, "PATCH").mockResolvedValue({ data: { muted: true } } as never);
		renderSection();

		const toggle = await screen.findByRole("switch", { name: /notifications for iPhone/i });
		fireEvent.click(toggle);

		await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
		expect(patch.mock.calls[0][1]).toMatchObject({
			params: { path: { installId: "i1" } },
			body: { muted: true },
		});
	});

	it("removes a device only after confirmation", async () => {
		vi.spyOn(apiClient, "GET").mockResolvedValue(twoDevices as never);
		const del = vi.spyOn(apiClient, "DELETE").mockResolvedValue({ data: undefined } as never);
		renderSection();

		fireEvent.click(await screen.findByRole("button", { name: /remove iPhone/i }));
		expect(del).not.toHaveBeenCalled();

		fireEvent.click(screen.getByRole("button", { name: /confirm remove/i }));
		await waitFor(() => expect(del).toHaveBeenCalledTimes(1));
	});

	it("shows an empty state when nothing is paired", async () => {
		vi.spyOn(apiClient, "GET").mockResolvedValue({ data: { devices: [] } } as never);
		renderSection();
		expect(await screen.findByText(/No devices paired yet/i)).toBeInTheDocument();
	});

	it("shows a distinct message when the device registry is unavailable, not the empty state", async () => {
		vi.spyOn(apiClient, "GET").mockResolvedValue({
			data: undefined,
			error: {
				error: "device registry unavailable",
				code: "DEVICE_REGISTRY_UNAVAILABLE",
				message: "device registry unavailable",
				requestId: "req-1",
			},
		} as never);
		renderSection();

		expect(await screen.findByText(/Device registry unavailable/i)).toBeInTheDocument();
		expect(screen.getByText(/AO could not read your saved devices/i)).toBeInTheDocument();
		expect(screen.queryByText(/No devices paired yet/i)).not.toBeInTheDocument();
	});

	it("keeps the retained list visible on a transient poll failure, showing a banner instead of blanking it", async () => {
		const get = vi.spyOn(apiClient, "GET").mockResolvedValueOnce(twoDevices as never);
		const { client } = renderSection();

		expect(await screen.findByText("iPhone")).toBeInTheDocument();

		get.mockResolvedValueOnce({
			data: undefined,
			error: {
				error: "temporary failure",
				code: "SOME_TRANSIENT_ERROR",
				message: "Temporary failure",
				requestId: "req-2",
			},
		} as never);

		await act(async () => {
			await client.refetchQueries({ queryKey: mobileDevicesQueryKey });
		});

		expect(await screen.findByText(/Temporary failure/i)).toBeInTheDocument();
		// The list stays put — a single failed poll must not blank a roster we
		// already successfully loaded.
		expect(screen.getByText("iPhone")).toBeInTheDocument();
		expect(screen.getByText("M31s")).toBeInTheDocument();
		expect(screen.queryByText(/No devices paired yet/i)).not.toBeInTheDocument();
	});

	it("surfaces a failed mute instead of silently reverting with no explanation", async () => {
		vi.spyOn(apiClient, "GET").mockResolvedValue(twoDevices as never);
		vi.spyOn(apiClient, "PATCH").mockResolvedValue({
			data: undefined,
			error: { error: "device not found", code: "DEVICE_NOT_FOUND", message: "Device not found", requestId: "req-3" },
		} as never);
		renderSection();

		const toggle = await screen.findByRole("switch", { name: /notifications for iPhone/i });
		fireEvent.click(toggle);

		expect(await screen.findByText(/Device not found/i)).toBeInTheDocument();
	});

	it("surfaces a failed remove instead of silently reverting with no explanation", async () => {
		vi.spyOn(apiClient, "GET").mockResolvedValue(twoDevices as never);
		vi.spyOn(apiClient, "DELETE").mockResolvedValue({
			data: undefined,
			error: { error: "device not found", code: "DEVICE_NOT_FOUND", message: "Device not found", requestId: "req-4" },
		} as never);
		renderSection();

		fireEvent.click(await screen.findByRole("button", { name: /remove iPhone/i }));
		fireEvent.click(screen.getByRole("button", { name: /confirm remove/i }));

		expect(await screen.findByText(/Device not found/i)).toBeInTheDocument();
	});

	it("keeps row order stable across polls instead of following server re-sorts", async () => {
		// The server sorts live-first then LastSeenAt descending, which can flip
		// on every 3s poll when 2+ devices are live. Rendering must sort on a
		// stable field (installId) so rows never reorder under the user.
		const reordered = {
			data: {
				devices: [twoDevices.data.devices[1], twoDevices.data.devices[0]],
			},
		};
		vi.spyOn(apiClient, "GET").mockResolvedValue(reordered as never);
		renderSection();

		await screen.findByText("iPhone");
		const names = screen.getAllByText(/iPhone|M31s/).map((el) => el.textContent);
		expect(names).toEqual(["iPhone", "M31s"]);
	});

	it("shows a disabled switch and explanatory line for a device with no push token, but keeps live/last-seen and removal working", async () => {
		const noToken = {
			data: {
				devices: [
					{
						installId: "i3", deviceName: "Pixel Announce", platform: "android",
						muted: false, live: true, notificationsEnabled: false,
						createdAt: new Date().toISOString(), lastSeenAt: new Date().toISOString(),
					},
				],
			},
		};
		vi.spyOn(apiClient, "GET").mockResolvedValue(noToken as never);
		const del = vi.spyOn(apiClient, "DELETE").mockResolvedValue({ data: undefined } as never);
		renderSection();

		expect(await screen.findByText("Pixel Announce")).toBeInTheDocument();
		// Still shows live state even with no token.
		expect(screen.getByText("Live")).toBeInTheDocument();
		// Explanatory line for the disabled-notifications state.
		expect(screen.getByText(/Notifications not enabled on this device/i)).toBeInTheDocument();

		const toggle = screen.getByRole("switch", { name: /notifications for Pixel Announce/i });
		expect(toggle).toBeDisabled();

		// Still removable.
		fireEvent.click(screen.getByRole("button", { name: /remove Pixel Announce/i }));
		fireEvent.click(screen.getByRole("button", { name: /confirm remove/i }));
		await waitFor(() => expect(del).toHaveBeenCalledTimes(1));
	});

	it("formats last-seen relative to the app's language, not the OS locale", async () => {
		await appI18n.changeLanguage("ja");
		vi.spyOn(apiClient, "GET").mockResolvedValue(twoDevices as never);
		renderSection();

		await screen.findByText("M31s");
		// Japanese Intl.RelativeTimeFormat renders "2 時間前" for 2 hours ago —
		// distinct from the English "2 hours ago" the OS-locale default would
		// have produced on an en-US machine regardless of the app's own language.
		expect(screen.getByText(/時間前/)).toBeInTheDocument();
		expect(screen.queryByText(/2 hours ago/i)).not.toBeInTheDocument();
	});
});

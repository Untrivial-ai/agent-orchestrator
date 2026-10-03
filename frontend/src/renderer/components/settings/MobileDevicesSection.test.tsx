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

	it("shows devices as a compact, single-line management list", async () => {
		vi.spyOn(apiClient, "GET").mockResolvedValue(twoDevices as never);
		renderSection();

		expect(await screen.findByText("iPhone")).toBeInTheDocument();
		expect(screen.getByRole("heading", { name: "Connected devices" })).toBeInTheDocument();
		const deviceList = screen.getByRole("list");
		expect(deviceList).toHaveClass("divide-y");
		expect(deviceList).not.toHaveClass("rounded-md", "border");
		expect(screen.getAllByRole("listitem")[0]).not.toHaveClass("rounded-lg", "border", "px-3", "bg-[var(--color-bg-settings-input)]");
		expect(screen.getByText("M31s")).toBeInTheDocument();
		expect(screen.queryByText("Live")).not.toBeInTheDocument();
		expect(screen.getAllByTestId("bell")).toHaveLength(2);
		expect(screen.queryByText(/2 hours ago/)).not.toBeInTheDocument();
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

		const removeButton = await screen.findByRole("button", { name: /remove iPhone/i });
		expect(removeButton.querySelector(".lucide-trash-2")).toBeInTheDocument();
		fireEvent.click(removeButton);
		expect(del).not.toHaveBeenCalled();

		fireEvent.click(screen.getByRole("button", { name: /confirm remove/i }));
		await waitFor(() => expect(del).toHaveBeenCalledTimes(1));
	});

	it("renders nothing when no devices are paired", async () => {
		vi.spyOn(apiClient, "GET").mockResolvedValue({ data: { devices: [] } } as never);
		renderSection();
		await waitFor(() => expect(apiClient.GET).toHaveBeenCalled());
		expect(screen.queryByRole("heading", { name: "Connected devices" })).not.toBeInTheDocument();
		expect(screen.queryByText(/No devices paired yet/i)).not.toBeInTheDocument();
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

	it("keeps a device without a push token mutable and explains why it gets nothing", async () => {
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
		const patch = vi.spyOn(apiClient, "PATCH").mockResolvedValue({ data: { muted: true } } as never);
		const del = vi.spyOn(apiClient, "DELETE").mockResolvedValue({ data: undefined } as never);
		renderSection();

		expect(await screen.findByText("Pixel Announce")).toBeInTheDocument();
		expect(screen.queryByText("Live")).not.toBeInTheDocument();
		// The hint explains the daemon state (no push token) without locking the switch.
		expect(await screen.findByText(/Turn on Agent notifications in the AO phone app/i)).toBeInTheDocument();

		const toggle = screen.getByRole("switch", { name: /notifications for Pixel Announce/i });
		expect(toggle).toBeEnabled();
		// Unmuted device with no token still renders as on: the switch reflects
		// the mute preference, not token presence.
		expect(toggle).toHaveAttribute("data-state", "checked");

		// Muting still saves — the preference sticks for when a token registers.
		fireEvent.click(toggle);
		await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
		expect(patch.mock.calls[0][1]).toMatchObject({
			params: { path: { installId: "i3" } },
			body: { muted: true },
		});

		// Still removable.
		fireEvent.click(screen.getByRole("button", { name: /remove Pixel Announce/i }));
		fireEvent.click(screen.getByRole("button", { name: /confirm remove/i }));
		await waitFor(() => expect(del).toHaveBeenCalledTimes(1));
	});

	it("flips the switch immediately on mute and rolls back on failure", async () => {
		vi.spyOn(apiClient, "GET").mockResolvedValue(twoDevices as never);
		let rejectPatch!: (err: unknown) => void;
		vi.spyOn(apiClient, "PATCH").mockImplementation(
			() => new Promise((_resolve, reject) => void (rejectPatch = reject)),
		);
		renderSection();

		const toggle = await screen.findByRole("switch", { name: /notifications for iPhone/i });
		// iPhone starts unmuted → on.
		expect(toggle).toHaveAttribute("data-state", "checked");
		fireEvent.click(toggle);

		// Optimistic flip while the request is still in flight.
		await waitFor(() => expect(toggle).toHaveAttribute("data-state", "unchecked"));

		// Server failure rolls back to the previous state with an error.
		rejectPatch(new Error("Device not found"));
		expect(await screen.findByText(/Device not found/i)).toBeInTheDocument();
		await waitFor(() => expect(toggle).toHaveAttribute("data-state", "checked"));
	});

	it("tracks each row's in-flight mute separately so concurrent toggles don't unlock each other", async () => {
		vi.spyOn(apiClient, "GET").mockResolvedValue(twoDevices as never);
		const resolvers: Array<(value: unknown) => void> = [];
		vi.spyOn(apiClient, "PATCH").mockImplementation(
			() => new Promise((resolve) => void resolvers.push(resolve)),
		);
		renderSection();

		const iPhone = await screen.findByRole("switch", { name: /notifications for iPhone/i });
		const m31s = await screen.findByRole("switch", { name: /notifications for M31s/i });

		fireEvent.click(iPhone);
		await waitFor(() => expect(iPhone).toBeDisabled());
		// The other row is unaffected by the first row's in-flight request.
		expect(m31s).toBeEnabled();

		fireEvent.click(m31s);
		await waitFor(() => expect(m31s).toBeDisabled());
		expect(iPhone).toBeDisabled();

		// Both settle: each row unlocks and shows its saved state.
		resolvers.forEach((resolve) => resolve({ data: { muted: true } }));
		await waitFor(() => expect(iPhone).toBeEnabled());
		await waitFor(() => expect(m31s).toBeEnabled());
	});
});

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { TooltipProvider } from "./ui/tooltip";

// vi.mock is hoisted above module-level consts, so the shared doubles have to
// be created inside vi.hoisted to exist by the time the factories run.
const { captureRendererEvent, mobileStatus, post } = vi.hoisted(() => ({
	captureRendererEvent: vi.fn(),
	mobileStatus: {
		enabled: false,
		host: "192.168.1.20",
		port: 3011,
		password: "hunter2secret",
		warning: "",
	},
	post: vi.fn(),
}));

vi.mock("../lib/telemetry", () => ({ captureRendererEvent }));
vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: async (path: string) =>
			path === "/api/v1/mobile/devices"
				? { data: { devices: [] }, error: undefined }
				: { data: mobileStatus, error: undefined },
		POST: post,
	},
	apiErrorMessage: () => "failed",
}));

import { ConnectMobileContent } from "./settings/ConnectMobileContent";

function renderMobileSettings(active = true) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={client}>
			<TooltipProvider>
				<ConnectMobileContent active={active} />
			</TooltipProvider>
		</QueryClientProvider>,
	);
}

function openEvents() {
	return captureRendererEvent.mock.calls.filter((c) => c[0] === "ao.renderer.mobile_connect_opened");
}

function toggleEvents() {
	return captureRendererEvent.mock.calls.filter((c) => c[0] === "ao.renderer.mobile_bridge_toggled");
}

async function confirmBridgeStart() {
	await userEvent.click(screen.getByRole("button", { name: "Generate" }));
	await userEvent.click(await screen.findByRole("button", { name: "I trust this network — generate" }));
}

describe("Connect Mobile telemetry", () => {
	beforeEach(() => {
		captureRendererEvent.mockClear();
		post.mockReset();
		post.mockResolvedValue({ data: {}, error: undefined });
		mobileStatus.enabled = false;
	});

	test("reports the open once, carrying the real bridge state", async () => {
		mobileStatus.enabled = true;
		renderMobileSettings();

		await waitFor(() => expect(openEvents()).toHaveLength(1));
		expect(openEvents()[0][1]).toEqual({ bridge_enabled: true });
	});

	test("does not re-report the open when the bridge is toggled on", async () => {
		// The status query is invalidated by every enable/disable/regenerate, so
		// bridge_enabled changes while the Mobile page stays open. Without the once-per-open
		// guard the effect re-fires on that change and a single visit reports an open
		// for each toggle, inflating the top of the funnel it is supposed to measure.
		renderMobileSettings();
		await waitFor(() => expect(openEvents()).toHaveLength(1));
		expect(openEvents()[0][1]).toEqual({ bridge_enabled: false });

		post.mockImplementationOnce(async () => {
			mobileStatus.enabled = true;
			return { data: {}, error: undefined };
		});
		await confirmBridgeStart();

		await waitFor(() => expect(toggleEvents()).toHaveLength(1));
		await waitFor(() => expect(screen.getByRole("button", { name: "Generate" })).toBeDisabled());
		expect(openEvents()).toHaveLength(1);
	});

	test("reports nothing while closed", async () => {
		renderMobileSettings(false);
		// The status query is disabled while closed, so there is no state to report
		// and nothing should be emitted.
		await new Promise((r) => setTimeout(r, 20));
		expect(captureRendererEvent).not.toHaveBeenCalled();
	});

	test("reports a successful enable, and never the pairing secrets", async () => {
		renderMobileSettings();
		await waitFor(() => expect(openEvents()).toHaveLength(1));

		await confirmBridgeStart();

		await waitFor(() => expect(toggleEvents()).toHaveLength(1));
		expect(toggleEvents()[0][1]).toEqual({ enabled: true, outcome: "succeeded" });

		// The host, port, and connection password are on screen and in the QR, so
		// assert directly that no reported property ever carries them.
		const reported = JSON.stringify(captureRendererEvent.mock.calls);
		expect(reported).not.toContain("hunter2secret");
		expect(reported).not.toContain("192.168.1.20");
		expect(reported).not.toContain("3011");
	});

	test("reports a failed enable as failed rather than staying silent", async () => {
		post.mockResolvedValue({ data: undefined, error: { message: "bind failed" } });
		renderMobileSettings();
		await waitFor(() => expect(openEvents()).toHaveLength(1));

		await confirmBridgeStart();

		await waitFor(() => expect(toggleEvents()).toHaveLength(1));
		expect(toggleEvents()[0][1]).toEqual({ enabled: true, outcome: "failed" });
	});
});

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, test, vi } from "vitest";

// vi.mock is hoisted above module-level consts, so the shared doubles have to
// be created inside vi.hoisted to exist by the time the factories run.
const { get, mobileStatus, devices } = vi.hoisted(() => ({
	get: vi.fn(),
	mobileStatus: {
		enabled: false,
		host: "192.168.1.20",
		port: 3011,
		password: "hunter2secret",
		warning: "",
	},
	devices: { devices: [] as unknown[] },
}));

vi.mock("../lib/telemetry", () => ({ captureRendererEvent: vi.fn() }));
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: get, POST: vi.fn(), PATCH: vi.fn(), DELETE: vi.fn() },
	apiErrorMessage: () => "failed",
	apiErrorCode: () => undefined,
}));

import { ConnectMobileModal } from "./ConnectMobileModal";

function renderModal() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={client}>
			<ConnectMobileModal open onOpenChange={vi.fn()} />
		</QueryClientProvider>,
	);
}

describe("ConnectMobileModal device roster mounting", () => {
	beforeEach(() => {
		get.mockReset();
		get.mockImplementation(async (path: string) => {
			if (path === "/api/v1/mobile/status") return { data: mobileStatus, error: undefined };
			if (path === "/api/v1/mobile/devices") return { data: devices, error: undefined };
			return { data: undefined, error: { message: "unexpected path" } };
		});
		mobileStatus.enabled = false;
	});

	// CRITICAL fix: the roster's Switch/remove controls issue real PATCH/DELETE
	// calls, so while the bridge is off the section must be absent from the DOM
	// entirely — not merely visually hidden behind the pairing block's CSS
	// collapse/opacity, which still leaves focusable, keyboard-reachable
	// controls sitting under aria-hidden="true" (a WCAG 4.1.2 violation and a
	// way to fire mutations against a disabled bridge).
	test("does not mount the device roster, or fetch its devices, while the bridge is disabled", async () => {
		mobileStatus.enabled = false;
		renderModal();

		await waitFor(() => expect(get).toHaveBeenCalledWith("/api/v1/mobile/status"));
		// Give any stray effect a tick to fire before asserting silence.
		await new Promise((r) => setTimeout(r, 20));

		expect(screen.queryByRole("heading", { name: "Devices" })).not.toBeInTheDocument();
		expect(get).not.toHaveBeenCalledWith("/api/v1/mobile/devices");
	});

	test("mounts the device roster once the bridge is enabled", async () => {
		mobileStatus.enabled = true;
		renderModal();

		expect(await screen.findByRole("heading", { name: "Devices" })).toBeInTheDocument();
		await waitFor(() => expect(get).toHaveBeenCalledWith("/api/v1/mobile/devices"));
	});
});

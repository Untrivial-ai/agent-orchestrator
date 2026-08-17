import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";

// vi.mock is hoisted above module-level consts, so the shared double has to be
// created inside vi.hoisted to exist by the time the factory runs.
const { mobileStatus } = vi.hoisted(() => ({
	mobileStatus: {
		enabled: true,
		host: "192.168.1.42",
		tailscaleHost: "100.72.46.7",
		port: 3011,
		password: "fake-password-for-testing",
		warning: "",
		securePairing: {
			enabled: false,
			available: false,
			active: false,
			host: "",
			port: 0,
			reason: "",
		},
	},
}));

vi.mock("../lib/telemetry", () => ({ captureRendererEvent: vi.fn() }));
vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: async () => ({ data: mobileStatus, error: undefined }),
		POST: vi.fn(async () => ({ data: {}, error: undefined })),
	},
	apiErrorMessage: () => "failed",
}));

import { ConnectMobileModal, pairingPayload } from "./ConnectMobileModal";

function renderModal() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={client}>
			<ConnectMobileModal open={true} onOpenChange={vi.fn()} />
		</QueryClientProvider>,
	);
}

// The QR is an <svg>, so read the payload off the value the component encodes
// rather than the DOM: qrcode.react renders paths, not text.
function qrPayload(): string | null {
	const svg = document.querySelector("svg[data-qr-value]");
	return svg?.getAttribute("data-qr-value") ?? null;
}

beforeEach(() => {
	mobileStatus.enabled = true;
	mobileStatus.host = "192.168.1.42";
	mobileStatus.tailscaleHost = "100.72.46.7";
	mobileStatus.warning = "";
	mobileStatus.securePairing = {
		enabled: false,
		available: false,
		active: false,
		host: "",
		port: 0,
		reason: "",
	};
});

test("QR payload carries host, port, and password for one-scan connect", () => {
	const s = pairingPayload("192.168.1.42", 3011, "fake-password-for-testing");
	expect(JSON.parse(s)).toEqual({ v: 1, host: "192.168.1.42", port: 3011, password: "fake-password-for-testing" });
});

test("encodes the LAN address by default", async () => {
	renderModal();
	await waitFor(() => expect(qrPayload()).not.toBeNull());
	expect(JSON.parse(qrPayload()!).host).toBe("192.168.1.42");
});

test("re-encodes the QR with the Tailscale address when that mode is selected", async () => {
	renderModal();
	await waitFor(() => expect(qrPayload()).not.toBeNull());

	await userEvent.click(screen.getByRole("radio", { name: "Tailscale" }));

	await waitFor(() => expect(JSON.parse(qrPayload()!).host).toBe("100.72.46.7"));
	// The password and port are unchanged — only the address differs.
	expect(JSON.parse(qrPayload()!)).toEqual({
		v: 1,
		host: "100.72.46.7",
		port: 3011,
		password: "fake-password-for-testing",
	});
});

test("shows a hint instead of a QR when Tailscale is not running", async () => {
	mobileStatus.tailscaleHost = "";
	renderModal();
	await waitFor(() => expect(qrPayload()).not.toBeNull());

	await userEvent.click(screen.getByRole("radio", { name: "Tailscale" }));

	await waitFor(() => expect(screen.getByText(/Tailscale isn't running/i)).toBeInTheDocument());
	expect(qrPayload()).toBeNull();
});

// Regression: an empty host used to encode {"v":1,"host":"",...}, which the
// phone rejects as "not an AO pairing code" — an incoherent error for a QR AO
// generated itself.
test("shows a hint instead of an unscannable QR when there is no LAN address", async () => {
	mobileStatus.host = "";
	renderModal();
	await waitFor(() => expect(screen.getByText(/No network address found/i)).toBeInTheDocument());
	expect(qrPayload()).toBeNull();
});

test("the address line follows the selected mode", async () => {
	renderModal();
	const address = await screen.findByTestId("mobile-pairing-address");
	expect(within(address).getByText("192.168.1.42:3011")).toBeInTheDocument();

	await userEvent.click(screen.getByRole("radio", { name: "Tailscale" }));
	await waitFor(() => expect(within(address).getByText("100.72.46.7:3011")).toBeInTheDocument());
});

test("omits the secure key entirely for plaintext pairing", () => {
	expect(JSON.parse(pairingPayload("192.168.1.42", 3011, "pw"))).toEqual({
		v: 1, host: "192.168.1.42", port: 3011, password: "pw",
	});
});

test("encodes secure:true when secure pairing is active", () => {
	expect(JSON.parse(pairingPayload("host.tail1.ts.net", 443, "pw", true))).toEqual({
		v: 1, host: "host.tail1.ts.net", port: 443, password: "pw", secure: true,
	});
});

test("Tailscale tab encodes the MagicDNS host over 443 when secure pairing is active", async () => {
	mobileStatus.securePairing = {
		enabled: true, available: true, active: true,
		host: "prasads-macbook-pro.tail057d04.ts.net", port: 443, reason: "",
	};
	renderModal();
	await waitFor(() => expect(qrPayload()).not.toBeNull());
	await userEvent.click(screen.getByRole("radio", { name: "Tailscale" }));

	await waitFor(() => {
		const p = JSON.parse(qrPayload()!);
		expect(p.host).toBe("prasads-macbook-pro.tail057d04.ts.net");
		expect(p.port).toBe(443);
		expect(p.secure).toBe(true);
	});
});

test("shows setup steps and no QR when certs are not enabled", async () => {
	mobileStatus.securePairing = {
		enabled: true, available: false, active: false,
		host: "h.tail1.ts.net", port: 0, reason: "no_certs",
	};
	renderModal();
	await waitFor(() => expect(qrPayload()).not.toBeNull());
	await userEvent.click(screen.getByRole("radio", { name: "Tailscale" }));

	await waitFor(() => expect(screen.getByText(/HTTPS certificates/i)).toBeInTheDocument());
	expect(qrPayload()).toBeNull();
});

// A failing secure-pairing POST must surface an error rather than silently
// snapping the switch back on the next status refetch with no explanation.
test("shows an error message when the secure-pairing toggle fails", async () => {
	const { apiClient } = await import("../lib/api-client");
	vi.mocked(apiClient.POST).mockImplementationOnce(async () => ({
		data: undefined,
		error: { message: "secure pairing failed" },
	}));
	renderModal();
	await waitFor(() => expect(qrPayload()).not.toBeNull());
	await userEvent.click(screen.getByRole("radio", { name: "Tailscale" }));

	const secureSwitch = await screen.findByRole("switch", { name: "Secure pairing (TLS)" });
	await userEvent.click(secureSwitch);

	await waitFor(() => expect(screen.getByText("failed")).toBeInTheDocument());
});

// The warning is false in secure mode — the connection really is encrypted.
test("hides the unencrypted-traffic warning when secure pairing is active", async () => {
	mobileStatus.warning = "Traffic on this connection is not encrypted.";
	mobileStatus.securePairing = {
		enabled: true, available: true, active: true,
		host: "h.tail1.ts.net", port: 443, reason: "",
	};
	renderModal();
	await waitFor(() => expect(qrPayload()).not.toBeNull());
	expect(screen.getByText(/not encrypted/i)).toBeInTheDocument();

	await userEvent.click(screen.getByRole("radio", { name: "Tailscale" }));
	await waitFor(() => expect(screen.queryByText(/not encrypted/i)).not.toBeInTheDocument());
});

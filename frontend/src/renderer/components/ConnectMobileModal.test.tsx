import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { TooltipProvider } from "./ui/tooltip";
import { apiClient } from "../lib/api-client";

// vi.mock is hoisted above module-level consts, so the shared double has to be
// created inside vi.hoisted to exist by the time the factory runs.
const { mobileStatus } = vi.hoisted(() => ({
	mobileStatus: {
		enabled: true,
		host: "192.168.1.42",
		tailscaleHost: "100.72.46.7",
		port: 3011,
		password: "fake-password-for-testing",
		// The daemon always advertises its endpoints now, and a v2 code is built
		// from that list — there is no v1 form left to fall back to when it is
		// missing, so a fixture without it is not a realistic daemon.
		hostId: "h_fixture",
		endpoints: [{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false }],
		tunnel: undefined as
			| undefined
			| {
					supported: boolean;
					running: boolean;
					ready: boolean;
					hostname: string;
					location: string;
					lastError: string;
			  },
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
		GET: async (path: string) =>
			path === "/api/v1/mobile/devices"
				? { data: { devices: [] }, error: undefined }
				: { data: mobileStatus, error: undefined },
		POST: vi.fn(async () => ({ data: {}, error: undefined })),
	},
	apiErrorMessage: () => "failed",
}));

import {
	ConnectMobileContent,
	mobileStatusRefetchInterval,
	pairingPayload,
	qrIsReady,
	qrValueFor,
} from "./settings/ConnectMobileContent";

function renderMobileSettings() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={client}>
			<TooltipProvider>
				<ConnectMobileContent active />
			</TooltipProvider>
		</QueryClientProvider>,
	);
}

// Read the payload off the wrapper used by the shared styled QR component.
function qrPayload(): string | null {
	const qr = document.querySelector("[data-qr-value]");
	return qr?.getAttribute("data-qr-value") ?? null;
}

// Mirrors what the phone does with a scanned code.
function decodeQr(value: string): Record<string, unknown> {
	const code = value.slice(value.indexOf("#") + 1);
	const b64 = code.replace(/-/g, "+").replace(/_/g, "/");
	return JSON.parse(atob(b64 + "=".repeat((4 - (b64.length % 4)) % 4)));
}

async function selectConnectionMethod(mode: "LAN" | "Tailscale") {
	await userEvent.click(await screen.findByRole("button", { name: "Connection method" }));
	await userEvent.click(await screen.findByRole("menuitem", { name: mode }));
}

beforeEach(() => {
	mobileStatus.enabled = true;
	mobileStatus.host = "192.168.1.42";
	mobileStatus.hostId = "h_fixture";
	mobileStatus.tunnel = undefined;
	mobileStatus.endpoints = [{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false }];
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
	renderMobileSettings();
	await waitFor(() => expect(qrPayload()).not.toBeNull());
	expect(decodeQr(qrPayload()!).endpoints).toContainEqual(
		expect.objectContaining({ kind: "lan", host: "192.168.1.42" }),
	);
});

test("can turn off the generated mobile connection", async () => {
	renderMobileSettings();
	const button = await screen.findByRole("button", { name: "Turn off mobile connection" });

	await userEvent.click(button);
	expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/mobile/disable");
});

test.each([
	{ outcome: "success", serverEnabled: false, failed: false },
	{ outcome: "error after shutdown", serverEnabled: false, failed: true },
	{ outcome: "error while still running", serverEnabled: true, failed: true },
])("refreshes mobile status after disable $outcome", async ({ serverEnabled, failed }) => {
	let statusReads = 0;
	const get = vi.spyOn(apiClient, "GET").mockImplementation(async (path) => {
		if (path === "/api/v1/mobile/devices") {
			return { data: { devices: [] }, error: undefined };
		}
		statusReads++;
		// Each response is a snapshot. Updating the fake server must not mutate
		// the cached response and conceal a missing status refetch.
		return { data: { ...mobileStatus }, error: undefined };
	});
	vi.mocked(apiClient.POST).mockImplementationOnce(async () => {
		mobileStatus.enabled = serverEnabled;
		return failed
			? { data: undefined, error: { message: "shutdown failed" } }
			: { data: {}, error: undefined };
	});

	try {
		renderMobileSettings();
		await userEvent.click(await screen.findByRole("button", { name: "Turn off mobile connection" }));
		await waitFor(() => expect(statusReads).toBeGreaterThan(1));
		await waitFor(() => {
			const disableButton = screen.queryByRole("button", { name: "Turn off mobile connection" });
			if (serverEnabled) {
				expect(disableButton).toBeVisible();
				expect(qrPayload()).not.toBeNull();
			} else {
				expect(disableButton).not.toBeInTheDocument();
				expect(screen.getByRole("button", { name: "Generate" })).toBeVisible();
			}
		});
		if (failed) expect(screen.getByText("failed")).toBeVisible();
	} finally {
		get.mockRestore();
	}
});

// SKIPPED: drives the connection picker, which is commented out in
// ConnectMobileContent. Un-skip with it — the behaviour is unchanged, only
// the way in is gone. TLS coverage does NOT live here any more: it keys off
// the tailnet address, so those tests run without the picker.
test.skip("keeps the connection-method dropdown at its fixed width", async () => {
	renderMobileSettings();
	await waitFor(() => expect(qrPayload()).not.toBeNull());

	const control = screen.getByRole("button", { name: "Connection method" });
	expect(control).toHaveClass("w-44", "justify-between");

	await userEvent.click(control);
	expect(screen.getByRole("menu")).toHaveClass("!w-44", "!min-w-0");
});

// The QR is withheld until the tunnel is ready, so a scannable code already
// carries a tunnel endpoint and the phone need not be on this network. The
// no-connector case is stated separately by remoteAccessUnavailable.
test("does not tell LAN users to join the same Wi-Fi", async () => {
	renderMobileSettings();
	await waitFor(() => expect(qrPayload()).not.toBeNull());

	expect(screen.queryByText(/same Wi-Fi/i)).not.toBeInTheDocument();
	expect(screen.getByText("Generate and scan the QR from the AO app")).toBeInTheDocument();
});

// Both stores are listed in step one now. The platform dropdown that used to
// gate them is gone, so neither link may be behind an interaction.
test("offers both store links without a platform choice", async () => {
	renderMobileSettings();

	expect(await screen.findByRole("button", { name: "Open Agent Orchestrator on the App Store" })).toBeInTheDocument();
	expect(screen.getByRole("button", { name: "Open Agent Orchestrator on Google Play" })).toBeInTheDocument();
	expect(screen.queryByRole("button", { name: "Get the app" })).not.toBeInTheDocument();
});

test("shows a square Google Play QR tooltip for Android", async () => {
	renderMobileSettings();
	await userEvent.hover(await screen.findByRole("button", { name: "Open Agent Orchestrator on Google Play" }));

	expect(await screen.findByTestId("android-play-qr")).toHaveClass("p-2");
});

test("shows a QR-only App Store tooltip", async () => {
	renderMobileSettings();
	await userEvent.hover(await screen.findByRole("button", { name: "Open Agent Orchestrator on the App Store" }));

	const tooltip = await screen.findByTestId("ios-store-qr");
	expect(tooltip).not.toHaveTextContent("App Store");
	expect(tooltip.querySelector("svg")).toBeInTheDocument();
});

// v1 encoded one chosen address, so switching mode had to re-encode the code.
// A v2 code carries every endpoint the daemon advertises and the phone races
// them, so the mode selector now only changes which address is *displayed* —
// the code itself is the same either way. Re-encoding per mode would defeat the
// race by handing the phone a single path again.
// SKIPPED: drives the connection picker, which is commented out in
// ConnectMobileContent. Un-skip with it — the behaviour is unchanged, only
// the way in is gone. TLS coverage does NOT live here any more: it keys off
// the tailnet address, so those tests run without the picker.
test.skip("keeps one code across modes, carrying every advertised endpoint", async () => {
	mobileStatus.endpoints = [
		{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false },
		{ kind: "tailscale", host: "100.72.46.7", port: 3011, secure: false },
	];
	renderMobileSettings();
	await waitFor(() => expect(qrPayload()).not.toBeNull());
	const before = qrPayload()!;

	await selectConnectionMethod("Tailscale");
	await waitFor(() => expect(qrPayload()).not.toBeNull());

	expect(decodeQr(qrPayload()!).endpoints).toEqual([
		{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false },
		{ kind: "tailscale", host: "100.72.46.7", port: 3011, secure: false },
	]);
	expect(decodeQr(before).endpoints).toEqual(decodeQr(qrPayload()!).endpoints);
});

// SKIPPED: drives the connection picker, which is commented out in
// ConnectMobileContent. Un-skip with it — the behaviour is unchanged, only
// the way in is gone. TLS coverage does NOT live here any more: it keys off
// the tailnet address, so those tests run without the picker.
test.skip("shows a hint instead of a QR when Tailscale is not running", async () => {
	mobileStatus.tailscaleHost = "";
	renderMobileSettings();
	await waitFor(() => expect(qrPayload()).not.toBeNull());

	await selectConnectionMethod("Tailscale");

	await waitFor(() => expect(screen.getByText(/Tailscale isn't running/i)).toBeInTheDocument());
	expect(qrPayload()).toBeNull();
});

// Regression: an empty host used to encode {"v":1,"host":"",...}, which the
// phone rejects as "not an AO pairing code" — an incoherent error for a QR AO
// generated itself.
test("shows a hint instead of an unscannable QR when there is no LAN address", async () => {
	mobileStatus.host = "";
	renderMobileSettings();
	await waitFor(() => expect(screen.getByText(/No network address found/i)).toBeInTheDocument());
	expect(qrPayload()).toBeNull();
});

// SKIPPED: drives the connection picker, which is commented out in
// ConnectMobileContent. Un-skip with it — the behaviour is unchanged, only
// the way in is gone. TLS coverage does NOT live here any more: it keys off
// the tailnet address, so those tests run without the picker.
test.skip("the address line follows the selected mode", async () => {
	renderMobileSettings();
	const address = await screen.findByTestId("mobile-pairing-address");
	expect(within(address).getByText("192.168.1.42:3011")).toBeInTheDocument();

	await selectConnectionMethod("Tailscale");
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

// KNOWN GAP, recorded rather than asserted away: a v2 code is built purely from
// the daemon's endpoint list, and Endpoints() only ever marks the tunnel
// secure — tailscale entries are the plain tailnet IP. So the MagicDNS host
// secure pairing sets up is not carried in the code at all.
//
// This predates the v1 removal; it was hidden because the old fixture
// advertised no endpoints, which forced the v1 branch that did encode it. The
// tunnel's publicly trusted certificate is what satisfies iOS ATS now, so this
// may be intentional obsolescence — but the setting is still offered, and a
// user who enables it gets a code that does not use it.
// SKIPPED: drives the connection picker, which is commented out in
// ConnectMobileContent. Un-skip with it — the behaviour is unchanged, only
// the way in is gone. TLS coverage does NOT live here any more: it keys off
// the tailnet address, so those tests run without the picker.
test.skip("does not carry the secure-pairing MagicDNS host in the code", async () => {
	mobileStatus.securePairing = {
		enabled: true, available: true, active: true,
		host: "prasads-macbook-pro.tail057d04.ts.net", port: 443, reason: "",
	};
	renderMobileSettings();
	await waitFor(() => expect(qrPayload()).not.toBeNull());
	await selectConnectionMethod("Tailscale");

	await waitFor(() => expect(qrPayload()).not.toBeNull());
	const hosts = (decodeQr(qrPayload()!).endpoints as { host: string }[]).map((e) => e.host);
	expect(hosts).not.toContain("prasads-macbook-pro.tail057d04.ts.net");
});

// SKIPPED: drives the connection picker, which is commented out in
// ConnectMobileContent. Un-skip with it — the behaviour is unchanged, only
// the way in is gone. TLS coverage does NOT live here any more: it keys off
// the tailnet address, so those tests run without the picker.
test.skip("shows setup steps and no QR when certs are not enabled", async () => {
	mobileStatus.securePairing = {
		enabled: true, available: false, active: false,
		host: "h.tail1.ts.net", port: 0, reason: "no_certs",
	};
	renderMobileSettings();
	await waitFor(() => expect(qrPayload()).not.toBeNull());
	await selectConnectionMethod("Tailscale");

	await waitFor(() => expect(screen.getByText(/HTTPS certificates/i)).toBeInTheDocument());
	expect(qrPayload()).toBeNull();
});

// A failing secure-pairing POST must surface an error rather than silently
// snapping the switch back on the next status refetch with no explanation.
test("surfaces an error when enabling secure pairing fails", async () => {
	mobileStatus.securePairing = {
		enabled: false, available: true, active: false,
		host: "", port: 0, reason: "",
	};
	const { apiClient } = await import("../lib/api-client");
	vi.mocked(apiClient.POST).mockImplementationOnce(async () => ({
		data: undefined,
		error: { message: "secure pairing failed" },
	}));
	renderMobileSettings();

	await waitFor(() => expect(screen.getByText("failed")).toBeInTheDocument());
});

// TLS is no longer a switch: iOS refuses cleartext to a 100.x address, so a
// Tailscale pairing with it off works on Android and fails on iPhone with
// nothing to explain why. Selecting Tailscale turns it on.
test("turns secure pairing on wherever a tailnet address exists", async () => {
	mobileStatus.securePairing = {
		enabled: false, available: true, active: false,
		host: "", port: 0, reason: "",
	};
	const { apiClient } = await import("../lib/api-client");
	renderMobileSettings();

	await waitFor(() =>
		expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/mobile/secure-pairing", { body: { enabled: true } }),
	);
	expect(screen.queryByRole("switch", { name: "Secure pairing (TLS)" })).not.toBeInTheDocument();
	// Nothing to report while it works: the panel guarantees TLS, so only a
	// failure earns space.
	expect(screen.queryByTestId("secure-pairing-reason")).not.toBeInTheDocument();
});

// A tailnet with no certificates rejects every attempt. Retrying on each status
// poll would hammer the daemon; the reason text is what tells the user.
test("does not retry enabling secure pairing when it is unavailable", async () => {
	mobileStatus.securePairing = {
		enabled: false, available: false, active: false,
		host: "", port: 0, reason: "no_certs",
	};
	const { apiClient } = await import("../lib/api-client");
	// POST is a suite-wide mock; a previous test's secure-pairing call would
	// otherwise satisfy the negative assertion below.
	vi.mocked(apiClient.POST).mockClear();
	renderMobileSettings();

	await waitFor(() => expect(screen.getByTestId("secure-pairing-reason")).toBeInTheDocument());
	expect(apiClient.POST).not.toHaveBeenCalledWith("/api/v1/mobile/secure-pairing", { body: { enabled: true } });
});


// The QR value is the wire contract with the phone.
test("emits a v2 deep link carrying every endpoint once the daemon advertises them", () => {
	const value = qrValueFor({
		hostId: "h_b3e07f31",
		host: "192.168.1.42",
		platform: "darwin",
		password: "pw",
		endpoints: [
			{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false },
			{ kind: "tunnel", host: "abc.trycloudflare.com", port: 443, secure: true },
		],
	});

	expect(value.startsWith("aomobile://pair#")).toBe(true);
	const code = value.slice(value.indexOf("#") + 1);
	const b64 = code.replace(/-/g, "+").replace(/_/g, "/");
	const decoded = JSON.parse(atob(b64 + "=".repeat((4 - (b64.length % 4)) % 4)));
	expect(decoded.v).toBe(2);
	expect(decoded.hostId).toBe("h_b3e07f31");
	expect(decoded.endpoints).toHaveLength(2);
});

// The token must never sit in the part of a URL that reaches a server.
test("keeps the connection token out of everything before the fragment", () => {
	const value = qrValueFor({
		hostId: "h_x", host: "192.168.1.42", platform: "darwin", password: "super-secret",
		endpoints: [{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false }],
	});

	expect(value.split("#")[0]).not.toContain("super-secret");
});

// v1 emission was removed alongside the phone's v1 support: those codes predate
// the identity probe the race uses, so a daemon old enough to need one could
// never complete a race. An empty endpoint list is the "preparing" state, not a
// reason to emit a code the phone will refuse — see qrIsReady.
test("emits only v2, never a raw JSON v1 payload", () => {
	const value = qrValueFor({
		hostId: "h_x", host: "192.168.1.42", platform: "darwin", password: "pw",
		endpoints: [{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false }],
	});

	expect(value.startsWith("aomobile://pair#")).toBe(true);
	// v1 travelled as bare JSON; a v2 link must never parse as one.
	expect(() => JSON.parse(value)).toThrow();
	expect(decodeQr(value).v).toBe(2);
});

// The trap that produced a pairing which worked on Wi-Fi and failed on
// cellular: the QR renders as soon as the LAN listener is up, but the tunnel
// takes ~30s more to become advertisable. A code scanned in that window
// carries no tunnel endpoint at all, so the phone has nothing to fall back to
// once it leaves the network.
test("holds the QR back while remote access is still starting", () => {
	expect(
		qrIsReady({
			enabled: true,
			endpoints: [{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false }],
			tunnel: { running: true, ready: false, hostname: "", location: "", lastError: "" },
		}),
	).toBe(false);
});

test("shows the QR once the tunnel is advertisable", () => {
	expect(
		qrIsReady({
			enabled: true,
			endpoints: [
				{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false },
				{ kind: "tunnel", host: "abc.trycloudflare.com", port: 443, secure: true },
			],
			tunnel: { running: true, ready: true, hostname: "abc.trycloudflare.com", location: "", lastError: "" },
		}),
	).toBe(true);
});

// Remote access unavailable entirely (no cloudflared) must not block pairing —
// LAN-only is a legitimate setup and waiting forever would be worse.
test("shows the QR when there is no tunnel to wait for", () => {
	expect(
		qrIsReady({
			enabled: true,
			endpoints: [{ kind: "lan", host: "192.168.1.42", port: 3011, secure: false }],
			tunnel: { running: false, ready: false, hostname: "", location: "", lastError: "" },
		}),
	).toBe(true);
});

test("holds the QR back when nothing is reachable yet", () => {
	expect(qrIsReady({ enabled: true, endpoints: [], tunnel: undefined })).toBe(false);
});

// A v2 code is built from the endpoint list and there is no v1 form left to
// fall back to, so an absent list is as unready as an empty one: the daemon has
// not yet said where it can be reached. Emitting a code here would produce one
// the phone refuses.
test("withholds the QR from a daemon that reports no endpoints", () => {
	expect(qrIsReady({ enabled: true, endpoints: undefined, tunnel: undefined })).toBe(false);
	expect(qrIsReady({ enabled: true, endpoints: [], tunnel: undefined })).toBe(false);
});

// The QR gate introduced a state the renderer has to wait out. The status query
// is fetched once on open and refetched only after a mutation, so without
// polling the modal sits on "Preparing remote access…" forever even though the
// daemon went advertisable seconds later.
test("polls while the connector is starting", () => {
	expect(
		mobileStatusRefetchInterval({ tunnel: { running: true, ready: false } }),
	).toBeGreaterThan(0);
});

// Polling only while there is something to wait for: once the tunnel is up
// there is no transient state left, and the modal should not keep hitting the
// daemon for the rest of the session.
test("stops polling once the tunnel is advertisable", () => {
	expect(mobileStatusRefetchInterval({ tunnel: { running: true, ready: true } })).toBe(false);
});

test("does not poll when there is no tunnel to wait for", () => {
	expect(mobileStatusRefetchInterval({ tunnel: { running: false, ready: false } })).toBe(false);
	expect(mobileStatusRefetchInterval({ tunnel: undefined })).toBe(false);
	expect(mobileStatusRefetchInterval(undefined)).toBe(false);
});

// Remote access is optional: nothing installs cloudflared, so on a machine
// without it there is no connector at all. A zero tunnel status made that
// indistinguishable from "not started yet", so the QR looked entirely normal
// and the user discovered the gap only by being away from home.
test("says so when this machine cannot be reached from elsewhere", async () => {
	mobileStatus.tunnel = {
		supported: false, running: false, ready: false, hostname: "", location: "", lastError: "",
	};
	renderMobileSettings();

	expect(await screen.findByTestId("mobile-remote-unavailable")).toHaveTextContent(
		/only|cloudflared/i,
	);
});

// A machine that has a connector must not carry the caveat, whether or not the
// tunnel happens to be up yet.
test("does not claim unavailability while the connector is merely starting", async () => {
	mobileStatus.tunnel = {
		supported: true, running: true, ready: false, hostname: "", location: "", lastError: "",
	};
	renderMobileSettings();

	await waitFor(() => expect(screen.queryByTestId("mobile-remote-unavailable")).toBeNull());
});

// The install offer belongs with the caveat: the user learns that remote access
// is unavailable and can act on it in the same place, rather than being told to
// go and find a terminal.
test("offers to install the connector when it is missing", async () => {
	mobileStatus.tunnel = {
		supported: false, running: false, ready: false, hostname: "", location: "", lastError: "",
	};
	renderMobileSettings();

	expect(await screen.findByTestId("mobile-install-cloudflared")).toBeInTheDocument();
});

test("does not offer an install when a connector already exists", async () => {
	mobileStatus.tunnel = {
		supported: true, running: true, ready: true, hostname: "x.trycloudflare.com", location: "", lastError: "",
	};
	renderMobileSettings();

	await waitFor(() => expect(screen.queryByTestId("mobile-install-cloudflared")).toBeNull());
});

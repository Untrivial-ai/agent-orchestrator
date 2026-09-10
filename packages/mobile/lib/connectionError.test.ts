import { describe, expect, it } from "vitest";
import {
	classifyConnectionFailure,
	describeConnectionFailure,
	isLocalNetworkHost,
	isTailscaleHost,
	shouldKeepPolling,
} from "./connectionError";

const target = (over: Partial<{ host: string; port: string; platform: string }> = {}) => ({
	host: "192.168.1.5",
	port: "3011",
	platform: "ios",
	...over,
});

describe("classifyConnectionFailure", () => {
	it("treats no answer as unreachable", () => {
		expect(classifyConnectionFailure(undefined)).toBe("unreachable");
	});

	it("maps 401 and 403 to auth", () => {
		expect(classifyConnectionFailure(401)).toBe("auth");
		expect(classifyConnectionFailure(403)).toBe("auth");
	});

	it("maps 429 to rate-limited", () => {
		expect(classifyConnectionFailure(429)).toBe("rate-limited");
	});

	it("maps any other status to a server error", () => {
		expect(classifyConnectionFailure(500)).toBe("server-error");
		expect(classifyConnectionFailure(404)).toBe("server-error");
	});
});

describe("isLocalNetworkHost", () => {
	it("accepts the RFC1918 ranges", () => {
		expect(isLocalNetworkHost("10.0.0.4")).toBe(true);
		expect(isLocalNetworkHost("192.168.1.5")).toBe(true);
		expect(isLocalNetworkHost("172.16.0.1")).toBe(true);
		expect(isLocalNetworkHost("172.31.255.254")).toBe(true);
	});

	it("rejects addresses just outside the 172.16/12 block", () => {
		expect(isLocalNetworkHost("172.15.0.1")).toBe(false);
		expect(isLocalNetworkHost("172.32.0.1")).toBe(false);
	});

	it("accepts loopback, link-local, and mDNS names", () => {
		expect(isLocalNetworkHost("127.0.0.1")).toBe(true);
		expect(isLocalNetworkHost("169.254.1.1")).toBe(true);
		expect(isLocalNetworkHost("localhost")).toBe(true);
		expect(isLocalNetworkHost("my-pc.local")).toBe(true);
	});

	// Tailscale rides a VPN interface, so the Local Network prompt is never the
	// cause there — offering that hint would send the user to a dead end.
	it("rejects the Tailscale CGNAT range and public hosts", () => {
		expect(isLocalNetworkHost("100.101.102.103")).toBe(false);
		expect(isLocalNetworkHost("my-pc.tail1234.ts.net")).toBe(false);
		expect(isLocalNetworkHost("203.0.113.7")).toBe(false);
	});

	it("ignores surrounding whitespace and case", () => {
		expect(isLocalNetworkHost("  My-PC.Local  ")).toBe(true);
	});

	it("rejects an empty host", () => {
		expect(isLocalNetworkHost("")).toBe(false);
		expect(isLocalNetworkHost("   ")).toBe(false);
	});
});

describe("isTailscaleHost", () => {
	it("accepts the 100.64.0.0/10 CGNAT range", () => {
		expect(isTailscaleHost("100.64.0.0")).toBe(true);
		expect(isTailscaleHost("100.101.102.103")).toBe(true);
		expect(isTailscaleHost("100.127.255.255")).toBe(true);
	});

	it("rejects addresses just outside the range", () => {
		expect(isTailscaleHost("100.63.255.255")).toBe(false);
		expect(isTailscaleHost("100.128.0.0")).toBe(false);
	});

	it("rejects LAN addresses and hostnames", () => {
		expect(isTailscaleHost("192.168.1.5")).toBe(false);
		expect(isTailscaleHost("my-pc.tail1234.ts.net")).toBe(false);
	});
});

describe("describeConnectionFailure", () => {
	it("names the scanned address when nothing answered", () => {
		const d = describeConnectionFailure("unreachable", target({ host: "192.168.1.5", port: "3011" }));
		expect(d.message).toContain("192.168.1.5:3011");
		expect(d.message).toContain("same Wi-Fi");
	});

	it("blames Wi-Fi for an unreachable LAN host", () => {
		const d = describeConnectionFailure("unreachable", target({ host: "192.168.1.5" }));
		expect(d.message).toContain("Wi-Fi");
	});

	it("does not blame Wi-Fi for an unreachable Tailscale host, and stays off the Local Network hint", () => {
		const d = describeConnectionFailure("unreachable", target({ host: "100.101.102.103" }));
		expect(d.message).not.toContain("Wi-Fi");
		expect(d.message.toLowerCase()).toContain("tailscale");
		expect(d.showLocalNetworkHint).toBe(false);
	});

	it("blames the password, not the network, on a 401", () => {
		const d = describeConnectionFailure("auth", target());
		expect(d.message).toContain("rotated");
		expect(d.message).not.toContain("Wi-Fi");
		expect(d.showLocalNetworkHint).toBe(false);
	});

	// The board's empty state shows the title on its own line, so a 401 must not
	// be labelled "disconnected" — the connection worked, the password didn't.
	it("gives every cause a distinct, non-empty title", () => {
		const titles = (["not-ao-qr", "unreachable", "auth", "rate-limited", "server-error"] as const).map(
			(r) => describeConnectionFailure(r, target()).title,
		);
		expect(titles.every((t) => t.length > 0)).toBe(true);
		expect(new Set(titles).size).toBe(titles.length);
		expect(describeConnectionFailure("auth", target()).title).not.toContain("disconnected");
		expect(describeConnectionFailure("unreachable", target()).title).toContain("disconnected");
	});

	it("explains the lockout on a 429, and that it clears itself", () => {
		const d = describeConnectionFailure("rate-limited", target());
		expect(d.message).toContain("locked this device out");
		expect(d.message).toContain("about a minute");
	});

	it("points at the desktop logs on a server error", () => {
		const d = describeConnectionFailure("server-error", target());
		expect(d.message).toContain("AO logs");
	});

	it("rejects a non-AO QR code without mentioning the network", () => {
		const d = describeConnectionFailure("not-ao-qr", target());
		expect(d.message).toContain("isn't an AO pairing code");
		expect(d.showLocalNetworkHint).toBe(false);
	});

	describe("the iOS Local Network hint", () => {
		it("shows for an unreachable LAN host on iOS", () => {
			const d = describeConnectionFailure("unreachable", target({ platform: "ios", host: "192.168.1.5" }));
			expect(d.showLocalNetworkHint).toBe(true);
		});

		it("does not show on Android, which has no such prompt", () => {
			const d = describeConnectionFailure("unreachable", target({ platform: "android", host: "192.168.1.5" }));
			expect(d.showLocalNetworkHint).toBe(false);
		});

		it("does not show for a Tailscale host", () => {
			const d = describeConnectionFailure("unreachable", target({ platform: "ios", host: "100.101.102.103" }));
			expect(d.showLocalNetworkHint).toBe(false);
		});

		// The server answered, so the phone plainly has network access to it.
		it("does not show when the server answered", () => {
			const d = describeConnectionFailure("auth", target({ platform: "ios", host: "192.168.1.5" }));
			expect(d.showLocalNetworkHint).toBe(false);
		});
	});
});

describe("shouldKeepPolling", () => {
	// Rejection is permanent until the user acts, and the daemon locks a device
	// out after five failed auths — polling into that blocks the pairing scan
	// meant to fix it.
	it("stops on rejection", () => {
		expect(shouldKeepPolling(401)).toBe(false);
		expect(shouldKeepPolling(403)).toBe(false);
		expect(shouldKeepPolling(429)).toBe(false);
	});

	// These recover on their own, so the poll is what notices.
	it("keeps going on transient failures", () => {
		expect(shouldKeepPolling(undefined)).toBe(true);
		expect(shouldKeepPolling(500)).toBe(true);
		expect(shouldKeepPolling(502)).toBe(true);
		expect(shouldKeepPolling(404)).toBe(true);
	});

	// The previous implementation matched the error *message* against "401"/"429"
	// prefixes. 403 never matched at all, and any rewording of the wire copy
	// silently turned the guard off.
	it("catches 403, which prefix-matching on the message never did", () => {
		expect(shouldKeepPolling(403)).toBe(false);
	});
});

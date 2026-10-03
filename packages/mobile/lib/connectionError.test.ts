import { describe, expect, it } from "vitest";
import {
	classifyConnectionFailure,
	daemonDetail,
	describeConnectionFailure,
	isDesktopUnreachable,
	isLocalNetworkHost,
	isUnreachableError,
	isTailscaleHost,
	shouldKeepPolling,
	UNREACHABLE_ACTION_COPY,
	UnreachableError,
	userFacingError,
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
	it("names an incompatible host and stops polling it", () => {
		expect(classifyConnectionFailure(426)).toBe("incompatible-host");
		expect(describeConnectionFailure("incompatible-host", target()).message).toContain("Update AO");
		expect(shouldKeepPolling(426)).toBe(false);
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
		expect(describeConnectionFailure("unreachable", target()).title).toContain("offline");
	});

	it("explains the lockout on a 429, and that it clears itself", () => {
		const d = describeConnectionFailure("rate-limited", target());
		expect(d.message).toContain("locked this device out");
		expect(d.message).toContain("about a minute");
	});

	it("points at the machine logs on a server error", () => {
		const d = describeConnectionFailure("server-error", target());
		expect(d.message).toContain("AO logs");
	});

	it("rejects a non-AO QR code without mentioning the network", () => {
		const d = describeConnectionFailure("not-ao-qr", target());
		expect(d.message).toContain("isn't an AO pairing code");
		expect(d.showLocalNetworkHint).toBe(false);
	});

	it("handles empty host/port gracefully without showing ':' with no address", () => {
		const d = describeConnectionFailure("unreachable", target({ host: "", port: "" }));
		// With empty host/port, the message should still be valid but not show ":"
		expect(d.message).not.toContain("at :");
		expect(d.message).not.toContain("at ::");
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

describe("userFacingError", () => {
	const answered = (status: number, extra: Record<string, unknown> = {}) =>
		Object.assign(new Error(`${status} Some Reason - wire text`), { status, ...extra });

	it("never shows fetch's own wording or a timeout as-is", () => {
		expect(userFacingError(new UnreachableError("offline"))).toBe(UNREACHABLE_ACTION_COPY);
		expect(userFacingError(new UnreachableError("timeout"))).toBe(UNREACHABLE_ACTION_COPY);
		expect(userFacingError(new TypeError("Network request failed"))).toBe(UNREACHABLE_ACTION_COPY);
	});

	it("uses the pairing copy for rejected passwords and lockouts", () => {
		expect(userFacingError(answered(401))).toMatch(/rejected this phone's password/);
		expect(userFacingError(answered(403))).toMatch(/rejected this phone's password/);
		expect(userFacingError(answered(429))).toMatch(/about a minute/);
	});

	it("shows the daemon's own message for other rejections, without the status line", () => {
		expect(userFacingError(answered(409, { detail: "branch is checked out elsewhere" }))).toBe("Branch is checked out elsewhere.");
		// An error without the detail field still loses its envelope prefix.
		expect(userFacingError(answered(400))).toBe("Wire text.");
	});

	it("falls back to status-specific copy when the daemon said nothing", () => {
		const bare = (status: number) => Object.assign(new Error(`${status} `), { status });
		expect(userFacingError(bare(404))).toBe("That's no longer on your machine. Refresh and try again.");
		expect(userFacingError(bare(409))).toMatch(/changed on your machine/);
		expect(userFacingError(bare(422))).toMatch(/couldn't complete that/);
	});

	it("hides internal server text but keeps the request ID for the logs", () => {
		const copy = userFacingError(answered(500, { detail: "pq: relation does not exist", requestId: "req-42" }));
		expect(copy).not.toContain("pq:");
		expect(copy).not.toContain("500");
		expect(copy).toContain("Reference: req-42");
		expect(userFacingError(answered(503))).toMatch(/still starting up/);
	});

	it("does not mistake a code defect for a lost connection, or show its text", () => {
		const defect = new TypeError("undefined is not a function (evaluating 'x.fetchPage()')");
		expect(isUnreachableError(defect)).toBe(false);
		expect(userFacingError(defect, "Couldn't do that.")).toBe("Couldn't do that.");
		expect(userFacingError(new ReferenceError("foo is not defined"), "Couldn't do that.")).toBe("Couldn't do that.");
	});

	it("recognizes fetch's own failure messages exactly", () => {
		for (const message of ["Network request failed", "Network request timed out", "Failed to fetch", "Load failed"]) {
			expect(isUnreachableError(new TypeError(message))).toBe(true);
		}
		expect(isUnreachableError(new TypeError("Network request failed badly in fetchPage"))).toBe(false);
	});

	it("passes the app's own errors through and uses the fallback otherwise", () => {
		expect(userFacingError(new Error("Pick a project first"))).toBe("Pick a project first");
		expect(userFacingError("boom", "Couldn't start the worker.")).toBe("Couldn't start the worker.");
	});

	it("never renders an HTTP status or reason phrase", () => {
		for (const status of [400, 401, 403, 404, 409, 410, 422, 429, 500, 502, 503]) {
			const copy = userFacingError(Object.assign(new Error(`${status} Not Found`), { status }));
			expect(copy).not.toMatch(/\b[45]\d\d\b/);
			expect(copy).not.toMatch(/Not Found/);
		}
	});
});

describe("daemonDetail", () => {
	it("reads the detail field, else strips the envelope, and ignores unanswered errors", () => {
		expect(daemonDetail(Object.assign(new Error("409 Conflict - x"), { status: 409, detail: "Clean" }))).toBe("Clean");
		expect(daemonDetail(Object.assign(new Error("409 Conflict - Stripped"), { status: 409 }))).toBe("Stripped");
		expect(daemonDetail(new Error("409 Conflict - no status field"))).toBeUndefined();
	});
});

describe("isDesktopUnreachable", () => {
	const poll = (errorStatus: number | null, over: Partial<{ connection: string; error: string | null }> = {}) => ({
		connection: "closed",
		error: "failed",
		errorStatus,
		...over,
	});

	it("is true only when the last poll got no answer", () => {
		expect(isDesktopUnreachable(poll(null))).toBe(true);
	});

	// A rejection stops the poll for good, so promising a reconnect would be a lie
	// and would hide the copy that says to re-scan the pairing code.
	it("is false for rejections and for errors the desktop answered with", () => {
		for (const status of [401, 403, 429, 500, 503]) expect(isDesktopUnreachable(poll(status))).toBe(false);
	});

	it("is false while connected and before any poll has failed", () => {
		expect(isDesktopUnreachable(poll(null, { connection: "open", error: null }))).toBe(false);
		expect(isDesktopUnreachable(poll(null, { error: null }))).toBe(false);
	});
});

describe("connection failure icons", () => {
	const target = { host: "192.168.1.5", port: "3011", platform: "ios" };
	// Every cause used to share "wifi-off", so a rotated password looked like a
	// network problem at a glance. Each board-facing cause now has its own glyph.
	it.each([
		["auth", "monitor-off"],
		["unreachable", "unplug"],
		["rate-limited", "timer"],
		["server-error", "monitor-cog"],
		["tunnel-rotated", "route-off"],
	] as const)("%s shows %s", (reason, icon) => {
		expect(describeConnectionFailure(reason, target).icon).toBe(icon);
	});
});

describe("connection failure hint", () => {
	// The board's empty states show only a title and buttons; a disconnect is the
	// one cause that keeps a short line, because the fix is on the user's side.
	it("gives a disconnect one short line, matched to the network in use", () => {
		expect(describeConnectionFailure("unreachable", { host: "192.168.1.5", port: "3011", platform: "ios" }).hint).toBe(
			"Check you're on the same Wi-Fi.",
		);
		expect(describeConnectionFailure("unreachable", { host: "100.101.102.103", port: "3011", platform: "ios" }).hint).toBe(
			"Check Tailscale is on for both devices.",
		);
	});

	it("leaves every other cause without one", () => {
		for (const reason of ["auth", "rate-limited", "server-error", "tunnel-rotated"] as const) {
			expect(describeConnectionFailure(reason, { host: "192.168.1.5", port: "3011", platform: "ios" }).hint).toBeUndefined();
		}
	});
});

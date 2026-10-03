import { describe, expect, it, vi } from "vitest";
import { IncompatibleRemoteVersionError, probeRemote, readRemoteIdentity } from "./remote-request";

const entry = { label: "workbox", url: "http://192.0.2.1:3011", password: "pw" };

// Typed as fetch so `mock.calls` carries fetch's argument tuple — an untyped
// `vi.fn(async () => …)` records a zero-length tuple and indexing it is a type error.
function fakeFetch(status: number, body: unknown = {}) {
	return vi.fn<typeof fetch>(async () => new Response(JSON.stringify(body), { status }));
}

// A body that is not JSON at all, the way an SPA catch-all answers every path.
function fakeTextFetch(status: number, text: string) {
	return vi.fn<typeof fetch>(async () => new Response(text, { status }));
}

const daemonProbe = { status: "ok", service: "agent-orchestrator-daemon", pid: 1234 };

describe("readRemoteIdentity", () => {
	it("learns the host ID without sending a credential", async () => {
		const doFetch = fakeFetch(200, { hostId: "h_workbox", apiVersion: 1 });
		await expect(readRemoteIdentity(entry, doFetch)).resolves.toBe("h_workbox");
		const [url, init] = doFetch.mock.calls[0] as unknown as [string, RequestInit];
		expect(url).toBe("http://192.0.2.1:3011/api/v1/identity");
		expect(new Headers(init.headers).has("Authorization")).toBe(false);
		expect(init.redirect).toBe("error");
	});
	it("rejects the desktop's reserved local host ID", async () => {
		await expect(readRemoteIdentity(entry, fakeFetch(200, { hostId: "local" }))).rejects.toThrow(/reserved/);
	});
	it.each([undefined, 2])("rejects incompatible API version %s before using the credential", async (apiVersion) => {
		await expect(readRemoteIdentity(entry, fakeFetch(200, { hostId: "h_workbox", apiVersion })))
			.rejects.toBeInstanceOf(IncompatibleRemoteVersionError);
	});
});

describe("probeRemote", () => {
	it("reports online on a 200 that carries the daemon's own probe body", async () => {
		await expect(probeRemote(entry, fakeFetch(200, daemonProbe))).resolves.toBe("online");
	});

	it("sends the saved credential only to the configured host and never follows redirects", async () => {
		const doFetch = fakeFetch(200, daemonProbe);
		await probeRemote({ ...entry, hostId: "h_workbox", url: "http://192.0.2.1/ao/" }, doFetch);
		const [url, init] = doFetch.mock.calls[0] as unknown as [string, RequestInit];
		expect(url).toBe("http://192.0.2.1/ao/healthz");
		expect(init.redirect).toBe("error");
		expect(new Headers(init.headers).get("Authorization")).toBe("Bearer pw");
		expect(new Headers(init.headers).get("X-AO-Expected-Host-ID")).toBe("h_workbox");
		await probeRemote({ ...entry, url: "workbox:3011" }, doFetch);
		expect(doFetch.mock.calls[1][0]).toBe("http://workbox:3011/healthz");
	});

	it("refuses embedded URL credentials before sending the saved credential", async () => {
		const doFetch = fakeFetch(200, daemonProbe);
		await expect(probeRemote({ ...entry, url: "http://attacker:secret@192.0.2.1:3011" }, doFetch)).resolves.toBe("offline");
		expect(doFetch).not.toHaveBeenCalled();
	});

	it("distinguishes a bad password from an unreachable host", async () => {
		await expect(probeRemote(entry, fakeFetch(401))).resolves.toBe("unauthorized");
		const refused = vi.fn(async () => {
			throw new TypeError("fetch failed");
		});
		await expect(probeRemote(entry, refused)).resolves.toBe("offline");
	});

	it("bounds probes to sleeping hosts", async () => {
		const sleeping = vi.fn<typeof fetch>((_input, init) =>
			new Promise((_resolve, reject) => {
				init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
			}),
		);

		await expect(probeRemote(entry, sleeping, 1)).resolves.toBe("offline");
		expect(sleeping.mock.calls[0]?.[1]?.signal).toBeInstanceOf(AbortSignal);
	});

	// The port typo that white-screened the app: :8081 was an Expo web server,
	// whose SPA catch-all answers /healthz with 200 and an HTML page. A status
	// code proves something replied, not that it speaks the daemon's protocol —
	// and once such a host became the api base, every query returned that page.
	it("rejects a 200 whose body is not a daemon probe", async () => {
		const html = fakeTextFetch(200, "<!DOCTYPE html><html><title>AO</title></html>");
		await expect(probeRemote(entry, html)).resolves.toBe("not-a-daemon");
	});

	it("rejects a 200 from a JSON service that is not the daemon", async () => {
		await expect(probeRemote(entry, fakeFetch(200, { status: "ok" }))).resolves.toBe("not-a-daemon");
		await expect(probeRemote(entry, fakeFetch(200, { ...daemonProbe, service: "grafana" }))).resolves.toBe(
			"not-a-daemon",
		);
	});
});

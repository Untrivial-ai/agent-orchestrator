import { describe, expect, it, vi } from "vitest";
import { probeRemote, remoteRequest } from "./remote-request";

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

describe("remoteRequest", () => {
	it("sends the connection password as a Bearer token", async () => {
		const doFetch = fakeFetch(201, { id: "p1" });
		await remoteRequest(entry, { method: "POST", path: "/api/v1/projects", body: { path: "/srv/repo" } }, doFetch);

		const [url, init] = doFetch.mock.calls[0] as unknown as [string, RequestInit];
		expect(url).toBe("http://192.0.2.1:3011/api/v1/projects");
		expect(init.redirect).toBe("error");
		expect(new Headers(init.headers).get("Authorization")).toBe("Bearer pw");
		expect(init.body).toBe('{"path":"/srv/repo"}');
	});

	it("returns the status and parsed body rather than throwing on 4xx", async () => {
		const doFetch = fakeFetch(400, { error: "path must be absolute" });
		await expect(remoteRequest(entry, { method: "POST", path: "/api/v1/projects" }, doFetch)).resolves.toEqual({
			status: 400,
			body: { error: "path must be absolute" },
		});
	});

	// A path is concatenated onto the host url, and "@" turns everything before
	// it into userinfo — so an unchecked path is a way to post this host's
	// connection password to someone else's server.
	it("refuses a path that redirects the credential to another host", async () => {
		const doFetch = fakeFetch(200);
		await expect(remoteRequest(entry, { method: "GET", path: "@evil.example/steal" }, doFetch)).rejects.toThrow(
			/evil\.example/,
		);
		expect(doFetch).not.toHaveBeenCalled();
	});

	it("keeps a protocol-relative path on the saved host", async () => {
		const doFetch = fakeFetch(200);
		await remoteRequest(entry, { method: "GET", path: "//evil.example/x" }, doFetch);
		expect(doFetch.mock.calls[0][0]).toBe("http://192.0.2.1:3011//evil.example/x");
	});

	it("keeps a reverse-proxy path prefix", async () => {
		const doFetch = fakeFetch(200);
		await remoteRequest({ ...entry, url: "http://192.0.2.1/ao" }, { method: "GET", path: "/healthz" }, doFetch);
		expect(doFetch.mock.calls[0][0]).toBe("http://192.0.2.1/ao/healthz");
	});

	it("joins paths without doubling the slash on a trailing-slash url", async () => {
		const doFetch = fakeFetch(200);
		await remoteRequest({ ...entry, url: "http://192.0.2.1:3011/" }, { method: "GET", path: "/healthz" }, doFetch);
		expect(doFetch.mock.calls[0][0]).toBe("http://192.0.2.1:3011/healthz");
	});

	it("accepts a scheme-less URL saved for the CLI", async () => {
		const doFetch = fakeFetch(200);
		await remoteRequest({ ...entry, url: "workbox:3011" }, { method: "GET", path: "/healthz" }, doFetch);
		expect(doFetch.mock.calls[0][0]).toBe("http://workbox:3011/healthz");
	});
});

describe("probeRemote", () => {
	it("reports online on a 200 that carries the daemon's own probe body", async () => {
		await expect(probeRemote(entry, fakeFetch(200, daemonProbe))).resolves.toBe("online");
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

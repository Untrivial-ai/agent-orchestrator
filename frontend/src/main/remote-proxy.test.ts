import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createServer, request as httpRequest, type IncomingMessage, type Server } from "node:http";
import { connect as netConnect, type AddressInfo } from "node:net";
import { startRemoteProxy, type ActiveProxy } from "./remote-proxy";

type Seen = {
	url: string;
	auth: string | undefined;
	expectedHost: string | undefined;
	origin: string | undefined;
	appAuth: string | undefined;
	appOrigin: string | undefined;
	body: string;
};

let upstream: Server | undefined;
let proxy: ActiveProxy | undefined;
// Lifecycle logging is the point of these spies, not a side effect to silence:
// every assertion below reads them, and swallowing the output keeps the suite
// readable while the proxy narrates itself.
let logged: string[];
let warned: string[];

beforeEach(() => {
	logged = [];
	warned = [];
	vi.spyOn(console, "log").mockImplementation((message: unknown) => {
		logged.push(String(message));
	});
	vi.spyOn(console, "warn").mockImplementation((message: unknown) => {
		warned.push(String(message));
	});
});

afterEach(async () => {
	vi.restoreAllMocks();
	await proxy?.close();
	upstream?.closeAllConnections();
	await new Promise<void>((resolve) => (upstream ? upstream.close(() => resolve()) : resolve()));
	upstream = undefined;
	proxy = undefined;
});

async function startUpstream(
	handler: (req: IncomingMessage, seen: Seen[]) => { status: number; body: string; headers?: Record<string, string> },
): Promise<{ port: number; seen: Seen[] }> {
	const seen: Seen[] = [];
	upstream = createServer((req, res) => {
		let body = "";
		req.on("data", (chunk) => (body += chunk));
		req.on("end", () => {
			seen.push({
				url: req.url ?? "",
				auth: req.headers.authorization,
				expectedHost: req.headers["x-ao-expected-host-id"] as string | undefined,
				origin: req.headers.origin,
				appAuth: req.headers["x-ao-preview-app-authorization"] as string | undefined,
				appOrigin: req.headers["x-ao-preview-app-origin"] as string | undefined,
				body,
			});
			const out = handler(req, seen);
			res.writeHead(out.status, { "content-type": "application/json", ...out.headers });
			res.end(out.body);
		});
	});
	await new Promise<void>((resolve) => upstream?.listen(0, "127.0.0.1", resolve));
	return { port: (upstream.address() as AddressInfo).port, seen };
}

describe("startRemoteProxy", () => {
	it("scopes a stable static preview origin to one session and serves root assets", async () => {
		const { port, seen } = await startUpstream((request) => ({
			status: request.url === "/api/v1/projects" ? 200 : 200,
			body: request.url === "/api/v1/projects" ? "SECRET API" : "preview file",
		}));
		proxy = await startRemoteProxy({ label: "workbox", url: `http://127.0.0.1:${port}`, password: "pw" });
		const source = `http://ao-preview.mfxs2mi.localhost:${port}/dist/index.html`;
		const preview = proxy.previewUrl("ao-1", source);
		expect(proxy.resolvePreviewUrl("ao-1", new URL("/design?x=1#note", preview).href)).toBe(`http://ao-preview.mfxs2mi.localhost:${port}/design?x=1#note`);
		expect(proxy.resolvePreviewUrl("ao-2", preview)).toBe("");
		expect(proxy.resolvePreviewUrl("ao-1", "https://example.com/design")).toBe("https://example.com/design");
		const shorthand = proxy.previewUrl("ao-3", "localhost:5173/");
		expect(proxy.resolvePreviewUrl("ao-3", new URL("/details", shorthand).href)).toBe("http://localhost:5173/details");
		expect(proxy.previewUrl("ao-1", source)).toBe(preview);
		expect(new URL(preview).hostname).not.toBe(new URL(proxy.base).hostname);
		expect(preview).not.toContain(new URL(proxy.base).pathname.slice(1));
		const request = async (url: string): Promise<{ status: number; body: string }> => {
			const target = new URL(url);
			return new Promise((resolve, reject) => {
				const req = httpRequest({ hostname: "127.0.0.1", port: Number(new URL(proxy!.base).port), path: target.pathname + target.search, headers: { Host: target.host } }, (res) => {
					let body = "";
					res.on("data", (chunk) => { body += chunk.toString(); });
					res.on("end", () => resolve({ status: res.statusCode ?? 0, body }));
				});
				req.on("error", reject);
				req.end();
			});
		};
		const asset = await request(new URL("/assets/app.css", preview).href);
		expect(asset.body).toBe("preview file");
		expect(seen.at(-1)?.url).toBe("/api/v1/sessions/ao-1/preview/files/dist/assets/app.css");
		const attemptedAPI = await request(new URL("/api/v1/projects", preview).href);
		expect(attemptedAPI.body).toBe("preview file");
		expect(seen.at(-1)?.url).toBe("/api/v1/sessions/ao-1/preview/files/dist/api/v1/projects");
		expect(() => proxy!.previewUrl("ao-2", source)).toThrow(/unsupported|preview/i);
		expect(new URL(proxy.previewUrl("ao-2", "http://127.0.0.2:5173/")).hostname).toMatch(/^ao-preview-[0-9a-f]{32}\.localhost$/);
		expect(new URL(proxy.previewUrl("ao-2", "http://[::ffff:127.0.0.1]:5173/")).hostname).toMatch(/^ao-preview-[0-9a-f]{32}\.localhost$/);
		expect(new URL(proxy.previewUrl("ao-2", "http://localhost.:5173/")).hostname).toMatch(/^ao-preview-[0-9a-f]{32}\.localhost$/);
		const changed = proxy.previewUrl("ao-1", `http://localhost:5173/`);
		expect(changed).not.toBe(preview);
		expect(proxy.resolvePreviewUrl("ao-1", preview)).toBe("");
		expect((await request(preview)).status).toBe(404);
		expect(proxy.previewUrl("ao-1", "https://example.com/")).toBe("https://example.com/");
		expect((await request(changed)).status).toBe(404);
		const clearing = proxy.previewUrl("ao-1", "http://localhost:5173/");
		expect(proxy.previewUrl("ao-1", "")).toBe("");
		expect((await request(clearing)).status).toBe(404);
	});

	it("relays app Authorization and same-origin intent only on preview requests", async () => {
		const { port, seen } = await startUpstream(() => ({ status: 200, body: "ok" }));
		proxy = await startRemoteProxy({ label: "workbox", url: `http://127.0.0.1:${port}`, password: "host-password" });
		const preview = new URL(proxy.previewUrl("ao-1", "http://localhost:5173/"));
		const post = (origin?: string, authorization?: string) => new Promise<number>((resolve, reject) => {
			const req = httpRequest({
				hostname: "127.0.0.1", port: Number(preview.port), path: "/submit", method: "POST",
				headers: {
					Host: preview.host, ...(origin ? { Origin: origin } : {}), ...(authorization ? { Authorization: authorization } : {}),
					"X-AO-Preview-App-Authorization": "forged", "X-AO-Preview-App-Origin": "forged",
				},
			}, (res) => { res.resume(); res.on("end", () => resolve(res.statusCode ?? 0)); });
			req.on("error", reject);
			req.end("data");
		});
		expect(await post(preview.origin, "Basic app-token")).toBe(200);
		expect(seen.at(-1)).toMatchObject({
			url: "/api/v1/sessions/ao-1/preview/app/submit", auth: "Bearer host-password", origin: undefined,
			appAuth: "Basic app-token", appOrigin: preview.origin, body: "data",
		});
		expect(await post()).toBe(200);
		expect(seen.at(-1)).toMatchObject({ appAuth: undefined, appOrigin: undefined });
		expect(await post("http://evil.example")).toBe(403);
		expect(seen).toHaveLength(2);
	});

	it("does not log preview paths or query secrets on connection failure", async () => {
		const { port } = await startUpstream(() => ({ status: 200, body: '{"hostId":"wrong","apiVersion":1}' }));
		proxy = await startRemoteProxy({ hostId: "expected", label: "workbox", url: `http://127.0.0.1:${port}`, password: "pw" });
		const preview = new URL(proxy.previewUrl("ao-1", "http://localhost:5173/reset/path-secret?code=query-secret"));
		const status = await new Promise<number>((resolve, reject) => {
			const req = httpRequest({ hostname: "127.0.0.1", port: Number(preview.port), path: preview.pathname + preview.search, headers: { Host: preview.host } }, (res) => {
				res.resume();
				res.on("end", () => resolve(res.statusCode ?? 0));
			});
			req.on("error", reject);
			req.end();
		});
		expect(status).toBe(502);
		expect(warned.join("\n")).toContain("preview/app");
		expect(warned.join("\n")).not.toMatch(/path-secret|query-secret/);
	});

	it("shows managed-server guidance only for AO's own preview conflict", async () => {
		let managed = false;
		const { port } = await startUpstream(() => ({
			status: 409, body: managed ? '{"code":"PREVIEW_MANAGED_REQUIRED"}' : "app conflict",
			headers: managed ? { "x-ao-preview-managed-required": "1" } : undefined,
		}));
		proxy = await startRemoteProxy({ label: "workbox", url: `http://127.0.0.1:${port}`, password: "pw" });
		const preview = new URL(proxy.previewUrl("ao-1", "http://localhost:5173/"));
		const request = () => new Promise<string>((resolve, reject) => {
			const req = httpRequest({ hostname: "127.0.0.1", port: Number(preview.port), path: "/", headers: { Host: preview.host } }, (res) => {
				let body = "";
				res.on("data", (chunk) => { body += chunk.toString(); });
				res.on("end", () => resolve(body));
			});
			req.on("error", reject);
			req.end();
		});
		expect(await request()).toBe("app conflict");
		managed = true;
		expect(await request()).toContain("ao preview start");
	});

	it("forwards a managed preview websocket on its preview-only origin", async () => {
		upstream = createServer((_request, response) => response.end("ok"));
		let upgradePath = "";
		let upgradeAuth = "";
		let upgradeAppAuth = "";
		let upgradeAppOrigin = "";
		upstream.on("upgrade", (request, socket) => {
			upgradePath = request.url ?? "";
			upgradeAuth = String(request.headers.authorization ?? "");
			upgradeAppAuth = String(request.headers["x-ao-preview-app-authorization"] ?? "");
			upgradeAppOrigin = String(request.headers["x-ao-preview-app-origin"] ?? "");
			socket.write("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n");
			socket.end();
		});
		await new Promise<void>((resolve) => upstream?.listen(0, "127.0.0.1", resolve));
		const port = (upstream.address() as AddressInfo).port;
		proxy = await startRemoteProxy({ label: "workbox", url: `http://127.0.0.1:${port}`, password: "pw" });
		const preview = new URL(proxy.previewUrl("ao-1", "http://localhost:5173/"));
		const socket = netConnect(Number(preview.port), "127.0.0.1");
		const reply = await new Promise<string>((resolve, reject) => {
			let data = "";
			socket.on("error", reject);
			socket.on("data", (chunk) => {
				data += chunk.toString();
				if (data.includes("\r\n\r\n")) resolve(data);
			});
			socket.on("connect", () => socket.write(`GET /socket HTTP/1.1\r\nHost: ${preview.host}\r\nOrigin: ${preview.origin}\r\nAuthorization: Basic app-token\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGVzdA==\r\nSec-WebSocket-Version: 13\r\n\r\n`));
		});
		socket.destroy();
		expect(reply).toContain("101 Switching Protocols");
		expect(upgradePath).toBe("/api/v1/sessions/ao-1/preview/app/socket");
		expect(upgradeAuth).toBe("Bearer pw");
		expect(upgradeAppAuth).toBe("Basic app-token");
		expect(upgradeAppOrigin).toBe(preview.origin);
	});
	it("proxies an IPv6 host instead of dialing its URL brackets", async (context) => {
		upstream = createServer((_request, response) => response.end("ok"));
		try {
			await new Promise<void>((resolve, reject) => {
				upstream?.once("error", reject);
				upstream?.listen(0, "::1", resolve);
			});
		} catch (error) {
			if (["EADDRNOTAVAIL", "EAFNOSUPPORT"].includes((error as NodeJS.ErrnoException).code ?? "")) {
				upstream = undefined;
				context.skip();
			}
			throw error;
		}
		const port = (upstream.address() as AddressInfo).port;
		proxy = await startRemoteProxy({ label: "IPv6 box", url: `http://[::1]:${port}`, password: "pw" });
		const response = await fetch(`${proxy.base}/api/v1/projects`);
		expect(response.status).toBe(200);
		expect(await response.text()).toBe("ok");
	});

	it("allows only the Forge dev renderer origin when configured", async () => {
		const { port } = await startUpstream(() => ({ status: 200, body: "{}" }));
		proxy = await startRemoteProxy(
			{ label: "workbox", url: `http://127.0.0.1:${port}`, password: "pw" },
			"http://localhost:5173",
		);
		const response = await fetch(`${proxy.base}/api/v1/projects`, {
			method: "OPTIONS",
			headers: { Origin: "http://localhost:5173" },
		});
		expect(response.headers.get("access-control-allow-origin")).toBe("http://localhost:5173");
		const foreign = await fetch(`${proxy.base}/api/v1/projects`, {
			method: "OPTIONS",
			headers: { Origin: "http://evil.example" },
		});
		expect(foreign.headers.get("access-control-allow-origin")).toBe("http://localhost:5173");
	});

	it("serves a verified host with the token stripped", async () => {
		const { port, seen } = await startUpstream((request) => ({
			status: 200,
			body: request.url === "/api/v1/identity"
				? '{"hostId":"h_workbox","apiVersion":1}'
				: '{"ok":true}',
		}));
		proxy = await startRemoteProxy({ hostId: "h_workbox", label: "workbox", url: `http://127.0.0.1:${port}`, password: "secret" });
		const response = await fetch(`${proxy.base}/api/v1/projects`, {
			headers: { "X-AO-Expected-Host-ID": "h_spoofed" },
		});
		expect(response.status).toBe(200);
		expect(seen.map(({ url, auth, expectedHost }) => ({ url, auth, expectedHost }))).toEqual([
			{ url: "/api/v1/identity", auth: undefined, expectedHost: undefined },
			{ url: "/api/v1/projects", auth: "Bearer secret", expectedHost: "h_workbox" },
		]);
	});

	it("rechecks host identity before a later request can send its credential", async () => {
		const { port, seen } = await startUpstream((request) => ({
			status: 200,
			body: request.url === "/api/v1/identity"
				? '{"hostId":"h_replacement","apiVersion":1}'
				: "{}",
		}));
		proxy = await startRemoteProxy({
			hostId: "h_expected",
			label: "workbox",
			url: `http://127.0.0.1:${port}`,
			password: "secret",
		});
		const response = await fetch(`${proxy.base}/api/v1/projects?code=secret-code`);
		expect(response.status).toBe(502);
		expect(await response.json()).toEqual({
			error: "remote host identity not verified",
			code: "HOST_IDENTITY_UNVERIFIED",
			message: "remote host identity not verified",
		});
		expect(seen.map((request) => request.url)).toEqual(["/api/v1/identity"]);
		expect(seen[0].auth).toBeUndefined();
		expect(warned.join("\n")).not.toContain("secret-code");
	});

	it("stops forwarding after a host upgrades to an incompatible API", async () => {
		const { port, seen } = await startUpstream((request) => ({
			status: 200,
			body: request.url === "/api/v1/identity"
				? '{"hostId":"h_workbox","apiVersion":2}'
				: "{}",
		}));
		proxy = await startRemoteProxy({ hostId: "h_workbox", label: "workbox", url: `http://127.0.0.1:${port}`, password: "secret" });
		const response = await fetch(`${proxy.base}/api/v1/projects`);
		expect(response.status).toBe(426);
		expect(await response.json()).toMatchObject({ code: "HOST_API_INCOMPATIBLE", message: expect.stringContaining("Update AO") });
		expect(seen.map((request) => request.url)).toEqual(["/api/v1/identity"]);
		expect(seen[0].auth).toBeUndefined();
	});

	it("connects a saved address without an explicit scheme", async () => {
		const { port, seen } = await startUpstream(() => ({ status: 200, body: "{}" }));
		proxy = await startRemoteProxy({ label: "workbox", url: `127.0.0.1:${port}`, password: "pw" });
		const response = await fetch(`${proxy.base}/healthz`);
		expect(response.status).toBe(200);
		expect(seen[0].auth).toBe("Bearer pw");
	});

	it("forwards with the token stripped and the credential injected", async () => {
		const { port, seen } = await startUpstream(() => ({
			status: 200,
			body: '{"ok":true}',
		}));
		proxy = await startRemoteProxy({
			label: "workbox",
			url: `http://127.0.0.1:${port}`,
			password: "pw",
		});

		const res = await fetch(`${proxy.base}/api/v1/projects`, {
			method: "POST",
			headers: { "content-type": "application/json", origin: "app://renderer" },
			body: '{"path":"/srv/repo"}',
		});

		expect(res.status).toBe(200);
		expect(await res.json()).toEqual({ ok: true });
		expect(seen).toHaveLength(1);
		expect(seen[0].url).toBe("/api/v1/projects"); // token gone
		expect(seen[0].auth).toBe("Bearer pw");
		expect(seen[0].origin).toBeUndefined(); // app://renderer never reaches the daemon
		expect(seen[0].body).toBe('{"path":"/srv/repo"}');
	});

	// The add-host dialog accepts https://, so this is what a Tailscale Serve or
	// reverse-proxy address does today: the upstream is a TLS endpoint and the
	// proxy must not put the connection password on the wire in the clear.
	it("never sends the credential in the clear to an https host", async () => {
		const { port, seen } = await startUpstream(() => ({ status: 200, body: "{}" }));
		proxy = await startRemoteProxy({
			label: "workbox",
			url: `https://127.0.0.1:${port}`,
			password: "pw",
		});

		// A cleartext listener cannot complete a TLS handshake, so the honest
		// outcome is a failed request — never a plaintext one that succeeds.
		const res = await fetch(`${proxy.base}/api/v1/projects`);
		expect(res.status).toBe(502);
		expect(await res.json()).toEqual({
			error: "remote daemon unreachable",
			code: "UPSTREAM_UNAVAILABLE",
			message: "remote daemon unreachable",
		});
		expect(seen).toHaveLength(0);
	});

	// A daemon can live behind a reverse proxy at a path. The renderer's base is
	// the loopback proxy's own root, so the upstream prefix is restored here or
	// every credentialled request lands on whatever else that vhost serves.
	it("restores the host's path prefix upstream", async () => {
		const { port, seen } = await startUpstream(() => ({ status: 200, body: "{}" }));
		proxy = await startRemoteProxy({
			label: "workbox",
			url: `http://127.0.0.1:${port}/ao`,
			password: "pw",
		});

		await fetch(`${proxy.base}/api/v1/projects`);
		expect(seen[0].url).toBe("/ao/api/v1/projects");
	});

	it("refuses a request without the token and sends nothing upstream", async () => {
		const { port, seen } = await startUpstream(() => ({
			status: 200,
			body: "{}",
		}));
		proxy = await startRemoteProxy({
			label: "workbox",
			url: `http://127.0.0.1:${port}`,
			password: "pw",
		});

		const bare = new URL(proxy.base);
		const res = await fetch(`${bare.origin}/api/v1/projects`);
		expect(res.status).toBe(404);
		expect(seen).toHaveLength(0);
	});

	it("refuses a near-miss token prefix", async () => {
		const { port, seen } = await startUpstream(() => ({
			status: 200,
			body: "{}",
		}));
		proxy = await startRemoteProxy({
			label: "workbox",
			url: `http://127.0.0.1:${port}`,
			password: "pw",
		});

		// A path that merely starts with the token's characters is not the token:
		// /<token>x/... must not authorize, or the token stops being a boundary.
		const bare = new URL(proxy.base);
		const res = await fetch(`${bare.origin}${bare.pathname}x/api/v1/projects`);
		expect(res.status).toBe(404);
		expect(seen).toHaveLength(0);
	});

	it("answers CORS preflight itself for the renderer origin", async () => {
		const { port, seen } = await startUpstream(() => ({
			status: 200,
			body: "{}",
		}));
		proxy = await startRemoteProxy({
			label: "workbox",
			url: `http://127.0.0.1:${port}`,
			password: "pw",
		});

		const res = await fetch(`${proxy.base}/api/v1/projects`, {
			method: "OPTIONS",
			headers: {
				origin: "app://renderer",
				"access-control-request-method": "POST",
				"access-control-request-headers": "content-type",
				"access-control-request-private-network": "true",
			},
		});
		expect(res.status).toBe(204);
		expect(res.headers.get("access-control-allow-origin")).toBe("app://renderer");
		expect(res.headers.get("access-control-allow-headers")).toMatch(/content-type/i);
		expect(res.headers.get("access-control-allow-private-network")).toBe("true");
		expect(seen).toHaveLength(0); // preflight never leaves the machine
	});

	it("adds the renderer origin to real responses so cross-origin fetch succeeds", async () => {
		const { port } = await startUpstream(() => ({
			status: 400,
			body: '{"error":"bad"}',
		}));
		proxy = await startRemoteProxy({
			label: "workbox",
			url: `http://127.0.0.1:${port}`,
			password: "pw",
		});

		const res = await fetch(`${proxy.base}/api/v1/projects`, {
			headers: { origin: "app://renderer" },
		});
		expect(res.status).toBe(400); // errors pass through untouched…
		expect(res.headers.get("access-control-allow-origin")).toBe("app://renderer"); // …but stay readable
	});

	// "The app can't reach my host" had no answer anywhere before this, and the
	// only thing that could make these lines unshippable is a secret in one.
	it("logs its own lifecycle without the token or the password", async () => {
		const { port } = await startUpstream(() => ({ status: 200, body: "{}" }));
		proxy = await startRemoteProxy({ label: "workbox", url: `http://127.0.0.1:${port}`, password: "hunter2secret" });
		const token = new URL(proxy.base).pathname.slice(1);

		expect(logged.some((line) => line.includes("[remote-proxy] started on 127.0.0.1:"))).toBe(true);
		await proxy.close();
		proxy = undefined;
		expect(logged.some((line) => line.includes("[remote-proxy] stopped on 127.0.0.1:"))).toBe(true);

		const everything = [...logged, ...warned].join("\n");
		expect(everything).not.toContain("hunter2secret");
		expect(everything).not.toContain(token);
	});

	it("warns which upstream failed when it answers 502, naming no secret", async () => {
		const { port } = await startUpstream(() => ({ status: 200, body: "{}" }));
		await new Promise<void>((resolve) => (upstream ? upstream.close(() => resolve()) : resolve()));
		upstream = undefined;
		proxy = await startRemoteProxy({ label: "workbox", url: `http://127.0.0.1:${port}`, password: "hunter2secret" });
		const token = new URL(proxy.base).pathname.slice(1);

		const res = await fetch(`${proxy.base}/api/v1/projects`);
		expect(res.status).toBe(502);
		const warning = warned.find((line) => line.includes("answering 502"));
		expect(warning).toContain(`127.0.0.1:${port}`);
		// The post-strip path, which is the useful half; req.url starts with the token.
		expect(warning).toContain("/api/v1/projects");
		expect(warning).not.toContain("hunter2secret");
		expect(warning).not.toContain(token);
	});

	it("binds only to 127.0.0.1, not every interface", async () => {
		const { port } = await startUpstream(() => ({ status: 200, body: "{}" }));
		proxy = await startRemoteProxy({
			label: "workbox",
			url: `http://127.0.0.1:${port}`,
			password: "pw",
		});
		expect((await fetch(`${proxy.base}/healthz`)).status).toBe(200);
		expect(proxy.listeningAddress).toBe("127.0.0.1");
	});
});

describe("startRemoteProxy streams", () => {
	it("closes a stream when the remote daemon drops it after headers", async () => {
		upstream = createServer((_req, res) => {
			res.writeHead(200, { "content-type": "text/event-stream" });
			res.write("data: first\n\n");
			setTimeout(() => res.destroy(), 20);
		});
		await new Promise<void>((resolve) => upstream?.listen(0, "127.0.0.1", resolve));
		const port = (upstream.address() as AddressInfo).port;
		proxy = await startRemoteProxy({ label: "workbox", url: `http://127.0.0.1:${port}`, password: "pw" });

		const response = await fetch(`${proxy.base}/api/v1/events`);
		expect(response.status).toBe(200);
		await expect(response.text()).rejects.toThrow();
		expect(warned.some((line) => line.includes("stream ended"))).toBe(true);
	});

	it("refuses a WebSocket when the host identity changed", async () => {
		let sawUpgrade = false;
		upstream = createServer((request, response) => {
			response.setHeader("content-type", "application/json");
			response.end(request.url === "/api/v1/identity" ? '{"hostId":"h_other","apiVersion":1}' : "{}");
		});
		upstream.on("upgrade", (_request, socket) => {
			sawUpgrade = true;
			socket.destroy();
		});
		await new Promise<void>((resolve) => upstream?.listen(0, "127.0.0.1", resolve));
		const port = (upstream.address() as AddressInfo).port;
		proxy = await startRemoteProxy({ hostId: "h_expected", label: "workbox", url: `http://127.0.0.1:${port}`, password: "secret" });
		const proxyUrl = new URL(proxy.base);
		const socket = netConnect(Number(proxyUrl.port), "127.0.0.1");
		await new Promise<void>((resolve) => socket.once("connect", resolve));
		const closed = new Promise<void>((resolve) => socket.once("close", () => resolve()));
		socket.write(`GET ${proxyUrl.pathname}/mux HTTP/1.1\r\nHost: 127.0.0.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGVzdA==\r\nSec-WebSocket-Version: 13\r\n\r\n`);
		await closed;
		expect(sawUpgrade).toBe(false);
	});

	it("closes while a WebSocket is waiting for the identity probe", async () => {
		let started!: () => void;
		const identityStarted = new Promise<void>((resolve) => { started = resolve; });
		upstream = createServer((request, response) => {
			if (request.url !== "/api/v1/identity") return;
			started();
			setTimeout(() => {
				response.setHeader("content-type", "application/json");
				response.end('{"hostId":"h_workbox","apiVersion":1}');
			}, 500);
		});
		await new Promise<void>((resolve) => upstream?.listen(0, "127.0.0.1", resolve));
		const port = (upstream.address() as AddressInfo).port;
		proxy = await startRemoteProxy({ hostId: "h_workbox", label: "workbox", url: `http://127.0.0.1:${port}`, password: "pw" });
		const proxyUrl = new URL(proxy.base);
		const socket = netConnect(Number(proxyUrl.port), "127.0.0.1");
		await new Promise<void>((resolve) => socket.once("connect", resolve));
		socket.write(`GET ${proxyUrl.pathname}/mux HTTP/1.1\r\nHost: 127.0.0.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGVzdA==\r\nSec-WebSocket-Version: 13\r\n\r\n`);
		await identityStarted;
		const closing = proxy.close();
		const outcome = await Promise.race([
			closing.then(() => "closed"),
			new Promise<string>((resolve) => setTimeout(() => resolve("pending"), 200)),
		]);
		socket.destroy();
		await closing;
		expect(outcome).toBe("closed");
	});

	it("closes while an upgraded socket is still open", async () => {
		upstream = createServer();
		const upgraded = new Promise<void>((resolve) => {
			upstream?.on("upgrade", (_req, socket) => {
				socket.on("error", () => undefined);
				socket.write("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n");
				socket.on("end", () => socket.destroy());
				resolve();
			});
		});
		await new Promise<void>((resolve) => upstream?.listen(0, "127.0.0.1", resolve));
		const port = (upstream.address() as AddressInfo).port;
		proxy = await startRemoteProxy({
			label: "workbox",
			url: `http://127.0.0.1:${port}`,
			password: "pw",
		});

		const proxyUrl = new URL(proxy.base);
		const socket = netConnect(Number(proxyUrl.port), "127.0.0.1");
		socket.on("error", () => undefined);
		socket.write(
			`GET ${proxyUrl.pathname}/mux HTTP/1.1\r\nHost: 127.0.0.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGVzdA==\r\nSec-WebSocket-Version: 13\r\n\r\n`,
		);
		await upgraded;

		const closing = proxy.close();
		const result = await Promise.race([
			closing.then(() => "closed"),
			new Promise<string>((resolve) => setTimeout(() => resolve("timed out"), 500)),
		]);
		socket.destroy();
		await closing;
		proxy = undefined;

		expect(result).toBe("closed");
	});

	// Regression for the connection-status hang: the previous test's upstream
	// writes its first chunk synchronously, so headers and that chunk reach the
	// client together even without an explicit flush — it never exercised the
	// gap. A real SSE upstream (GET /api/v1/events) can hold its first byte
	// indefinitely; a client's EventSource must still see the response headers
	// right away; otherwise it reports CONNECTING forever, which is exactly what
	// "not receiving live updates" looked like before this was found.
	it("flushes response headers before the first SSE byte arrives", async () => {
		let upstreamClosed = false;
		upstream = createServer((_req, res) => {
			res.once("close", () => { upstreamClosed = true; });
			res.writeHead(200, { "content-type": "text/event-stream" });
			// The real daemon flushes its own headers immediately (confirmed by a
			// direct curl against it) — this upstream must too, or the test would
			// measure the fake upstream's header delay instead of the proxy's.
			res.flushHeaders();
			// The body is what's withheld for 500ms, like a daemon with nothing to
			// say yet.
			setTimeout(() => {
				res.write("data: first\n\n");
				res.end();
			}, 500);
		});
		await new Promise<void>((resolve) => upstream?.listen(0, "127.0.0.1", resolve));
		const port = (upstream.address() as AddressInfo).port;
		proxy = await startRemoteProxy({
			label: "workbox",
			url: `http://127.0.0.1:${port}`,
			password: "pw",
		});

		const proxyUrl = new URL(proxy.base);
		const started = Date.now();
		const head = await new Promise<string>((resolve) => {
			const socket = netConnect(Number(proxyUrl.port), "127.0.0.1", () => {
				socket.write(`GET ${proxyUrl.pathname}/api/v1/events HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n`);
			});
			socket.on("error", () => undefined);
			let buf = "";
			socket.on("data", (chunk: Buffer) => {
				buf += chunk.toString();
				if (buf.includes("\r\n\r\n")) {
					socket.destroy();
					resolve(buf);
				}
			});
		});
		const headersArrivedAfterMs = Date.now() - started;

		expect(head).toContain("200");
		expect(head.toLowerCase()).toContain("content-type: text/event-stream");
		// The upstream withholds its first byte for 500ms; seeing the header
		// block well before that proves the proxy flushes headers on their own
		// rather than only when they can piggyback on the first body write.
		expect(headersArrivedAfterMs).toBeLessThan(300);
		await vi.waitFor(() => expect(upstreamClosed).toBe(true), { timeout: 1_000 });
	});

	it("delivers SSE chunks as they are written, not on close", async () => {
		upstream = createServer((_req, res) => {
			res.writeHead(200, { "content-type": "text/event-stream" });
			res.write("data: first\n\n");
			setTimeout(() => {
				res.write("data: second\n\n");
				res.end();
			}, 500);
		});
		await new Promise<void>((resolve) => upstream?.listen(0, "127.0.0.1", resolve));
		const port = (upstream.address() as AddressInfo).port;
		proxy = await startRemoteProxy({
			label: "workbox",
			url: `http://127.0.0.1:${port}`,
			password: "pw",
		});

		const res = await fetch(`${proxy.base}/api/v1/events`);
		const reader = res.body!.getReader();
		const started = Date.now();
		const first = new TextDecoder().decode((await reader.read()).value);
		const firstArrivedAfterMs = Date.now() - started;

		expect(first).toContain("data: first");
		// The second chunk is written 500ms later; receiving the first well before
		// that proves streaming rather than buffer-until-close.
		expect(firstArrivedAfterMs).toBeLessThan(300);
		let rest = "";
		for (;;) {
			const { done, value } = await reader.read();
			if (done) break;
			rest += new TextDecoder().decode(value);
		}
		expect(rest).toContain("data: second");
	});

	it("tunnels a WebSocket upgrade with the credential injected", async () => {
		const sawAuth: Array<string | undefined> = [];
		upstream = createServer();
		upstream.on("upgrade", (req, socket) => {
			sawAuth.push(req.headers.authorization);
			socket.write("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n");
			socket.on("data", (d) => socket.write(d)); // echo frames back verbatim
			// Upgraded sockets are half-open by default and would hold close() open.
			socket.on("end", () => socket.destroy());
		});
		await new Promise<void>((resolve) => upstream?.listen(0, "127.0.0.1", resolve));
		const port = (upstream.address() as AddressInfo).port;
		proxy = await startRemoteProxy({
			label: "workbox",
			url: `http://127.0.0.1:${port}`,
			password: "pw",
		});

		const proxyUrl = new URL(proxy.base);
		const received: Buffer[] = [];
		const socket = netConnect(Number(proxyUrl.port), "127.0.0.1");
		await new Promise<void>((resolve) => socket.on("connect", () => resolve()));
		socket.on("data", (d) => received.push(d));
		socket.write(
			`GET ${proxyUrl.pathname}/mux HTTP/1.1\r\nHost: 127.0.0.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGVzdA==\r\nSec-WebSocket-Version: 13\r\n\r\n`,
		);
		await new Promise((resolve) => setTimeout(resolve, 200));
		socket.write("payload-bytes");
		await new Promise((resolve) => setTimeout(resolve, 200));
		socket.destroy();

		const all = Buffer.concat(received).toString();
		expect(all).toContain("101 Switching Protocols");
		expect(all).toContain("payload-bytes"); // echoed through both pipes
		expect(sawAuth).toEqual(["Bearer pw"]);
	});

	it("destroys an upgrade that carries no token", async () => {
		const sawUpgrade: string[] = [];
		upstream = createServer();
		upstream.on("upgrade", (upgraded) => sawUpgrade.push(upgraded.url ?? ""));
		await new Promise<void>((resolve) => upstream?.listen(0, "127.0.0.1", resolve));
		const port = (upstream.address() as AddressInfo).port;
		proxy = await startRemoteProxy({
			label: "workbox",
			url: `http://127.0.0.1:${port}`,
			password: "pw",
		});

		const proxyUrl = new URL(proxy.base);
		const socket = netConnect(Number(proxyUrl.port), "127.0.0.1");
		await new Promise<void>((resolve) => socket.on("connect", () => resolve()));
		const closed = new Promise<void>((resolve) => socket.on("close", () => resolve()));
		socket.on("error", () => undefined);
		socket.write(
			"GET /mux HTTP/1.1\r\nHost: 127.0.0.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGVzdA==\r\nSec-WebSocket-Version: 13\r\n\r\n",
		);
		await closed;
		expect(sawUpgrade).toEqual([]);
	});
});

import { createServer, request as httpRequest, type Server } from "node:http";
import { request as httpsRequest } from "node:https";
import { connect as netConnect, isIP, type AddressInfo, type Socket } from "node:net";
import { connect as tlsConnect } from "node:tls";
import { randomBytes, timingSafeEqual } from "node:crypto";
import path from "node:path";
import { IncompatibleRemoteVersionError, readRemoteIdentity } from "./remote-request";
import type { RemoteEntry } from "./remotes-store";

// Loopback proxy fronting one remote AO daemon. It exists because the renderer
// cannot authenticate to a remote daemon itself: EventSource and WebSocket
// cannot set an Authorization header, and app://renderer has no CORS standing
// there. The proxy holds the credential in main-process memory, injects it on
// every forwarded request, and answers CORS for the renderer origin locally.
//
// Loopback is ambient authority everywhere in AO, but this socket fronts a
// DIFFERENT machine — so every request must carry a 128-bit token in the URL
// path (the one place EventSource and WebSocket can both put it). The token is
// stripped before forwarding: the remote daemon and its logs never see it.
export type ActiveProxy = {
	base: string;
	listeningAddress?: string;
	previewUrl: (sessionId: string, sourceUrl: string) => string;
	resolvePreviewUrl: (sessionId: string, viewedUrl: string) => string;
	close: () => Promise<void>;
};

type PreviewTarget = { sessionId: string; kind: "static" | "app"; entry: string };

function previewHostForSession(id: string): string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567";
	let bits = 0;
	let value = 0;
	let encoded = "";
	for (const byte of Buffer.from(id, "utf8")) {
		value = (value << 8) | byte;
		bits += 8;
		while (bits >= 5) {
			encoded += alphabet[(value >>> (bits -= 5)) & 31];
		}
	}
	if (bits) encoded += alphabet[(value << (5 - bits)) & 31];
	return `ao-preview.${encoded.match(/.{1,50}/g)?.join(".")}.localhost`;
}

function previewAssetPath(entry: string, requested: string): string {
	const file = path.posix.normalize(entry).replace(/^\/+/, "");
	const clean = path.posix.normalize(requested).replace(/^\/+/, "");
	if (!clean) return file;
	const root = path.posix.dirname(file);
	if (root === ".") return clean;
	if (clean === root) return file;
	return path.posix.join(root, clean.startsWith(`${root}/`) ? clean.slice(root.length + 1) : clean);
}

const RENDERER_ORIGIN = "app://renderer";
// Hop-by-hop or wrong-machine headers that must not transit the proxy.
const STRIP_REQUEST_HEADERS = [
	"host",
	"origin",
	"connection",
	"keep-alive",
	"transfer-encoding",
	"upgrade",
	"proxy-authorization",
	"x-ao-preview-app-authorization",
	"x-ao-preview-app-origin",
	"x-ao-expected-host-id",
];

// Nothing in this file may log a secret. Not the connection password, not the
// proxy token, and not `req.url` (its first segment IS the token) — only the
// post-strip path, and the upstream address, which is a machine on the user's
// own network named in their own console. That is the whole point: "the app
// can't reach my host" had no answer anywhere before this.
function log(message: string): void {
	console.log(`[remote-proxy] ${message}`);
}

function warn(message: string): void {
	console.warn(`[remote-proxy] ${message}`);
}

function safeLogPath(path: string, previewKind?: PreviewTarget["kind"]): string {
	return previewKind ? `preview/${previewKind}` : path.split("?", 1)[0];
}

function equalsToken(candidate: string, token: string): boolean {
	// Constant-time: the token is the only thing standing between a local
	// process and another machine's daemon, so don't leak it a byte at a time.
	const a = Buffer.from(candidate);
	const b = Buffer.from(token);
	return a.length === b.length && timingSafeEqual(a, b);
}

export async function startRemoteProxy(entry: RemoteEntry, rendererOrigin = RENDERER_ORIGIN): Promise<ActiveProxy> {
	const token = randomBytes(16).toString("hex");
	const corsHeaders: Record<string, string> = {
		"access-control-allow-origin": rendererOrigin,
		"access-control-allow-methods": "GET, POST, PUT, PATCH, DELETE, OPTIONS",
		vary: "origin",
	};
	const upstream = new URL(entry.url.includes("://") ? entry.url : `http://${entry.url}`);
	if (!["http:", "https:"].includes(upstream.protocol) || upstream.username || upstream.password)
		throw new Error("remote host must use an HTTP(S) URL without embedded credentials");
	const secure = upstream.protocol === "https:";
	const upstreamPort = Number(upstream.port || (secure ? 443 : 80));
	const hostname = upstream.hostname.replace(/^\[|\]$/g, "");
	// An https host must be spoken to over TLS. Forwarding it as cleartext puts
	// the connection password on the wire in the clear — and the address bar
	// already accepts https:// (AddRemoteHostDialog), so this is reachable by
	// typing a Tailscale Serve or reverse-proxy URL.
	const upstreamRequest = secure ? httpsRequest : httpRequest;
	const dialUpstream = (onReady: () => void): Socket =>
		secure
			? tlsConnect(upstreamPort, hostname, { servername: isIP(hostname) ? undefined : hostname }, onReady)
			: netConnect(upstreamPort, hostname, onReady);
	// A host may be a daemon behind a reverse proxy at a path ("http://box/ao").
	// The renderer's base is the proxy's own root, so the upstream prefix has to
	// be restored here — without it every forwarded request, credential and all,
	// lands on whatever else that vhost serves at /api/v1.
	const prefix = upstream.pathname.replace(/\/+$/, "");
	const server: Server = createServer();
	const previewHosts = new Map<string, PreviewTarget>();
	const previewSessions = new Map<string, { host: string; sourceUrl: string; sourceHref: string; url: string }>();
	const tunnels = new Set<() => void>();
	// Allow slow uploads; SSE response timeouts are disabled on the upstream request below.
	server.requestTimeout = 0;

	const stripToken = (rawUrl: string | undefined): string | null => {
		if (!rawUrl || !rawUrl.startsWith("/")) return null;
		const slash = rawUrl.indexOf("/", 1);
		const first = slash === -1 ? rawUrl.slice(1) : rawUrl.slice(1, slash);
		if (!equalsToken(first, token)) return null;
		return prefix + (slash === -1 ? "/" : rawUrl.slice(slash));
	};

	const previewPath = (rawHost: string | undefined, rawUrl: string | undefined, method: string | undefined): { path: string; kind: PreviewTarget["kind"] } | null => {
		if (!rawHost || !rawUrl?.startsWith("/")) return null;
		const target = previewHosts.get(rawHost.toLowerCase());
		if (!target) return null;
		if (target.kind === "static" && method !== "GET" && method !== "HEAD") return null;
		const parsed = new URL(rawUrl, "http://localhost");
		const asset = target.kind === "static" ? previewAssetPath(target.entry, parsed.pathname) : parsed.pathname.replace(/^\/+/, "");
		const route = target.kind === "static" ? "files" : "app";
		return { path: `${prefix}/api/v1/sessions/${encodeURIComponent(target.sessionId)}/preview/${route}/${asset}${parsed.search}`, kind: target.kind };
	};

	const forwardHeaders = (incoming: NodeJS.Dict<string | string[]>, previewKind?: PreviewTarget["kind"]): NodeJS.Dict<string | string[]> => {
		const out: NodeJS.Dict<string | string[]> = {};
		for (const [name, value] of Object.entries(incoming)) {
			if (!STRIP_REQUEST_HEADERS.includes(name.toLowerCase())) out[name] = value;
		}
		// The outer Authorization authenticates to AO. Keep app credentials and
		// same-origin intent in separate headers for the managed-app proxy only.
		if (previewKind === "app") {
			if (incoming.authorization) out["x-ao-preview-app-authorization"] = incoming.authorization;
			if (incoming.origin) out["x-ao-preview-app-origin"] = incoming.origin;
		}
		out.host = upstream.host;
		out.authorization = `Bearer ${entry.password}`;
		if (entry.hostId) out["x-ao-expected-host-id"] = entry.hostId;
		return out;
	};

	const validPreviewOrigin = (host: string | undefined, origin: string | undefined, kind?: PreviewTarget["kind"]): boolean =>
		kind !== "app" || !origin || origin === `http://${host}`;

	// Addresses can be reassigned while the desktop stays open. Check on every
	// new HTTP or WebSocket connection, not only when the proxy is first created.
	const verifyUpstream = async (): Promise<void> => {
		if (!entry.hostId) return;
		const actual = await readRemoteIdentity(entry);
		if (actual !== entry.hostId) throw new Error("remote host identity changed");
	};

	server.on("request", async (req, res) => {
		const preview = previewPath(req.headers.host, req.url, req.method);
		const path = preview?.path ?? stripToken(req.url);
		if (path === null) {
			res.writeHead(404, {
				"content-type": "application/json",
				...corsHeaders,
			});
			res.end('{"error":"unknown path"}');
			return;
		}
		if (!validPreviewOrigin(req.headers.host, req.headers.origin, preview?.kind)) {
			res.writeHead(403, { "content-type": "application/json" });
			res.end('{"error":"preview origin mismatch"}');
			return;
		}
		if (req.method === "OPTIONS") {
			res.writeHead(204, {
				...corsHeaders,
				"access-control-allow-headers": String(req.headers["access-control-request-headers"] ?? "content-type"),
				...(req.headers["access-control-request-private-network"] === "true"
					? { "access-control-allow-private-network": "true" }
					: {}),
				"access-control-max-age": "600",
			});
			res.end();
			return;
		}
		try {
			await verifyUpstream();
		} catch (error) {
			warn(`upstream ${upstream.host} identity check failed on ${req.method} ${safeLogPath(path, preview?.kind)} (${(error as Error).message})`);
			const incompatible = error instanceof IncompatibleRemoteVersionError;
			res.writeHead(incompatible ? 426 : 502, { "content-type": "application/json", ...corsHeaders });
			res.end(incompatible
				? JSON.stringify({ error: "incompatible", code: "HOST_API_INCOMPATIBLE", message: error.message })
				: '{"error":"remote host identity not verified","code":"HOST_IDENTITY_UNVERIFIED","message":"remote host identity not verified"}');
			return;
		}
		if (res.destroyed) return;
		const proxied = upstreamRequest(
			{
				host: hostname,
				port: upstreamPort,
				method: req.method,
				path,
				headers: forwardHeaders(req.headers, preview?.kind),
			},
			(upstreamRes) => {
				if (preview?.kind === "app" && upstreamRes.statusCode === 409 && upstreamRes.headers["x-ao-preview-managed-required"] === "1") {
					upstreamRes.resume();
					res.writeHead(409, { "content-type": "text/html; charset=utf-8", "cache-control": "no-store" });
					res.end("<main style='font:16px system-ui;padding:24px'><h1>Remote preview needs a managed server</h1><p>Run <code>ao preview start</code> in this session on the host.</p></main>");
					return;
				}
				let dropped = false;
				const closeDroppedStream = () => {
					if (dropped) return;
					dropped = true;
					warn(`upstream ${upstream.host} stream ended on ${req.method} ${safeLogPath(path, preview?.kind)}`);
					res.destroy();
				};
				upstreamRes.on("aborted", closeDroppedStream);
				upstreamRes.on("error", closeDroppedStream);
				const headers: NodeJS.Dict<string | string[] | number> = {
					...upstreamRes.headers,
					...corsHeaders,
				};
				delete headers.connection;
				delete headers["keep-alive"];
				if (preview?.kind === "static") delete headers["set-cookie"];
				res.writeHead(upstreamRes.statusCode ?? 502, headers);
				// Node holds the header block until the first body byte or an explicit
				// flush. An SSE upstream (GET /api/v1/events) can go arbitrarily long
				// before its first byte, so without this the client's EventSource sits
				// in CONNECTING forever — flush headers on their own, then pipe.
				res.flushHeaders();
				// pipe streams SSE chunk-by-chunk once headers are already on the wire.
				upstreamRes.pipe(res);
			},
		);
		proxied.setTimeout(0);
		proxied.on("error", (error: Error) => {
			warn(`upstream ${upstream.host} failed on ${req.method} ${safeLogPath(path, preview?.kind)} (${error.message}); answering 502`);
			if (res.headersSent) {
				res.destroy();
				return;
			}
			res.writeHead(502, {
				"content-type": "application/json",
				...corsHeaders,
			});
			res.end('{"error":"remote daemon unreachable","code":"UPSTREAM_UNAVAILABLE","message":"remote daemon unreachable"}');
		});
		// Closing an EventSource or tab must also close its upstream stream.
		// Otherwise the remote daemon keeps the SSE request alive after the
		// renderer is gone, and proxy shutdown can leave orphaned connections.
		res.once("close", () => {
			if (!res.writableFinished) proxied.destroy();
		});
		req.pipe(proxied);
	});

	server.on("upgrade", (req, socket: Socket, head: Buffer) => {
		const preview = previewPath(req.headers.host, req.url, req.method);
		const path = preview?.kind === "app" ? preview.path : stripToken(req.url);
		if (path === null) {
			socket.destroy();
			return;
		}
		if (!validPreviewOrigin(req.headers.host, req.headers.origin, preview?.kind)) {
			socket.destroy();
			return;
		}
		const stopPending = () => {
			tunnels.delete(stopPending);
			socket.destroy();
		};
		tunnels.add(stopPending);
		socket.once("close", stopPending);
		void verifyUpstream().then(() => {
			tunnels.delete(stopPending);
			socket.off("close", stopPending);
			if (socket.destroyed) return;
			const upstreamSocket = dialUpstream(() => {
				const headers = forwardHeaders(req.headers, preview?.kind);
				const lines = [`${req.method} ${path} HTTP/1.1`];
				// The WebSocket-specific headers were stripped as hop-by-hop; restore
				// the two the handshake requires, with the credential injected above.
				headers.connection = "Upgrade";
				headers.upgrade = String(req.headers.upgrade ?? "websocket");
				for (const [name, value] of Object.entries(headers)) {
					for (const v of Array.isArray(value) ? value : [value]) lines.push(`${name}: ${v}`);
				}
				upstreamSocket.write(lines.join("\r\n") + "\r\n\r\n");
				if (head.length > 0) upstreamSocket.write(head);
				socket.pipe(upstreamSocket);
				upstreamSocket.pipe(socket);
			});
			upstreamSocket.on("error", (error: Error) => {
				warn(`upstream ${upstream.host} tunnel failed on ${safeLogPath(path, preview?.kind)} (${error.message})`);
			});
			const drop = () => {
				tunnels.delete(drop);
				socket.destroy();
				upstreamSocket.destroy();
			};
			tunnels.add(drop);
			// http.Server keeps upgraded sockets half-open (allowHalfOpen), so a peer
			// that only sends FIN never triggers "close" — tear the pair down on
			// "end" too or every closed terminal leaks a socket to the remote host.
			for (const event of ["error", "end", "close"] as const) {
				upstreamSocket.on(event, drop);
				socket.on(event, drop);
			}
		}).catch((error: Error) => {
			warn(`upstream ${upstream.host} identity check failed on ${safeLogPath(path, preview?.kind)} (${error.message})`);
			stopPending();
		});
	});

	await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
	const { address: listeningAddress, port } = server.address() as AddressInfo;
	log(`started on 127.0.0.1:${port} for ${upstream.host}`);
	return {
		base: `http://127.0.0.1:${port}/${token}`,
		listeningAddress,
		previewUrl: (sessionId, sourceUrl) => {
			if (!sessionId || sessionId.length > 256) throw new Error("invalid preview session");
			const previous = previewSessions.get(sessionId);
			if (previous?.sourceUrl === sourceUrl) return previous.url;
			if (previous) {
				previewHosts.delete(previous.host);
				previewSessions.delete(sessionId);
			}
			const raw = sourceUrl.trim();
			if (!raw) return "";
			const parsed = new URL(/^(?:localhost|127\.\d+\.\d+\.\d+|0\.0\.0\.0|\[::1\])(?::\d+)?(?:[/?#]|$)/i.test(raw) ? `http://${raw}` : raw);
			if (parsed.protocol !== "http:" && parsed.protocol !== "https:") throw new Error("unsupported preview URL");
			let kind: PreviewTarget["kind"];
			const previewHost = parsed.hostname.replace(/^\[|\]$/g, "").replace(/\.+$/, "");
			const loopback = previewHost === "localhost" || previewHost === "0.0.0.0" || previewHost === "::1" ||
				(isIP(previewHost) === 4 && previewHost.startsWith("127.")) ||
				(isIP(previewHost) === 6 && /^::ffff:7f[0-9a-f]{2}:/i.test(previewHost));
			if (previewHost === previewHostForSession(sessionId)) kind = "static";
			else if (parsed.protocol === "http:" && loopback) kind = "app";
			else if (loopback) throw new Error("unsupported local preview protocol");
			else if (previewHost.endsWith(".localhost")) throw new Error("preview does not belong to this session");
			else return sourceUrl;
			const host = `ao-preview-${randomBytes(16).toString("hex")}.localhost:${port}`;
			previewHosts.set(host, { sessionId, kind, entry: parsed.pathname });
			const url = `http://${host}${parsed.pathname}${parsed.search}${parsed.hash}`;
			previewSessions.set(sessionId, { host, sourceUrl, sourceHref: parsed.href, url });
			return url;
		},
		resolvePreviewUrl: (sessionId, viewedUrl) => {
			let viewed: URL;
			try { viewed = new URL(viewedUrl); } catch { return viewedUrl; }
			if (!/^ao-preview-[0-9a-f]{32}\.localhost$/.test(viewed.hostname)) return viewedUrl;
			const active = previewSessions.get(sessionId);
			if (!active || viewed.origin !== new URL(active.url).origin) return "";
			const source = new URL(active.sourceHref);
			source.pathname = viewed.pathname;
			source.search = viewed.search;
			source.hash = viewed.hash;
			return source.href;
		},
		close: () =>
			new Promise((resolve) => {
				// close() alone waits on keep-alive and tunnelled sockets forever;
				// a deactivated proxy must actually stop serving.
				for (const drop of tunnels) drop();
				server.closeAllConnections();
				server.close(() => {
					log(`stopped on 127.0.0.1:${port} for ${upstream.host}`);
					resolve();
				});
			}),
	};
}

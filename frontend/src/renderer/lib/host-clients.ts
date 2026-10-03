import createClient from "openapi-fetch";
import type { paths } from "../../api/schema";
import { aoBridge } from "./bridge";
import { apiClient } from "./api-client";
import { LOCAL_HOST, type HostId } from "./hosts";

type ConnectedHost = { hostId: HostId; label: string; url: string; base: string };
const hosts = new Map<HostId, ConnectedHost>();
const clients = new Map<HostId, ReturnType<typeof createClient<paths>>>();
const listeners = new Set<() => void>();
let snapshot: HostId[] = [];

function publish(): void {
	snapshot = [...hosts.keys()];
	for (const listener of listeners) listener();
}

export function connectedHosts(): HostId[] {
	return snapshot;
}

export function subscribeConnectedHosts(listener: () => void): () => void {
	listeners.add(listener);
	return () => listeners.delete(listener);
}

export function baseUrlForHost(hostId: HostId): string | undefined {
	return hosts.get(hostId)?.base;
}

export function connectedHost(hostId: HostId): Readonly<ConnectedHost> | undefined {
	return hosts.get(hostId);
}

export function isQuickTunnelHost(hostId: HostId): boolean {
	const url = hosts.get(hostId)?.url;
	return Boolean(url && new URL(url).hostname.endsWith(".trycloudflare.com"));
}

export function labelForHost(hostId: HostId): string | undefined {
	return hosts.get(hostId)?.label;
}

export function clientForHost(hostId: HostId) {
	if (hostId === LOCAL_HOST) throw new Error("local is not a remote host");
	const host = hosts.get(hostId);
	if (!host) throw new Error(`Host ${hostId} is not connected`);
	let client = clients.get(hostId);
	if (!client) {
		client = createClient<paths>({ baseUrl: host.base });
		clients.set(hostId, client);
	}
	return client;
}

/** Resolve the daemon that owns a session at the point a request is made. */
export function clientForSessionHost(hostId?: HostId) {
	return hostId && hostId !== LOCAL_HOST ? clientForHost(hostId) : apiClient;
}

export async function connectHost(url: string, hostId?: HostId, shouldAdopt: () => boolean = () => true): Promise<ConnectedHost> {
	const host = await aoBridge.remotes.connect(url, hostId);
	if (!shouldAdopt()) return host;
	let replaced = false;
	for (const [oldId, oldHost] of hosts) {
		if (oldId !== host.hostId && oldHost.url === host.url) {
			hosts.delete(oldId);
			clients.delete(oldId);
			replaced = true;
		}
	}
	const previous = hosts.get(host.hostId);
	if (previous?.base !== host.base) clients.delete(host.hostId);
	hosts.set(host.hostId, host);
	if (replaced || !previous || previous.base !== host.base || previous.label !== host.label || previous.url !== host.url) publish();
	return host;
}

export async function disconnectHost(hostId: HostId): Promise<void> {
	const host = hosts.get(hostId);
	if (!host) return;
	hosts.delete(hostId);
	clients.delete(hostId);
	publish();
	await aoBridge.remotes.disconnect(host.url);
}

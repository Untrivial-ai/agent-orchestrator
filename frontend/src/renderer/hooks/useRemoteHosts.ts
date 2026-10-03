import { useCallback, useEffect, useRef, useState } from "react";
import { aoBridge } from "../lib/bridge";
import { connectHost, connectedHosts, disconnectHost } from "../lib/host-clients";
import { useUiStore } from "../stores/ui-store";

const HOSTS_CHANGED_EVENT = "ao:remote-hosts-changed";
const OFFLINE_RETRY_MS = 15_000;
const NO_HOSTS: RemoteHost[] = [];

function isOfflineError(error: unknown): boolean {
	return error instanceof Error && error.message.endsWith(" is offline");
}

export function requestRemoteHostsRefresh(): void {
	window.dispatchEvent(new Event(HOSTS_CHANGED_EVENT));
}

export type RemoteHost = {
	hostId: string;
	label: string;
	url: string;
	status: "connecting" | "connected" | "offline";
	failureReason?: "unauthorized" | "incompatible";
};

export function useRemoteHosts(): { hosts: RemoteHost[]; refresh: () => Promise<void> } {
	const enabled = useUiStore((state) => state.developerMode && state.remoteHosts);
	const enabledRef = useRef(enabled);
	enabledRef.current = enabled;
	const [hosts, setHosts] = useState<RemoteHost[]>([]);
	const hostsRef = useRef(hosts);
	hostsRef.current = hosts;
	const retryableHosts = useRef(new Set<string>());
	const retryingHosts = useRef(new Set<string>());
	const savedHostUrls = useRef(new Map<string, string>());
	const refreshGeneration = useRef(0);
	const refresh = useCallback(async () => {
		if (!enabledRef.current) return;
		const generation = ++refreshGeneration.current;
		const current = () => enabledRef.current && refreshGeneration.current === generation;
		const saved = await aoBridge.remotes.list();
		if (!current()) return;
		savedHostUrls.current = new Map(saved.map((host) => [host.hostId, host.url]));
		setHosts((previous) => saved.map((host) => ({
			...host,
			status: previous.some((entry) => entry.hostId === host.hostId && entry.url === host.url && entry.status === "connected") ? "connected" : "connecting",
		})));
		await Promise.all(saved.map(async (savedHost) => {
			let status: RemoteHost["status"] = "connected";
			let failureReason: RemoteHost["failureReason"];
			let connectedHostId = savedHost.hostId;
			try {
				const shouldAdopt = () => current() && savedHostUrls.current.get(savedHost.hostId) === savedHost.url;
				connectedHostId = (await connectHost(savedHost.url, savedHost.hostId, shouldAdopt)).hostId;
				if (!shouldAdopt()) {
					if (savedHostUrls.current.get(savedHost.hostId) !== savedHost.url) await aoBridge.remotes.disconnect(savedHost.url);
					return;
				}
				retryableHosts.current.delete(savedHost.hostId);
			} catch (error) {
				status = "offline";
				if (error instanceof Error && error.message.endsWith(" is unauthorized")) failureReason = "unauthorized";
				if (error instanceof Error && error.message.endsWith(" is incompatible")) failureReason = "incompatible";
				if (!current()) return;
				if (isOfflineError(error)) retryableHosts.current.add(savedHost.hostId);
				else retryableHosts.current.delete(savedHost.hostId);
				// A failed reconnect must not leave the old proxy marked connected.
				try { await disconnectHost(savedHost.hostId); } catch { /* Keep the offline state visible. */ }
			}
			if (!current()) return;
			setHosts((current) => current.map((host) => host.url === savedHost.url && host.hostId === savedHost.hostId ? { ...host, hostId: connectedHostId, status, ...(failureReason ? { failureReason } : {}) } : host));
		}));
	}, []);

	useEffect(() => {
		if (enabled) {
			void refresh();
			return;
		}
		setHosts([]);
		retryableHosts.current.clear();
		savedHostUrls.current.clear();
		for (const hostId of connectedHosts()) void disconnectHost(hostId);
	}, [enabled, refresh]);
	useEffect(() => {
		if (!enabled) return;
		const timer = setInterval(() => {
			for (const host of hostsRef.current) {
				if (host.status !== "offline" || !retryableHosts.current.has(host.hostId) || retryingHosts.current.has(host.hostId)) continue;
				retryingHosts.current.add(host.hostId);
				const generation = refreshGeneration.current;
				void connectHost(host.url, host.hostId, () => enabledRef.current && generation === refreshGeneration.current && savedHostUrls.current.get(host.hostId) === host.url).then(async () => {
					if (!enabledRef.current) {
						await aoBridge.remotes.disconnect(host.url);
						return;
					}
					if (generation !== refreshGeneration.current) {
						if (savedHostUrls.current.get(host.hostId) !== host.url) await aoBridge.remotes.disconnect(host.url);
						return;
					}
					retryableHosts.current.delete(host.hostId);
					setHosts((current) => current.map((entry) => entry.hostId === host.hostId && entry.url === host.url
						? { ...entry, status: "connected" } : entry));
				}).catch((error: unknown) => {
					if (!isOfflineError(error)) retryableHosts.current.delete(host.hostId);
				}).finally(() => retryingHosts.current.delete(host.hostId));
			}
		}, OFFLINE_RETRY_MS);
		return () => clearInterval(timer);
	}, [enabled]);
	useEffect(() => {
		if (!enabled) return;
		const onChanged = () => { void refresh(); };
		window.addEventListener(HOSTS_CHANGED_EVENT, onChanged);
		return () => window.removeEventListener(HOSTS_CHANGED_EVENT, onChanged);
	}, [enabled, refresh]);

	return { hosts: enabled ? hosts : NO_HOSTS, refresh };
}

import { useSyncExternalStore } from "react";
import { connectedHost, connectedHosts, subscribeConnectedHosts } from "../lib/host-clients";
import { LOCAL_HOST, type HostId } from "../lib/hosts";
import { useUiStore } from "../stores/ui-store";

const NO_HOSTS: HostId[] = [];

/** Keep the selected host separate from its optional live connection. */
export function useHostConnection(hostId?: HostId) {
	const connection = useSyncExternalStore(subscribeConnectedHosts, () => hostId ? connectedHost(hostId) : undefined);
	return {
		hostId,
		isRemote: Boolean(hostId && hostId !== LOCAL_HOST),
		baseUrl: connection?.base,
		label: connection?.label,
	};
}

export function useConnectedHosts(): HostId[] {
	const enabled = useUiStore((state) => state.developerMode && state.remoteHosts);
	const hosts = useSyncExternalStore(subscribeConnectedHosts, connectedHosts, connectedHosts);
	return enabled ? hosts : NO_HOSTS;
}

// Host identity. Local is a host like any other — the one whose requests skip
// the proxy — so no code path has to special-case "is this remote?".
export type HostId = string;

export const LOCAL_HOST: HostId = "local";

/** Anything addressable across hosts. A bare id is never enough to act on. */
export type Ref = {
	host: HostId;
	id: string;
};

export function isLocal(host: HostId): boolean {
	return host === LOCAL_HOST;
}

// A host id is a URL and an id may contain anything, so the key encodes both
// halves rather than relying on a separator being absent from either.
export function refKey(ref: Ref): string {
	return `${encodeURIComponent(ref.host)}:${encodeURIComponent(ref.id)}`;
}

export function parseRefKey(key: string): Ref {
	const separator = key.indexOf(":");
	if (separator === -1) throw new Error(`malformed ref key: ${key}`);
	return {
		host: decodeURIComponent(key.slice(0, separator)),
		id: decodeURIComponent(key.slice(separator + 1)),
	};
}

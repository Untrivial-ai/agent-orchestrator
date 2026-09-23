/**
 * Building the pairing code the mobile app scans.
 *
 * Pure, and deliberately not inside the modal: it is the wire contract with the
 * phone, and the phone's parser is tested against the same shape in
 * packages/mobile/lib/pairingCode.ts.
 */

export type PairingEndpointKind = "lan" | "tailscale" | "tunnel" | "relay";

export type PairingEndpoint = {
	/**
	 * Typed as a plain string, not the union above, to match what the daemon's
	 * generated schema produces. The desktop only passes the value through into
	 * the pairing code — the phone is what validates it — so narrowing here
	 * would reject a newer daemon advertising a kind this build predates.
	 */
	kind: string;
	host: string;
	port: number;
	secure: boolean;
};

export type PairingOffer = {
	v: 2;
	hostId: string;
	name: string;
	platform: string;
	endpoints: PairingEndpoint[];
	token: string;
};

export type PairingOfferInput = {
	endpoints: readonly PairingEndpoint[];
	password: string;
	hostId: string;
	name: string;
	platform: string;
};

/**
 * Assembles the offer for a QR or a copyable code.
 *
 * Carries every endpoint the daemon advertises rather than one chosen address:
 * the phone races them, so a machine on both Wi-Fi and Ethernet offers both,
 * and a tunnel entry keeps it reachable from any network.
 *
 * Throws on an empty endpoint list. A code with nothing to connect to is worse
 * than no code — it looks scannable and cannot work — so the caller must show
 * "preparing" instead.
 */
export function buildPairingOffer(input: PairingOfferInput): PairingOffer {
	if (input.endpoints.length === 0) {
		throw new Error("cannot build a pairing offer with no endpoints");
	}
	return {
		v: 2,
		hostId: input.hostId,
		name: input.name,
		platform: input.platform,
		endpoints: [...input.endpoints],
		token: input.password,
	};
}

/** base64url, unpadded: shorter QR, and safe inside a URL fragment. */
function toBase64Url(json: string): string {
	return btoa(json).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/**
 * Wraps an offer in a scannable link.
 *
 * The payload goes in the fragment, never the query. Fragments are not sent to
 * servers, so even when the https form is opened by a browser the connection
 * token stays out of web logs and referrer headers.
 */
export function pairingCodeUrl(offer: PairingOffer, base: string): string {
	return `${base}#${toBase64Url(JSON.stringify(offer))}`;
}

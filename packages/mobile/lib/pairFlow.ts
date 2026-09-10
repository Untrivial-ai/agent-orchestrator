import { configForEndpoint } from "./connect";
import type { ServerConfig } from "./config";
import type { Endpoint } from "./endpoints";
import type { Host } from "./hosts";
import { isLegacyPairingCode, parsePairingCode } from "./pairingCode";
import type { RaceOutcome } from "./race";

export type PairResult =
	| { ok: true; config: ServerConfig; host: Host }
	| {
			ok: false;
			reason: "not-ao-qr" | "outdated-desktop" | "no-candidates" | "none-reachable" | "verify-failed";
		};

export type PairDeps = {
	/** Probes the code's endpoints and returns the best that answers. */
	race: (endpoints: Endpoint[], expectedHostId: string) => Promise<RaceOutcome>;
	/** Proves the winning endpoint actually serves us, with our credential. */
	verify: (config: ServerConfig) => Promise<void>;
	saveHost: (host: Host) => Promise<void>;
	setActiveHost: (id: string) => Promise<void>;
};

/**
 * Turns a scanned or pasted pairing code into a stored machine.
 *
 * Lives here rather than in the pair screen because `packages/mobile` has no
 * screen tests: logic left in a component is unreachable by the suite.
 *
 * Order matters. The endpoints are raced first, so what gets stored is a
 * machine we have actually reached, and verification runs before anything is
 * written — a pairing saved without being proven leaves the app looking
 * connected to something it has never spoken to.
 */
export async function pairFromCode(raw: string, deps: PairDeps): Promise<PairResult> {
	const offer = parsePairingCode(raw);
	// Distinguished from unrecognised input so the reason does not depend on
	// which caller reached here: a v1 code is a real AO code from a desktop too
	// old to pair with, and saying so is more useful than "not a pairing code".
	if (!offer) {
		return { ok: false, reason: isLegacyPairingCode(raw) ? "outdated-desktop" : "not-ao-qr" };
	}

	const outcome = await deps.race(offer.endpoints, offer.hostId);
	if (!outcome.ok) return { ok: false, reason: outcome.reason };

	const config = configForEndpoint(outcome.endpoint, offer.token, offer.hostId || outcome.hostId);
	try {
		await deps.verify(config);
	} catch {
		return { ok: false, reason: "verify-failed" };
	}

	const host: Host = {
		// A v1 code carries no identity, but we have just spoken to the machine
		// and it told us who it is, so adopt that now rather than leaving it
		// unverified until the next connect.
		id: offer.hostId || outcome.hostId,
		name: offer.name || outcome.endpoint.host,
		platform: offer.platform,
		endpoints: offer.endpoints,
		token: offer.token,
		lastConnected: Date.now(),
	};
	await deps.saveHost(host);
	await deps.setActiveHost(host.id);

	return { ok: true, config, host };
}

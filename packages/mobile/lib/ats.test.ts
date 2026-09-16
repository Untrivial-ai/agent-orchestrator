import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// App Transport Security is declared in app.json and only takes effect in a
// native build, so nothing at runtime can catch it drifting. Since iOS 17 ATS
// refuses cleartext to any IP address outside the local ranges, which is where
// a tailnet lives (#3852). The exception has to exist, and it has to stay this
// narrow: NSAllowsArbitraryLoads would also "fix" it, and would waive ATS for
// every host the app ever talks to.
describe("app.json App Transport Security", () => {
	const ats = JSON.parse(readFileSync(join(__dirname, "..", "app.json"), "utf8")).expo.ios.infoPlist
		.NSAppTransportSecurity as Record<string, unknown>;
	const domains = ats.NSExceptionDomains as Record<string, Record<string, unknown>>;

	it("allows cleartext to the tailnet, by address range and by MagicDNS name", () => {
		expect(domains["100.64.0.0/10"]).toEqual({ NSExceptionAllowsInsecureHTTPLoads: true });
		expect(domains["ts.net"]).toEqual({ NSIncludesSubdomains: true, NSExceptionAllowsInsecureHTTPLoads: true });
	});

	it("keeps the local-network exemption and waives nothing else", () => {
		expect(ats.NSAllowsLocalNetworking).toBe(true);
		expect(ats.NSAllowsArbitraryLoads).toBeUndefined();
		expect(ats.NSAllowsArbitraryLoadsInWebContent).toBeUndefined();
		expect(Object.keys(domains).sort()).toEqual(["100.64.0.0/10", "ts.net"]);
	});
});

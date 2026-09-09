import { describe, expect, it, vi } from "vitest";
import { applyMarketingConsent } from "./marketing-consent";

function recordingClient() {
	const calls: string[] = [];
	return {
		calls,
		register: vi.fn((props: Record<string, unknown>) => {
			calls.push(`register:${JSON.stringify(props)}`);
		}),
		opt_in_capturing: vi.fn(() => calls.push("opt_in")),
		opt_out_capturing: vi.fn(() => calls.push("opt_out")),
	};
}

describe("applyMarketingConsent", () => {
	// The regression the reviewer asked for: a returning visitor who already
	// accepted consent. opt_in_capturing fires the opt-in event and initial
	// pageview synchronously, so app_name must be registered strictly before it,
	// or those first events leak untagged into the shared desktop project.
	it("registers the marketing tag before opting a returning consented visitor in", () => {
		const client = recordingClient();
		applyMarketingConsent(client, "accepted", "useao.dev");

		expect(client.calls).toEqual([
			'register:{"app_name":"marketing","domain":"useao.dev"}',
			"opt_in",
		]);
		// Order, not just presence: register must come first.
		expect(client.register.mock.invocationCallOrder[0]).toBeLessThan(
			client.opt_in_capturing.mock.invocationCallOrder[0],
		);
	});

	it("still tags the client when the visitor has not consented, then opts out", () => {
		const client = recordingClient();
		applyMarketingConsent(client, null, "useao.dev");
		expect(client.calls).toEqual([
			'register:{"app_name":"marketing","domain":"useao.dev"}',
			"opt_out",
		]);
		expect(client.opt_in_capturing).not.toHaveBeenCalled();
	});

	it("treats any non-accepted value as opt-out", () => {
		for (const consent of ["", "declined", "pending", "ACCEPTED"]) {
			const client = recordingClient();
			applyMarketingConsent(client, consent, "useao.dev");
			expect(client.opt_out_capturing, `consent=${consent}`).toHaveBeenCalledOnce();
			expect(client.opt_in_capturing).not.toHaveBeenCalled();
		}
	});
});

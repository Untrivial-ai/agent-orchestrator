import { describe, expect, it } from "vitest";
import { resolveAgentRecoveryAction } from "./agent-recovery";

function readiness(installation: string, authentication: string) {
	return {
		installation: { state: installation },
		authentication: { state: authentication },
	};
}

describe("resolveAgentRecoveryAction", () => {
	it("suppresses recovery while readiness is loading", () => {
		expect(
			resolveAgentRecoveryAction({
				readiness: readiness("not_installed", "unauthorized"),
				isLoading: true,
			}),
		).toBeNull();
	});

	it("prioritizes installation over every authentication state", () => {
		for (const authentication of ["unauthorized", "configured", "unknown", "authorized"]) {
			expect(
				resolveAgentRecoveryAction({
					readiness: readiness("not_installed", authentication),
				}),
			).toBe("install");
		}
	});

	it("maps unauthorized authentication to login", () => {
		expect(
			resolveAgentRecoveryAction({
				readiness: readiness("installed", "unauthorized"),
			}),
		).toBe("login");
	});

	it("maps configured authentication to review", () => {
		expect(
			resolveAgentRecoveryAction({
				readiness: readiness("installed", "configured"),
			}),
		).toBe("review");
	});

	it.each([
		["unknown", "unauthorized", "login"],
		["unknown", "configured", "review"],
	])("prioritizes authentication recovery over unknown installation (%s, %s)", (installation, authentication, action) => {
		expect(resolveAgentRecoveryAction({ readiness: readiness(installation, authentication) })).toBe(action);
	});

	it.each([
		["unknown", "authorized"],
		["installed", "unknown"],
		["unexpected-installation", "authorized"],
		["installed", "unexpected-authentication"],
	])("maps uncertain readiness (%s, %s) to configure", (installation, authentication) => {
		expect(
			resolveAgentRecoveryAction({
				readiness: readiness(installation, authentication),
			}),
		).toBe("configure");
	});

	it.each(["authorized", "not_applicable"])(
		"returns no action for an installed agent with %s authentication",
		(authentication) => {
			expect(
				resolveAgentRecoveryAction({
					readiness: readiness("installed", authentication),
				}),
			).toBeNull();
		},
	);

	it("offers configuration when readiness completed without an agent observation", () => {
		expect(resolveAgentRecoveryAction({ readiness: undefined })).toBe("configure");
	});
});

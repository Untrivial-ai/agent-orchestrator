import { describe, expect, it } from "vitest";
import {
	AGENT_CAPABILITIES,
	AGENT_AUTHENTICATION_STATES,
	AGENT_INSTALLATION_STATES,
	hasAgentCapability,
	type AgentProfile,
} from "./agent-capabilities";

describe("agent capabilities", () => {
	it("keeps the public vocabulary stable", () => {
		expect(AGENT_CAPABILITIES).toEqual([
			"interface.chat",
			"interface.tui",
			"model.catalog",
			"model.custom",
			"attachments",
			"browser.preview",
			"review.execute",
			"session.resume",
		]);
	});

	it("checks host-supplied profiles without inferring agent behavior", () => {
		const profile: AgentProfile = {
			id: "runtime-agent",
			label: "Runtime Agent",
			capabilities: ["interface.chat", "session.resume"],
			availability: {
				available: false,
				installation: "installed",
				authentication: "unauthorized",
				organizationPolicy: "denied",
			},
		};

		expect(hasAgentCapability(profile, "session.resume")).toBe(true);
		expect(hasAgentCapability(profile, "attachments")).toBe(false);
		expect(profile.availability).toMatchObject({
			installation: "installed",
			authentication: "unauthorized",
			organizationPolicy: "denied",
		});
	});

	it("normalizes installation and authentication states", () => {
		expect(AGENT_INSTALLATION_STATES).toEqual([
			"installed",
			"not_installed",
			"unknown",
			"not_applicable",
		]);
		expect(AGENT_AUTHENTICATION_STATES).toEqual([
			"authorized",
			"unauthorized",
			"unknown",
			"not_applicable",
		]);
	});
});

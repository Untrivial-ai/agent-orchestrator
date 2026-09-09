import { describe, expect, it } from "vitest";
import { findSessionLinks, parseSessionLink, resolveSessionLink } from "./session-links";

describe("session links", () => {
	it.each([
		["ao://sessions/project/session", { projectId: "project", sessionId: "session" }],
		["ao://sessions/project%20one/session%2Done", { projectId: "project one", sessionId: "session-one" }],
	])("parses %s", (url, expected) => expect(parseSessionLink(url)).toEqual(expected));

	it.each([
		"AO://sessions/project/session", "a0://sessions/project/session", "ao://session/project/session",
		"ao://sessions/project", "ao://sessions/project/session/extra", "ao://sessions//session",
		"ao://sessions/project/session?kill=true", "ao://sessions/project/session#chat",
		"ao://user@sessions/project/session", "ao://sessions/project/%ZZ", "ao://sessions/project/%2Faction",
		"ao://sessions/project/session/kill",
	])("rejects unsupported value %s", (url) => expect(parseSessionLink(url)).toBeUndefined());

	it("trims adjacent punctuation while preserving encoded IDs", () => {
		expect(findSessionLinks("See (ao://sessions/p/s), then ao://sessions/p/t.")).toEqual([
			{ start: 5, end: 22, text: "ao://sessions/p/s" },
			{ start: 30, end: 47, text: "ao://sessions/p/t" },
		]);
	});

	it("resolves by stable project and session IDs with termination state", () => {
		const workspaces = [{ id: "p", sessions: [{ id: "s", title: "renamed", status: "terminated" }] }];
		expect(resolveSessionLink("ao://sessions/p/s", workspaces)).toEqual({ projectId: "p", sessionId: "s", isTerminated: true });
		expect(resolveSessionLink("ao://sessions/p/missing", workspaces)).toBeUndefined();
	});
});

import { describe, expect, it } from "vitest";
import { forgetLastSession, lastSessionTarget, readLastSession, rememberLastSession } from "./last-session";
import { STANDALONE_WORKSPACE_ID } from "../types/workspace";

function memoryStorage() {
	const values = new Map<string, string>();
	return {
		getItem: (key: string) => values.get(key) ?? null,
		setItem: (key: string, value: string) => {
			values.set(key, value);
		},
		removeItem: (key: string) => {
			values.delete(key);
		},
	};
}

describe("last session memory", () => {
	it("round-trips the session the user was looking at", () => {
		const storage = memoryStorage();
		rememberLastSession(
			{
				sessionId: "mer-1",
				projectId: "mer",
				incarnation: "2026-09-22T00:00:00Z",
				title: "Fix the board",
			},
			storage,
		);

		expect(readLastSession(storage)).toEqual({
			sessionId: "mer-1",
			projectId: "mer",
			incarnation: "2026-09-22T00:00:00Z",
			title: "Fix the board",
		});

		forgetLastSession(storage);
		expect(readLastSession(storage)).toBeNull();
	});

	it("ignores a record that cannot claim a draft incarnation", () => {
		const storage = memoryStorage();
		storage.setItem("ao.lastSession", JSON.stringify({ sessionId: "mer-1", title: "Missing incarnation" }));
		expect(readLastSession(storage)).toBeNull();
		storage.setItem("ao.lastSession", "{");
		expect(readLastSession(storage)).toBeNull();
	});

	it("opens a project session in its project and a standalone session on its own", () => {
		expect(
			lastSessionTarget({
				sessionId: "mer-1",
				projectId: "mer",
				incarnation: "2026-09-22T00:00:00Z",
				title: "Fix the board",
			}),
		).toEqual({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "mer", sessionId: "mer-1" },
		});
		expect(
			lastSessionTarget({
				sessionId: "solo-1",
				projectId: STANDALONE_WORKSPACE_ID,
				incarnation: "2026-09-22T00:00:00Z",
				title: "Scratch",
			}).to,
		).toBe("/sessions/$sessionId");
	});
});

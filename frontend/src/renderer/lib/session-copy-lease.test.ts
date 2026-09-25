import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { releaseColdSessionCopies, retainedSessionCopies } from "./session-copy-lease";

describe("retainedSessionCopies", () => {
	it("keeps the open session and the one just left", () => {
		expect(retainedSessionCopies("c", ["b", "a"])).toEqual(["c", "b"]);
	});

	it("keeps only the last session when nothing is open", () => {
		expect(retainedSessionCopies(undefined, ["c", "b"])).toEqual(["c"]);
	});
});

describe("releaseColdSessionCopies", () => {
	it("drops a cold conversation and file copy without touching the warm ones or the workspace list", () => {
		const client = new QueryClient();
		client.setQueryData(["conversation", "a"], { pages: [{ text: "old chat" }] });
		client.setQueryData(["session-workspace-diffs", "a", "head"], { files: ["big.diff"] });
		client.setQueryData(["conversation", "b"], { pages: [{ text: "warm chat" }] });
		client.setQueryData(["conversation", "c"], { pages: [{ text: "open chat" }] });
		client.setQueryData(["workspaces"], [{ id: "project" }]);
		client.setQueryData(["conversation-dispatch-tracking"], { a: { state: "pending" } });
		client.setQueryData(["conversation-local-echos"], { a: [{ text: "still sending" }] });

		const retained = releaseColdSessionCopies(client, "c", ["b", "a"]);

		expect(retained).toEqual(["c", "b"]);
		expect(client.getQueryData(["conversation", "a"])).toBeUndefined();
		expect(client.getQueryData(["session-workspace-diffs", "a", "head"])).toBeUndefined();
		expect(client.getQueryData(["conversation", "b"])).toEqual({ pages: [{ text: "warm chat" }] });
		expect(client.getQueryData(["conversation", "c"])).toEqual({ pages: [{ text: "open chat" }] });
		expect(client.getQueryData(["workspaces"])).toEqual([{ id: "project" }]);
		expect(client.getQueryData(["conversation-dispatch-tracking"])).toEqual({ a: { state: "pending" } });
		expect(client.getQueryData(["conversation-local-echos"])).toEqual({ a: [{ text: "still sending" }] });
	});
});

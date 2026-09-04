import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ImportableSession } from "../hooks/useImportableSessions";
import { useImportRunStore } from "./import-run-store";

const h = vi.hoisted(() => ({ post: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: vi.fn(), POST: h.post },
	apiErrorMessage: (_e: unknown, fallback: string) => fallback,
}));

function session(id: string, alreadyImported = false, cwd = "/repo"): ImportableSession {
	return {
		provider: "claude-code",
		nativeSessionId: id,
		title: id,
		cwd,
		lastActivity: new Date().toISOString(),
		messageCount: 5,
		sizeBytes: 100,
		alreadyImported,
	};
}

beforeEach(() => {
	h.post.mockReset();
	h.post.mockResolvedValue({ data: { session: { id: "s" } }, error: undefined });
	useImportRunStore.setState({ progress: null, running: false });
});

describe("import run store", () => {
	// The point of the feature: one action, not one click per conversation.
	it("imports every pending conversation in one run", async () => {
		await useImportRunStore.getState().start([session("a"), session("b"), session("c")]);

		expect(h.post).toHaveBeenCalledTimes(3);
		expect(useImportRunStore.getState().progress?.imported).toBe(3);
		expect(useImportRunStore.getState().running).toBe(false);
	});

	it("skips conversations that are already imported", async () => {
		await useImportRunStore.getState().start([session("a", true), session("b"), session("c", true)]);
		expect(h.post).toHaveBeenCalledTimes(1);
	});

	// One unreadable transcript must not strand the other ninety-nine.
	it("counts a failure and keeps going", async () => {
		h.post
			.mockResolvedValueOnce({ data: undefined, error: { message: "boom" } })
			.mockResolvedValue({ data: { session: { id: "s" } }, error: undefined });

		await useImportRunStore.getState().start([session("a"), session("b"), session("c")]);

		expect(h.post).toHaveBeenCalledTimes(3);
		expect(useImportRunStore.getState().progress).toMatchObject({ imported: 2, failed: 1 });
	});

	// Two imports racing inside one repository would contend for git's
	// repository-wide lock, so a folder is imported in order even though
	// separate folders proceed at the same time.
	it("keeps one folder in order while running folders concurrently", async () => {
		const started: string[] = [];
		const finished: string[] = [];
		h.post.mockImplementation(async (_path: string, init: { body: { nativeSessionId: string } }) => {
			started.push(init.body.nativeSessionId);
			await new Promise((r) => setTimeout(r, 5));
			finished.push(init.body.nativeSessionId);
			return { data: { session: { id: "s" } }, error: undefined };
		});

		await useImportRunStore.getState().start([
			session("a1", false, "/repo-a"),
			session("a2", false, "/repo-a"),
			session("b1", false, "/repo-b"),
		]);

		expect(finished.indexOf("a1")).toBeLessThan(started.indexOf("a2"));
		expect(started.indexOf("b1")).toBeLessThan(finished.indexOf("a2"));
	});

	// Stopping must actually end the run, not leave a spinner turning forever.
	it("stops on request and still reports what it managed", async () => {
		h.post.mockImplementation(async () => {
			await new Promise((r) => setTimeout(r, 5));
			return { data: { session: { id: "s" } }, error: undefined };
		});

		const batch = Array.from({ length: 30 }, (_, i) => session(`s${i}`, false, `/repo-${i}`));
		const run = useImportRunStore.getState().start(batch);
		await new Promise((r) => setTimeout(r, 8));
		useImportRunStore.getState().stop();
		await run;

		expect(useImportRunStore.getState().running).toBe(false);
		expect(useImportRunStore.getState().progress!.done).toBeLessThan(30);
	});

	// The run outlives the dialog that started it, so a second start must not
	// launch a parallel run over the same conversations.
	it("refuses to start a second run while one is going", async () => {
		h.post.mockImplementation(async () => {
			await new Promise((r) => setTimeout(r, 5));
			return { data: { session: { id: "s" } }, error: undefined };
		});

		const batch = Array.from({ length: 10 }, (_, i) => session(`s${i}`, false, `/repo-${i}`));
		const first = useImportRunStore.getState().start(batch);
		await useImportRunStore.getState().start(batch);
		await first;

		expect(h.post).toHaveBeenCalledTimes(10);
	});
});

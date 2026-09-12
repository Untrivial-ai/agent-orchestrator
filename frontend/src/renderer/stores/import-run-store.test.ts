import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ImportableSession } from "../hooks/useImportableSessions";
import {
	IMPORT_REQUEST_TIMEOUT_MS,
	useImportRunStore,
} from "./import-run-store";
const h = vi.hoisted(() => ({
	post: vi.fn(),
	invalidate: vi.fn(),
	update: vi.fn(),
}));
vi.mock("../lib/api-client", () => ({
	apiClient: { POST: h.post },
	apiErrorCode: (e: { code?: string }) => e?.code,
	apiErrorMessage: (e: { message?: string }, fallback: string) =>
		e?.message ?? fallback,
}));
vi.mock("../lib/query-client", () => ({
	queryClient: { invalidateQueries: h.invalidate, setQueryData: h.update },
}));
function session(id: string, alreadyImported = false): ImportableSession {
	return {
		provider: "claude-code",
		nativeSessionId: id,
		title: id,
		cwd: "/repo",
		lastActivity: new Date().toISOString(),
		messageCount: 0,
		tokenCount: 15_000,
		sizeBytes: 100,
		alreadyImported,
	};
}
function deferred() {
	let resolve!: (value: unknown) => void;
	const promise = new Promise((r) => {
		resolve = r;
	});
	return { promise, resolve };
}
beforeEach(() => {
	vi.clearAllMocks();
	h.post.mockReset().mockImplementation((_path, options) => Promise.resolve({ data: { session: { id: "s" }, results: options.body.sessions ?? [] } }));
	useImportRunStore.setState({ runs: {} });
});
afterEach(() => vi.useRealTimers());
describe("project import run", () => {
 it("updates the list once for a large batch and preserves failed items", async () => {
  const sessions = Array.from({length: 1000}, (_,i) => session(String(i)));
  h.post.mockResolvedValueOnce({data:{results:sessions.map((s,i) => ({...s,error:i===17 ? "missing" : undefined}))}});
  await useImportRunStore.getState().start("a",sessions);
  expect(h.update).toHaveBeenCalledTimes(1);
  const updated = h.update.mock.calls[0][1](sessions);
  expect(updated.filter((s: ImportableSession) => s.alreadyImported)).toHaveLength(999);
  expect(updated[17].alreadyImported).toBe(false);
  expect(useImportRunStore.getState().runs.a.progress).toEqual({done:1000,total:1000,imported:999,failed:1});
 });

	it("imports pending unique conversations in one request with explicit project identity", async () => {
		const first = deferred();
		h.post.mockReturnValueOnce(first.promise);
		const run = useImportRunStore
			.getState()
			.start("a", [
				session("one"),
				session("one"),
				session("done", true),
				session("two"),
			]);
		expect(h.post).toHaveBeenCalledTimes(1);
		expect(useImportRunStore.getState().runs.a.running).toBe(true);
		expect(h.post.mock.calls[0][1].body).toEqual({
			projectId: "a",
			sessions: [{provider: "claude-code", nativeSessionId: "one"}, {provider: "claude-code", nativeSessionId: "two"}],
		});
		first.resolve({ data: {results: [{provider: "claude-code", nativeSessionId: "one"}, {provider: "claude-code", nativeSessionId: "two"}]} });
		await run;
		expect(h.post).toHaveBeenCalledTimes(1);
		expect(useImportRunStore.getState().runs.a.progress).toEqual({
			done: 2,
			total: 2,
			imported: 2,
			failed: 0,
		});
		expect(
			h.invalidate.mock.calls.filter(
				([args]) => args.queryKey[0] === "importable-sessions",
			),
		).toHaveLength(1);
	});
	it("prevents single/bulk overlap in one project while retaining independent project results", async () => {
		const first = deferred();
		h.post.mockReturnValueOnce(first.promise);
		const run = useImportRunStore.getState().start("a", [session("one")]);
		await useImportRunStore
			.getState()
			.start("a", [session("one"), session("two")]);
		await useImportRunStore.getState().start("b", [session("other")]);
		expect(h.post).toHaveBeenCalledTimes(2);
		expect(useImportRunStore.getState().runs.a.running).toBe(true);
		expect(useImportRunStore.getState().runs.b.progress.imported).toBe(1);
		first.resolve({ data: {} });
		await run;
	});
	it("shows a session-specific daemon failure and continues the remaining queue", async () => {
		h.post.mockResolvedValueOnce({
			data: {results: [{provider: "claude-code", nativeSessionId: "one", error: "Conversation no longer exists"}, {provider: "claude-code", nativeSessionId: "two"}]},
		});
		await useImportRunStore
			.getState()
			.start("a", [session("one"), session("two")]);
		expect(useImportRunStore.getState().runs.a.errors).toEqual([
			{ title: "one", message: "Conversation no longer exists" },
		]);
		expect(useImportRunStore.getState().runs.a.progress).toMatchObject({
			imported: 1,
			failed: 1,
		});
	});
	it.each([
		"CODEX_ACCOUNT_AUTH_UNVERIFIED",
		"CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE",
		"AGENT_BINARY_NOT_FOUND",
	])("stops on shared setup failure %s and permits retry after recovery", async (code) => {
		vi.useFakeTimers();
		const sessions = Array.from({ length: 180 }, (_, i) => ({
			...session(String(i)), provider: "codex" as const,
		}));
		h.post.mockResolvedValueOnce({ error: { code, message: "Complete agent setup in Settings" } });
		await useImportRunStore.getState().start("a", sessions);
		expect(h.post).toHaveBeenCalledTimes(1);
		expect(useImportRunStore.getState().runs.a).toMatchObject({
			running: false,
			stopped: true,
			progress: { done: 1, total: 180, imported: 0, failed: 1 },
			errors: [{ title: "0", message: "Complete agent setup in Settings" }],
		});
		expect(vi.getTimerCount()).toBe(0);
		await useImportRunStore.getState().start("a", sessions);
		expect(useImportRunStore.getState().runs.a).toMatchObject({
			running: false,
			stopped: false,
			progress: { done: 180, total: 180, imported: 180, failed: 0 },
			errors: [],
		});
	});
	it("stops a never-settling request and ignores its late completion after another run", async () => {
		const first = deferred();
		h.post.mockReturnValueOnce(first.promise);
		const run = useImportRunStore
			.getState()
			.start("a", [session("one"), session("two")]);
		useImportRunStore.getState().stop("a");
		await run;
		expect(h.post.mock.calls[0][1].signal.aborted).toBe(true);
		expect(useImportRunStore.getState().runs.a).toMatchObject({
			running: false,
			stopped: true,
		});
		await useImportRunStore.getState().start("a", [session("new")]);
		first.resolve({ data: {} });
		await Promise.resolve();
		expect(useImportRunStore.getState().runs.a.progress).toMatchObject({
			total: 1,
			imported: 1,
		});
		expect(h.post).toHaveBeenCalledTimes(2);
	});
	it("bounds hung requests without starting the next import", async () => {
		vi.useFakeTimers();
		h.post.mockReturnValue(new Promise(() => {}));
		const run = useImportRunStore
			.getState()
			.start("a", [session("one"), session("two")]);
		expect(useImportRunStore.getState().runs.a.running).toBe(true);
		await vi.advanceTimersByTimeAsync(IMPORT_REQUEST_TIMEOUT_MS);
		await run;
		expect(useImportRunStore.getState().runs.a.running).toBe(false);
		expect(useImportRunStore.getState().runs.a.errors[0].message).toContain(
			"timed out",
		);
		expect(h.post).toHaveBeenCalledTimes(1);
		expect(vi.getTimerCount()).toBe(0);
	});
});

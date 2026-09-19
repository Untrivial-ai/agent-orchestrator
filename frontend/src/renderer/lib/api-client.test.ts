import { afterEach, describe, expect, it, vi } from "vitest";
import {
	apiClient,
	apiErrorDetails,
	apiErrorMessage,
	getApiBaseUrl,
	hasTrustedApiBaseUrl,
	setApiDaemonStatus,
	setApiBaseUrl,
	subscribeApiBaseUrl,
} from "./api-client";

describe("apiClient runtime base URL", () => {
	afterEach(() => {
		vi.restoreAllMocks();
		setApiBaseUrl("http://127.0.0.1:3001");
		setApiDaemonStatus({ state: "stopped" });
	});

	it("rewrites requests to the current runtime daemon port", async () => {
		const seenUrls: string[] = [];
		vi.spyOn(globalThis, "fetch").mockImplementation(async (input: RequestInfo | URL) => {
			seenUrls.push(input instanceof Request ? input.url : input.toString());
			return new Response(JSON.stringify({ projects: [] }), {
				status: 200,
				headers: { "Content-Type": "application/json" },
			});
		});

		setApiBaseUrl("http://127.0.0.1:3037/");

		const { error } = await apiClient.GET("/api/v1/projects");

		expect(error).toBeUndefined();
		expect(getApiBaseUrl()).toBe("http://127.0.0.1:3037");
		expect(seenUrls).toEqual(["http://127.0.0.1:3037/api/v1/projects"]);
	});

	it("rebases POSTs without Request-as-init, preserving method, body, and headers", async () => {
		// Regression: `new Request(target, input)` needs the source request's
		// `duplex` getter, which Electron's Chromium lacks — every request with a
		// body threw. The rewrite must copy fields explicitly instead.
		const seen: { url: string; method?: string; body?: string; contentType?: string | null }[] = [];
		vi.spyOn(globalThis, "fetch").mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
			const headers = new Headers(init?.headers);
			seen.push({
				url: input instanceof Request ? input.url : input.toString(),
				method: init?.method,
				body: init?.body instanceof ArrayBuffer ? new TextDecoder().decode(init.body) : undefined,
				contentType: headers.get("content-type"),
			});
			return new Response(JSON.stringify({ session: { id: "s1" } }), {
				status: 201,
				headers: { "Content-Type": "application/json" },
			});
		});

		setApiBaseUrl("http://127.0.0.1:3037");

		const { error } = await apiClient.POST("/api/v1/sessions", {
			body: { projectId: "p1", prompt: "hello" },
		});

		expect(error).toBeUndefined();
		expect(seen).toHaveLength(1);
		expect(seen[0].url).toBe("http://127.0.0.1:3037/api/v1/sessions");
		expect(seen[0].method).toBe("POST");
		expect(seen[0].contentType).toBe("application/json");
		expect(JSON.parse(seen[0].body ?? "{}")).toEqual({ projectId: "p1", prompt: "hello" });
	});

	it("skips the rebase when the request already targets the runtime base URL", async () => {
		const seen: (RequestInfo | URL)[] = [];
		vi.spyOn(globalThis, "fetch").mockImplementation(async (input: RequestInfo | URL) => {
			seen.push(input);
			return new Response(JSON.stringify({ projects: [] }), {
				status: 200,
				headers: { "Content-Type": "application/json" },
			});
		});

		// Match the base openapi-fetch built the request against (the dev origin
		// in jsdom), so the rewrite has nothing to do.
		setApiBaseUrl(window.location.origin);
		const { error } = await apiClient.GET("/api/v1/projects");

		expect(error).toBeUndefined();
		expect(seen).toHaveLength(1);
		// Untouched pass-through: fetch receives the original Request object.
		expect(seen[0]).toBeInstanceOf(Request);
	});

	it("passes the request through untouched when the base URL is empty", async () => {
		const seen: Request[] = [];
		vi.spyOn(globalThis, "fetch").mockImplementation(async (input: RequestInfo | URL) => {
			seen.push(input as Request);
			return new Response(JSON.stringify({ projects: [] }), {
				status: 200,
				headers: { "Content-Type": "application/json" },
			});
		});

		setApiBaseUrl("");

		const { error } = await apiClient.GET("/api/v1/projects");

		expect(error).toBeUndefined();
		expect(getApiBaseUrl()).toBe("");
		// Empty base → no rewrite; openapi-fetch's own request reaches fetch as-is.
		expect(seen).toHaveLength(1);
		expect(seen[0].url).toContain("/api/v1/projects");
	});

	it("returns unavailable without fetching when the daemon base URL is untrusted", async () => {
		const fetchSpy = vi.spyOn(globalThis, "fetch");

		setApiBaseUrl(null);

		const { error } = await apiClient.GET("/api/v1/projects");

		expect(error).toEqual({ message: "AO daemon is not ready." });
		expect(getApiBaseUrl()).toBe("");
		expect(hasTrustedApiBaseUrl()).toBe(false);
		expect(fetchSpy).not.toHaveBeenCalled();
	});

	it("returns the current daemon startup failure when the base URL is untrusted", async () => {
		setApiBaseUrl(null);
		setApiDaemonStatus({
			state: "error",
			code: "exited",
			message: "AO daemon exited with code 1",
		});

		const { error } = await apiClient.GET("/api/v1/projects");

		expect(error).toEqual({ code: "exited", message: "AO daemon exited with code 1" });
	});
});

describe("subscribeApiBaseUrl", () => {
	afterEach(() => {
		setApiBaseUrl("http://127.0.0.1:3001");
	});

	it("notifies subscribers when the base URL actually changes", () => {
		const listener = vi.fn();
		const unsubscribe = subscribeApiBaseUrl(listener);
		try {
			setApiBaseUrl("http://127.0.0.1:4555");
			expect(listener).toHaveBeenCalledTimes(1);
			expect(getApiBaseUrl()).toBe("http://127.0.0.1:4555");
		} finally {
			unsubscribe();
		}
	});

	it("does not notify for a no-op set (same URL, trailing slash included)", () => {
		setApiBaseUrl("http://127.0.0.1:4555");
		const listener = vi.fn();
		const unsubscribe = subscribeApiBaseUrl(listener);
		try {
			setApiBaseUrl("http://127.0.0.1:4555");
			setApiBaseUrl("http://127.0.0.1:4555/");
			expect(listener).not.toHaveBeenCalled();
		} finally {
			unsubscribe();
		}
	});

	it("stops notifying after unsubscribe", () => {
		const listener = vi.fn();
		subscribeApiBaseUrl(listener)();

		setApiBaseUrl("http://127.0.0.1:4555");

		expect(listener).not.toHaveBeenCalled();
	});
});

describe("apiErrorMessage", () => {
	it("shows the human daemon message without technical error codes", () => {
		expect(apiErrorMessage({ code: "AGENT_BINARY_NOT_FOUND", message: "agent binary not found on PATH" })).toBe(
			"agent binary not found on PATH",
		);
	});

	it("preserves the message when it contains parenthetical detail", () => {
		expect(
			apiErrorMessage({
				code: "RUNTIME_PREREQUISITE_MISSING",
				message: "tmux required (RUNTIME_PREREQUISITE_MISSING)",
			}),
		).toBe("tmux required (RUNTIME_PREREQUISITE_MISSING)");
	});

	it("reads the nested daemon error envelope", () => {
		expect(
			apiErrorMessage({
				error: { code: "REVIEWER_NOT_FOUND", message: "reviewer has not reviewed this PR" },
			}),
		).toBe("reviewer has not reviewed this PR");
	});
});

describe("apiErrorDetails", () => {
	it("preserves structured daemon recovery metadata", () => {
		const details = { existingProjectId: "registered-project", suggestedProjectId: "another-project" };
		expect(apiErrorDetails({ code: "PATH_ALREADY_REGISTERED", details })).toEqual(details);
	});

	it.each([undefined, null, "error", {}, { details: null }, { details: "project" }, { details: ["project"] }])(
		"ignores malformed metadata: %j", (error) => {
			expect(apiErrorDetails(error)).toBeUndefined();
		},
	);
});
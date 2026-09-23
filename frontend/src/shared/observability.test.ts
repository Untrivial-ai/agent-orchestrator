import { describe, expect, it } from "vitest";

import { classifyError } from "./observability";

describe("classifyError — severity + owner", () => {
	it("crashes are P0/ours and reported", () => {
		for (const category of ["native_crash", "render_crash"]) {
			const t = classifyError({ category });
			expect(t.severity).toBe("P0");
			expect(t.owner).toBe("ours");
			expect(t.level).toBe("fatal");
			expect(t.report).toBe(true);
		}
	});

	it("server 5xx and agent-runtime failures are P1/ours", () => {
		expect(classifyError({ category: "http_5xx" })).toMatchObject({ severity: "P1", owner: "ours", report: true });
		expect(classifyError({ category: "agent_runtime" })).toMatchObject({ severity: "P1", owner: "ours" });
	});

	it("daemon unavailable is P1 but environment (usually not running)", () => {
		expect(classifyError({ category: "daemon_unavailable" })).toMatchObject({ severity: "P1", owner: "environment" });
	});

	it("transient connectivity is a breadcrumb, not an issue", () => {
		for (const category of ["offline", "network_error", "timeout"]) {
			const t = classifyError({ category });
			expect(t.transient).toBe(true);
			expect(t.report).toBe(false);
		}
	});

	it("daemon 503 backpressure is a breadcrumb, not a paged issue", () => {
		// The typed SERVICE_UNAVAILABLE code, with or without the http_5xx category.
		for (const meta of [
			{ code: "SERVICE_UNAVAILABLE", httpStatus: 503 },
			{ category: "http_5xx", code: "SERVICE_UNAVAILABLE", httpStatus: 503 },
			{ category: "http_5xx", httpStatus: 503 },
		]) {
			const t = classifyError(meta);
			expect(t.transient).toBe(true);
			expect(t.report).toBe(false);
			expect(t.severity).toBe("P2");
		}
	});

	it("a genuinely unreachable daemon (503 synthetic) still pages as P1", () => {
		// Distinct from backpressure: the client couldn't reach the daemon at all.
		const t = classifyError({ category: "daemon_unavailable", httpStatus: 503 });
		expect(t.severity).toBe("P1");
		expect(t.owner).toBe("environment");
		expect(t.report).toBe(true);
	});

	it("validation / 4xx / not_found ride as P3 breadcrumbs", () => {
		expect(classifyError({ category: "validation" })).toMatchObject({ severity: "P3", owner: "user", report: false });
		expect(classifyError({ category: "http_4xx" })).toMatchObject({ severity: "P3", report: false });
		expect(classifyError({ category: "not_found" })).toMatchObject({ severity: "P3", report: false });
	});
});

describe("classifyError — code overrides", () => {
	it("agent binary / prereq missing is P1 but the user's setup", () => {
		expect(classifyError({ category: "agent_runtime", code: "AGENT_BINARY_NOT_FOUND" })).toMatchObject({
			severity: "P1",
			owner: "user",
		});
		expect(classifyError({ category: "validation", code: "RUNTIME_PREREQUISITE_MISSING" })).toMatchObject({
			severity: "P1",
			owner: "user",
		});
	});

	it("worktree lock/contention is P1/ours (the multi-session case)", () => {
		expect(classifyError({ category: "conflict", code: "WORKSPACE_LOCKED" })).toMatchObject({
			severity: "P1",
			owner: "ours",
			report: true,
		});
	});

	it("wrong pairing password is the user, not external", () => {
		expect(classifyError({ category: "auth", code: "BAD_PASSWORD" })).toMatchObject({ owner: "user" });
		expect(classifyError({ category: "auth" })).toMatchObject({ owner: "external" }); // default without code
	});

	it("SCM/PR operation failures are external", () => {
		expect(classifyError({ category: "http_5xx", code: "SCM_UNAVAILABLE" })).toMatchObject({ owner: "external", severity: "P1" });
		expect(classifyError({ category: "http_5xx", code: "PR_OPERATION_FAILED" })).toMatchObject({ owner: "external" });
	});

	it("not-a-git-repo is the user's setup", () => {
		expect(classifyError({ category: "validation", code: "NOT_A_GIT_REPO" })).toMatchObject({ owner: "user", severity: "P2" });
	});
});

describe("classifyError — escalation rules & fallbacks", () => {
	it("an unhandled error is never below P1", () => {
		expect(classifyError({ category: "validation", unhandled: true }).severity).toBe("P1");
		expect(classifyError({ unhandled: true }).severity).toBe("P1");
	});

	it("a 5xx status floors severity at P1", () => {
		expect(classifyError({ category: "integration", httpStatus: 500 }).severity).toBe("P1");
	});

	it("falls back to apierr kind, then to a safe P2/ours default", () => {
		expect(classifyError({ kind: "invalid" })).toMatchObject({ severity: "P3", owner: "user" });
		expect(classifyError({ kind: "internal" })).toMatchObject({ severity: "P1", owner: "ours" });
		expect(classifyError({})).toMatchObject({ severity: "P2", owner: "ours", report: true }); // nothing known -> visible, ours
	});

	it("code override beats category", () => {
		// category says P3/user, code says P1/ours
		expect(classifyError({ category: "validation", code: "INTERNAL_ERROR" })).toMatchObject({
			severity: "P1",
			owner: "ours",
		});
	});
});

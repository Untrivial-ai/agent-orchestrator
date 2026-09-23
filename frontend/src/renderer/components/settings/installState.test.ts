import { describe, expect, it } from "vitest";
import { installView, type InstallJob } from "./installState";

const job = (over: Partial<InstallJob>): InstallJob => ({ target: "cloudflared", status: "idle", ...over }) as InstallJob;

describe("what to show while installing the connector", () => {
	it("offers the install before anything has run", () => {
		expect(installView(undefined, false)).toEqual({ kind: "offer" });
	});

	// The POST is in flight but no job has come back yet; without this the
	// button would look untouched and invite a second click.
	it("shows progress from the moment the request is sent", () => {
		expect(installView(undefined, true)).toEqual({ kind: "running" });
	});

	it("shows progress while the command runs", () => {
		expect(installView(job({ status: "running" }), false).kind).toBe("running");
	});

	it("reports success", () => {
		expect(installView(job({ status: "succeeded" }), false)).toEqual({ kind: "done" });
	});

	// Linux is unsupported *with* a command, because AO never asks for an
	// administrator password. That is instructions, not a failure — showing it
	// as an error would tell the user nothing they can act on.
	it("hands over the command when AO cannot run it itself", () => {
		const got = installView(
			job({ status: "unsupported", command: "sudo apt-get install -y cloudflared", error: "needs root" }),
			false,
		);
		expect(got).toEqual({
			kind: "manual",
			command: "sudo apt-get install -y cloudflared",
			reason: "needs root",
		});
	});

	// Unsupported with nothing to run — no package manager at all — is a dead
	// end, and saying "run this" with no command would be worse than useless.
	it("reports a dead end when there is no command to offer", () => {
		expect(installView(job({ status: "unsupported", error: "no package manager found" }), false)).toEqual({
			kind: "failed",
			reason: "no package manager found",
		});
	});

	it("reports a failure with its reason", () => {
		expect(installView(job({ status: "failed", error: "brew exited 1" }), false)).toEqual({
			kind: "failed",
			reason: "brew exited 1",
		});
	});
});

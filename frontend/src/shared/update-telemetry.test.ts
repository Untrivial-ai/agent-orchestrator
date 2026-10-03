import { expect, test } from "vitest";
import {
	looksLikeTechnicalUpdateDump,
	isNetErrorMessage,
	updateFailureCategory,
	updateFailureOutcome,
} from "./update-telemetry";

test("detects Chromium network-stack errors", () => {
	expect(isNetErrorMessage("net::ERR_FAILED")).toBe(true);
	expect(isNetErrorMessage("net::ERR_CONNECTION_RESET")).toBe(true);
	// Anchored at the start — a net:: substring elsewhere is not the wedge signature.
	expect(isNetErrorMessage("Error: net::ERR_FAILED")).toBe(false);
	expect(isNetErrorMessage("HttpError: 404")).toBe(false);
	expect(isNetErrorMessage(undefined)).toBe(false);
});

test("buckets updater errors into safe categories", () => {
	expect(updateFailureCategory("net::ERR_CONNECTION_RESET")).toBe("network");
	expect(updateFailureCategory("getaddrinfo ENOTFOUND github.com")).toBe("network");
	expect(updateFailureCategory("sha512 checksum mismatch")).toBe("signature");
	expect(updateFailureCategory("EACCES: permission denied")).toBe("permission");
	expect(updateFailureCategory("ENOSPC: no space left on device")).toBe("disk_space");
	expect(updateFailureCategory("HTTP 404 not found")).toBe("not_found");
	expect(updateFailureCategory("Cannot update: unsupported platform")).toBe("not_supported");
	expect(updateFailureCategory(undefined)).toBe("unknown");
});

// The raw message carries the feed URL and local staging paths, so only the
// bucketed category may leave the process.
test("never forwards the raw updater message", () => {
	const outcome = updateFailureOutcome(
		"EACCES: permission denied, open '/Users/someone/Library/Caches/ao-updater/pending/AO.zip'",
		"download",
		"automatic",
		"0.11.3",
	);
	expect(outcome).toEqual({
		event: "ao.renderer.update_failed",
		phase: "download",
		trigger: "automatic",
		error_category: "permission",
		to_version: "0.11.3",
	});
	expect(JSON.stringify(outcome)).not.toContain("someone");
});

// The shared classifier is the main process's rewrite precondition and the
// renderer's last-resort guard, so it must recognise every dump shape the
// GitHub feed has actually produced (issue #5755) and nothing shorter.
test("recognises raw updater dumps by shape, not by known phrases", () => {
	expect(looksLikeTechnicalUpdateDump("Couldn't check for updates. Try again.")).toBe(false);
	expect(looksLikeTechnicalUpdateDump("net::ERR_CONNECTION_RESET")).toBe(false);
	expect(looksLikeTechnicalUpdateDump("Download failed. Try again.")).toBe(false);

	expect(looksLikeTechnicalUpdateDump('HttpError: 503 "method: GET"')).toBe(true);
	expect(looksLikeTechnicalUpdateDump("<html><body>Gateway Time-out</body></html>")).toBe(true);
	expect(looksLikeTechnicalUpdateDump('Headers: {"content-type":"text/html"}')).toBe(true);
	expect(looksLikeTechnicalUpdateDump("at GitHubProvider.getLatestTagName (app.asar:1:1)")).toBe(true);

	// Length alone is enough: our own lines are all short, a body dump is not.
	expect(looksLikeTechnicalUpdateDump("x".repeat(281))).toBe(true);
	expect(looksLikeTechnicalUpdateDump("x".repeat(280))).toBe(false);
});

// phase and trigger come from the main process, which is the only place that
// knows whether the hourly timer or the user started the operation.
test("records the phase and trigger it was given", () => {
	expect(updateFailureOutcome("boom", "check", "automatic", undefined)).toMatchObject({
		phase: "check",
		trigger: "automatic",
	});
	expect(updateFailureOutcome("boom", "download", "manual", undefined)).toMatchObject({
		phase: "download",
		trigger: "manual",
	});
	// An unknown target version is omitted rather than sent as undefined.
	expect(updateFailureOutcome("boom", "check", "manual", undefined)).not.toHaveProperty("to_version");
});

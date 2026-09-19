import { test as base, expect } from "@playwright/test";

// Shared AO Playwright fixture: an uncaught renderer error must fail the owning
// test instead of being silently swallowed. Ordinary specs pass while the
// renderer emits `pageerror` / error-level console events (ResizeObserver loops,
// xterm `Viewport.syncScrollArea` throws, etc.), so a green assertion can hide a
// real production regression. This gate records those events and fails the test
// in teardown with the captured stacks.
//
// Import `test` and `expect` from this module instead of `@playwright/test` so
// the gate is always installed:
//
//   import { expect, test } from "./support/test";
//
// A narrowly documented, linked upstream defect may be tolerated per-spec via
// `test.use({ allowedPageErrors: [...] })`. Keep the allowlist EXACT-signature
// (a full string equality or a tightly anchored RegExp) and link the tracking
// issue in a comment, so it cannot mask an unrelated new failure. Do NOT add a
// broad ResizeObserver/xterm allowlist here: that would re-hide
// https://github.com/Untrivial-ai/agent-orchestrator/issues/3787.

/**
 * Exact signature of a tolerated renderer error. A `string` matches the error
 * message by full equality; a `RegExp` is tested against it. Anchor RegExps
 * (`^`/`$`) so the allowance stays scoped to the one known defect.
 */
export type PageErrorSignature = string | RegExp;

type CapturedErrorKind = "pageerror" | "console.error";

interface CapturedPageError {
	kind: CapturedErrorKind;
	message: string;
	stack?: string;
	location?: string;
}

function matchesSignature(message: string, sig: PageErrorSignature): boolean {
	return typeof sig === "string" ? message === sig : sig.test(message);
}

function formatEvidence(errors: CapturedPageError[]): string {
	return errors
		.map((e, i) => {
			const lines = [`  [${i + 1}] (${e.kind}) ${e.message}`];
			if (e.location) lines.push(`      at ${e.location}`);
			if (e.stack) {
				lines.push(
					e.stack
						.split("\n")
						.map((l) => `      ${l.trimEnd()}`)
						.join("\n"),
				);
			}
			return lines.join("\n");
		})
		.join("\n");
}

interface AoFixtures {
	/**
	 * Per-spec allowlist of tolerated renderer error signatures. Empty by
	 * default. Override with `test.use({ allowedPageErrors: [/^…$/] })`.
	 */
	allowedPageErrors: PageErrorSignature[];
	/**
	 * Auto fixture that records renderer errors for the active `page` and fails
	 * the test in teardown if any unexpected ones were seen.
	 */
	failOnPageErrors: void;
}

export const test = base.extend<AoFixtures>({
	allowedPageErrors: [[], { option: true }],

	failOnPageErrors: [
		async ({ page, allowedPageErrors }, use) => {
			const captured: CapturedPageError[] = [];

			page.on("pageerror", (error) => {
				captured.push({
					kind: "pageerror",
					message: error.message,
					stack: error.stack,
				});
			});
			page.on("console", (msg) => {
				if (msg.type() !== "error") return;
				const loc = msg.location();
				const location = loc.url
					? `${loc.url}:${loc.lineNumber}:${loc.columnNumber}`
					: undefined;
				captured.push({
					kind: "console.error",
					message: msg.text(),
					location,
				});
			});

			// Run the test body. Preserve any failure it throws so the gate never
			// hides the original assertion error.
			let bodyError: unknown;
			try {
				await use();
			} catch (err) {
				bodyError = err;
			}

			const unexpected = captured.filter(
				(e) => !allowedPageErrors.some((sig) => matchesSignature(e.message, sig)),
			);

			if (unexpected.length > 0) {
				const evidence = formatEvidence(unexpected);
				const testInfo = test.info();
				await testInfo.attach("unexpected-page-errors", {
					body: JSON.stringify(unexpected, null, 2),
					contentType: "application/json",
				});

				const summary =
					`${unexpected.length} unexpected renderer error(s) during ` +
					`"${testInfo.title}":\n${evidence}`;

				// If the test body already failed, append the evidence to that error
				// so both the original assertion failure and the renderer errors are
				// reported together.
				if (bodyError instanceof Error) {
					bodyError.message += `\n\n[page-error gate] ${summary}`;
					throw bodyError;
				}
				if (bodyError !== undefined) throw bodyError;
				throw new Error(`[page-error gate] ${summary}`);
			}

			if (bodyError !== undefined) throw bodyError;
		},
		{ auto: true },
	],
});

export { expect };

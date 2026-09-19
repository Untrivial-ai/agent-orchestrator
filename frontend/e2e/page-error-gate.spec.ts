import { expect, test } from "./support/test";

// Negative control for the shared page-error gate (see ./support/test.ts and
// https://github.com/Untrivial-ai/agent-orchestrator/issues/4429). These specs
// prove the gate actually detects renderer errors rather than silently passing.
//
// The deliberate-failure case is quarantined behind an env flag so CI stays
// green: run it locally with
//
//   AO_PROVE_PAGE_ERROR_GATE=1 npx playwright test page-error-gate --grep @negative-control
//
// and confirm the test FAILS with a "[page-error gate] ... unexpected renderer
// error(s)" message even though its assertion passed.

test("a clean scenario stays green", async ({ page }) => {
	await page.goto("/#/projects/ao-demo/sessions/demo-working");
	await expect(page.locator("#inspector")).toBeVisible();
});

test.describe("allowlisted signatures", () => {
	// Exact-signature allowance for this describe block only.
	test.use({ allowedPageErrors: [/^ao-gate synthetic allowlisted error$/] });

	test("an allowlisted error signature does not fail the gate", async ({
		page,
	}) => {
		await page.goto("/#/projects/ao-demo/sessions/demo-working");
		await page.evaluate(() => {
			setTimeout(() => {
				throw new Error("ao-gate synthetic allowlisted error");
			}, 0);
		});
		// Assertion still passes; the gate must NOT fail because the signature is
		// explicitly allowlisted.
		await expect(page.locator("#inspector")).toBeVisible();
		await page.waitForTimeout(50);
	});
});

// Deliberately-injected uncaught renderer error. The assertion below passes, so
// WITHOUT the gate this test would report green. WITH the gate it must fail.
// Gated behind AO_PROVE_PAGE_ERROR_GATE so it does not break normal runs.
const proveGate = process.env.AO_PROVE_PAGE_ERROR_GATE === "1";
// eslint-disable-next-line playwright/no-skipped-test
(proveGate ? test : test.skip)(
	"@negative-control a synthetic page error fails an otherwise-green test",
	async ({ page }) => {
		await page.goto("/#/projects/ao-demo/sessions/demo-working");
		await page.evaluate(() => {
			// Uncaught -> surfaces as a `pageerror` the gate records.
			setTimeout(() => {
				throw new Error("ao-gate synthetic uncaught renderer error");
			}, 0);
		});
		// This assertion PASSES; the test only fails via the gate.
		await expect(page.locator("#inspector")).toBeVisible();
		await page.waitForTimeout(50);
	},
);

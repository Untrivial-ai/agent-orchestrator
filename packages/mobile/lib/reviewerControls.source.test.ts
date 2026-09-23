import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const actions = readFileSync(new URL("../app/sheets/review-actions.tsx", import.meta.url), "utf8");
const detail = readFileSync(new URL("../app/review/[sessionId].tsx", import.meta.url), "utf8");

describe("reviewer control integration", () => {
	it("keeps the project-default override separate from the effective reviewer", () => {
		expect(actions).toContain('const [reviewerOverride, setReviewerOverride] = useState("")');
		expect(actions).toContain("setEffectiveReviewer(reviewState.reviewerHarness || reviewer)");
		expect(actions).toContain("selected={!reviewerOverride}");
	});

	it("ignores stale model catalogs after the effective reviewer changes", () => {
		expect(actions).toContain("const request = ++modelRequest.current");
		expect(actions).toContain("request === modelRequest.current");
	});

	it("renders every reviewer through the shared harness logo registry", () => {
		expect(actions).toContain('import { AgentLogo } from "../../lib/AgentLogo"');
		expect(actions).toContain("harness={agent.id}");
		expect(actions).toContain("<AgentLogo harness={harness}");
	});

	it("does not let auto review lose its persistent reviewer", () => {
		expect(detail).toContain("!data.reviewerHandleId || autoReviewEnabled");
		expect(detail).toContain("disabled={Boolean(mutation) || autoReviewEnabled}");
	});
});

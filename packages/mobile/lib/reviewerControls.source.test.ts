import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const actions = readFileSync(new URL("../app/sheets/review-actions.tsx", import.meta.url), "utf8");
const detail = readFileSync(new URL("../app/review/[sessionId].tsx", import.meta.url), "utf8");
const pickerIOS = readFileSync(new URL("./reviewer-picker.ios.tsx", import.meta.url), "utf8");
const picker = readFileSync(new URL("./reviewer-picker.tsx", import.meta.url), "utf8");
const terminal = readFileSync(new URL("./session/TerminalSessionScreen.tsx", import.meta.url), "utf8");
const source = (path: string) => readFileSync(new URL(path, import.meta.url), "utf8");

describe("reviewer control integration", () => {
	it("keeps the project-default override separate from the effective reviewer", () => {
		expect(actions).toContain('const [reviewerOverride, setReviewerOverride] = useState("")');
		expect(actions).toContain("const effectiveReviewer = reviewerOverride || projectDefaultReviewer;");
		expect(actions).toContain("defaultReviewerHarness(project?.config?.reviewers, session.harness ?? undefined)");
		// The daemon's reviewerHarness names the last reviewer that ran, so it
		// must never pick the model catalog after a switch.
		expect(actions).not.toContain("result.reviewerHarness");
		expect(actions).not.toContain("reviewState.reviewerHarness");
		expect(actions).toContain("selectedReviewer={reviewerOverride}");
		expect(actions).toContain("effectiveReviewer={effectiveReviewer}");
	});

	it("ignores stale model catalogs after the effective reviewer changes", () => {
		expect(actions).toContain("const request = ++modelRequest.current");
		expect(actions).toContain("request === modelRequest.current");
	});

	it("confirms model and mode changes while a review is running", () => {
		expect(actions).toContain("confirmReviewerChange(reviewerOverride, { ...reviewerConfig, [key]: value })");
	});

	it("does not warn or save when the selected reviewer settings are unchanged", () => {
		expect(actions).toContain("reviewerSelectionChanged(reviewerOverride, reviewerConfig, id, agentConfig)");
	});

	it("renders every reviewer through the shared harness logo registry", () => {
		expect(pickerIOS).toContain('import { HarnessImage, useHarnessLogoUris } from "./spawn-composer-controls.ios"');
		expect(pickerIOS).toContain("<HarnessImage uri={logoUris[agent.id]} harness={agent.id} />");
		expect(picker).toContain("<AgentLogo harness={harness}");
	});

	it("expands reviewer and model choices inline on Android/web", () => {
		expect(picker).toContain('accessibilityState={{ expanded: expanded === "reviewer", disabled: busy }}');
		expect(picker).toContain('accessibilityState={{ expanded: expanded === "model", disabled: busy }}');
		expect(picker).toContain('{expanded === "reviewer" ? <View style={styles.options}>');
		expect(picker).toContain('{expanded === "model" ? <View style={styles.options}>');
	});

	it("keeps Android review actions scrolling inside the sheet", () => {
		expect(actions).toMatch(/<ScrollView[^>]*nestedScrollEnabled/);
	});

	it("picks the reviewer and model from native iOS menus like the spawn sheet", () => {
		expect(pickerIOS).toContain('import { Button, HStack, Image, Menu, Spacer, Text, VStack } from "@expo/ui/swift-ui"');
		expect(pickerIOS).toContain('accessibilityIdentifier("review-reviewer")');
		expect(pickerIOS).toContain('accessibilityIdentifier("review-model")');
	});

	it("only offers reviewers that can run, like the spawn sheet", () => {
		expect(actions).toContain(".filter((agent) => agent.selectable || agent.id === reviewerOverride)");
		expect(actions).toContain("reviewers={availableReviewers}");
	});

	it("uses the desktop inspector's automation wording and the system switch colors", () => {
		expect(actions).toContain('title="Auto review"');
		expect(actions).toContain('title="Automatically fix review comments"');
		expect(actions).toContain('title="Automatically fix CI failures"');
		expect(actions).not.toContain("trackColor");
	});

	it("does not let auto review lose its persistent reviewer", () => {
		expect(detail).toContain("!data.reviewerHandleId || autoReviewEnabled");
		expect(detail).toContain("const stopActions: ItemAction[] = controls.stop && !autoReviewEnabled");
	});

	it("checks fresh review state before changing reviewer, model, or mode", () => {
		expect(actions).toContain("const latest = await getSessionReviews(config, sessionId)");
		expect(actions).toContain("latest.reviews.some((item) => item.status === \"running\")");
		expect(actions).toContain("confirmReviewerChange(reviewerOverride");
		expect(actions).not.toContain('reviewerSwitchWarning(running === "true")');
	});

	it("does not fall back to a different pull request", () => {
		expect(actions).toContain("setPR(matchedPR)");
		expect(actions).not.toContain("?? prs[0]");
	});

	it("only offers finished review findings to the worker", () => {
		expect(detail).toContain("...(reviewRunSendable(run) && !sent ? [{ id: \"send\", label: \"Send to worker\"");
	});

	it("never presents the in-app browser on top of the actions sheet", () => {
		const calls = actions.match(/openGitHub\([^)]*\)/g) ?? [];
		expect(calls.length).toBeGreaterThan(0);
		for (const call of calls) expect(call).toContain("{ fromSheet: true }");
	});

	it("keeps per-item actions in a desktop-style menu", () => {
		expect(actions).toContain("<ItemActionsMenu accessibilityLabel={`Actions for ${reviewerId}'s comment`}");
		expect(actions).toContain('{ id: "resolve", label: "Resolve"');
		expect(detail).toContain("<ItemActionsMenu accessibilityLabel={`Actions for the ${reviewVerdictLabel(run).toLowerCase()} review`}");
	});

	it("chooses Open, Restore, or Stop from what is actually available", () => {
		expect(detail).toContain("const controls = reviewerControls(data, review, sessionId);");
		expect(detail).toContain("{controls.open\n\t\t\t\t\t\t? <RowPill label=\"Open\"");
		expect(detail).toContain(": controls.restore ? <RowPill label=\"Restore\"");
		expect(detail).toContain("Push a new commit to run another review.");
	});

	// The reviewer pane is attached by handle, but it is not a daemon shell
	// terminal: closing it as one answered 404 "No such shell terminal".
	it("stops the reviewer, not a shell, when its terminal is closed", () => {
		expect(terminal).toContain('const reviewerPane = params.kind === "reviewer" && Boolean(params.sessionId);');
		expect(terminal).toContain("if (reviewerPane) await killSessionReviewer(config, String(params.sessionId));");
	});

	// A native menu nested in the row's Pressable lost its tap to the row, which
	// opened the reviewer instead of the menu.
	it("lays the reviewer's pill and menu over its row instead of nesting them", () => {
		const row = detail.slice(detail.indexOf("onPress={openReviewer} style="), detail.indexOf('<ListSectionHeader label="AO review" />'));
		expect(row.indexOf("</Pressable>")).toBeLessThan(row.indexOf("<ItemActionsMenu"));
		expect(detail).toContain('<View style={styles.overlaySlot} pointerEvents="box-none">');
	});

	// A page pushed while a sheet is presented opens inside that sheet, so every
	// way into review detail and the reviewer goes through useOpenPage.
	it("opens review detail and the reviewer as pages, never inside a sheet", () => {
		expect(source("./PRCard.tsx")).toContain("openPage({\n\t\t\t\t\tpathname: \"/review/[sessionId]\"");
		expect(source("./worker-list-row.tsx")).toContain("if (reviewRoute) openPage(reviewRoute);");
		expect(source("./chat/ChatSessionScreen.tsx")).toContain("if (route) openPage(route);");
		expect(source("../app/notifications.tsx")).toContain('action.kind === "review") openPage(');
		expect(source("./PushManager.tsx")).toContain('if (target === "review") openPage(destination as Href);');
		expect(detail).toContain("openPage(destination);");
		for (const path of ["./PRCard.tsx", "./worker-list-row.tsx", "./chat/ChatSessionScreen.tsx", "../app/review/[sessionId].tsx"]) {
			expect(source(path)).not.toMatch(/router\.(push|navigate)\((reviewRoute|route|destination)\)/);
		}
	});

	it("lays the review screen out as flat board rows, not cards", () => {
		expect(detail).not.toContain("<Card");
		expect(detail).toContain('<ListSectionHeader label="Pull request" />');
		expect(detail).toContain("borderBottomWidth: rowDividerWidth");
	});

	it("merges like desktop: only when ready, fenced to the head commit, after confirmation", () => {
		expect(detail).toContain("const merge = pr ? mergeReadiness(pr) : undefined;");
		expect(detail).toContain("await mergeSessionPR(config, pr)");
		expect(detail).toContain("This will squash-merge PR #${pr.number} in the remote repository.");
	});

	it("keys automatic-review dismissal to the stable run id", () => {
		expect(detail).toContain("const autoReviewFailureId = autoReviewFailure?.id");
		expect(detail).toContain("[autoReviewFailureId, dismissedAutoFailureId]");
	});
});

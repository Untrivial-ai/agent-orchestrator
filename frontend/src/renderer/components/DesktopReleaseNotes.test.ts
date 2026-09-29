import { expect, it } from "vitest";
import { prepareDesktopReleaseNotes } from "../lib/desktop-release-notes";

it("removes contributor handles and the comparison footer while retaining linked PR numbers", () => {
	const notes = [
		"### Added",
		"",
		"- Add useful workflows by @person in [#123](https://github.com/Untrivial-ai/agent-orchestrator/pull/123)",
		"- Update dependencies by @dependabot[bot] in [#124](https://github.com/Untrivial-ai/agent-orchestrator/pull/124)",
		"",
		"**Full Changelog**: https://github.com/Untrivial-ai/agent-orchestrator/compare/v0.13.0...v0.13.1",
	].join("\n");

	expect(prepareDesktopReleaseNotes(notes)).toBe([
		"### Added",
		"",
		"- Add useful workflows [#123](https://github.com/Untrivial-ai/agent-orchestrator/pull/123)",
		"- Update dependencies [#124](https://github.com/Untrivial-ai/agent-orchestrator/pull/124)",
	].join("\n"));
});

it("leaves nightly notes and unrelated user text unchanged", () => {
	const notes = "This automated build tracks main.\n\n- Source: @team in #general";
	expect(prepareDesktopReleaseNotes(notes)).toBe(notes);
});

it("removes contributor handles from generated nightly changes", () => {
	const notes = [
		"**Changes in this nightly**",
		"",
		"- Add durable scheduled automations by @person in [#4459](https://github.com/Untrivial-ai/agent-orchestrator/pull/4459)",
		"",
		"**Build details**",
	].join("\n");

	expect(prepareDesktopReleaseNotes(notes)).toBe([
		"**Changes in this nightly**",
		"",
		"- Add durable scheduled automations [#4459](https://github.com/Untrivial-ai/agent-orchestrator/pull/4459)",
		"",
		"**Build details**",
	].join("\n"));
});

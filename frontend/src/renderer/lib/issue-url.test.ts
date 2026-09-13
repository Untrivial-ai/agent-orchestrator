import { describe, expect, it } from "vitest";
import { trackerIssueLink } from "./issue-url";

describe("trackerIssueLink", () => {
	it("links GitHub intake ids that already include owner/repo", () => {
		expect(trackerIssueLink("github:acme/demo#12")).toEqual({
			url: "https://github.com/acme/demo/issues/12",
			label: "#12",
			nativeId: "acme/demo#12",
		});
	});

	it("links GitLab.com and self-managed canonical ids", () => {
		expect(trackerIssueLink("gitlab:group/project#7")).toEqual({
			url: "https://gitlab.com/group/project/-/issues/7",
			label: "#7",
			nativeId: "group/project#7",
		});
		expect(trackerIssueLink("gitlab:group/sub/project#9@gitlab.internal")).toEqual({
			url: "https://gitlab.internal/group/sub/project/-/issues/9",
			label: "#9",
			nativeId: "group/sub/project#9",
		});
		expect(trackerIssueLink("gitlab:group/repo#7@gitlab.local:8443")).toEqual({
			url: "https://gitlab.local:8443/group/repo/-/issues/7",
			label: "#7",
			nativeId: "group/repo#7",
		});
	});

	it("links issue web URLs and ignores pull-request paths", () => {
		expect(trackerIssueLink("https://github.com/acme/demo/issues/12")).toEqual({
			url: "https://github.com/acme/demo/issues/12",
			label: "#12",
			nativeId: "acme/demo#12",
		});
		expect(trackerIssueLink("https://gitlab.com/group/project/-/issues/7")).toEqual({
			url: "https://gitlab.com/group/project/-/issues/7",
			label: "#7",
			nativeId: "group/project#7",
		});
		expect(trackerIssueLink("https://github.com/acme/demo/pull/12")).toBeUndefined();
	});

	it("does not guess a repo for number-only or non-tracker ids", () => {
		expect(trackerIssueLink("github:42")).toBeUndefined();
		expect(trackerIssueLink("42")).toBeUndefined();
		expect(trackerIssueLink("github:INT-17")).toBeUndefined();
		expect(trackerIssueLink("Fix the renderer")).toBeUndefined();
		expect(trackerIssueLink(undefined)).toBeUndefined();
	});

	it("resolves a bare number only against a single project origin (unused by board/topbar today)", () => {
		expect(
			trackerIssueLink("github:42", { originUrl: "https://github.com/acme/demo.git" }),
		).toEqual({
			url: "https://github.com/acme/demo/issues/42",
			label: "#42",
			nativeId: "acme/demo#42",
		});
		expect(
			trackerIssueLink("7", { originUrl: "git@gitlab.internal:group/project.git" }),
		).toEqual({
			url: "https://gitlab.internal/group/project/-/issues/7",
			label: "#7",
			nativeId: "group/project#7",
		});
		expect(trackerIssueLink("42", { originUrl: "" })).toBeUndefined();
	});
});

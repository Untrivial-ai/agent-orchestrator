import { describe, expect, it } from "vitest";
import { joinCloneDestination, repositoryNameFromGitUrl } from "./CloneRepositoryDialog";

describe("clone repository input", () => {
	it.each([
		["https://github.com/acme/web-app.git", "web-app"],
		["ssh://git@github.com/acme/web-app.git", "web-app"],
		["git@github.com:acme/web-app.git", "web-app"],
		["file:///tmp/web-app", "web-app"],
		["file:///tmp/my%20repo.git", "my repo"],
		["https://github.com/acme/nested%2Frepo.git", "repo"],
		["file:///tmp/literal%252Frepo.git", "literal%2Frepo"],
	])("derives the checkout name from %s", (remoteUrl, expected) => {
		expect(repositoryNameFromGitUrl(remoteUrl)).toBe(expected);
	});

	it.each([
		"repository-without-a-scheme",
		"--upload-pack=malicious",
		"https://user:secret@example.com/acme/repo.git",
		"https://example.com/acme/repo.git?access_token=secret",
		"ssh://git:secret@example.com/acme/repo.git",
		"https://github.com/acme/two words.git",
		"https://github.com/acme/web-app/pull/123",
		"https://github.com/acme/web-app/issues/12",
		"https://gitlab.com/acme/web-app/-/merge_requests/12",
		"file:///tmp/bad%ZZ.git",
	])("rejects unsafe or incomplete URL %s", (remoteUrl) => {
		expect(repositoryNameFromGitUrl(remoteUrl)).toBeNull();
	});

	it("joins POSIX and Windows destinations", () => {
		expect(joinCloneDestination("/Users/me/Code/", "web-app")).toBe("/Users/me/Code/web-app");
		expect(joinCloneDestination("C:\\Code\\", "web-app")).toBe("C:\\Code\\web-app");
	});
});

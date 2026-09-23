import { describe, expect, it } from "vitest";
import { buildCoderRequestOptions } from "./coder-session-options-store";

const base = {
	templateId: "",
	supportedParams: [] as string[],
	size: "medium" as const,
	startupScript: "",
	extraRepos: [],
};

describe("buildCoderRequestOptions", () => {
	it("returns undefined for the default template with no extra repos", () => {
		expect(buildCoderRequestOptions(base)).toBeUndefined();
	});

	it("sends only the template id when the template declares no parameters", () => {
		// A paramless template (e.g. a pre-existing infra template) must never
		// carry size/startup, or Coder rejects the build.
		const coder = buildCoderRequestOptions({
			...base,
			templateId: "tpl-1",
			supportedParams: [],
			size: "large",
			startupScript: "make dev",
		});
		expect(coder).toEqual({ templateId: "tpl-1" });
	});

	it("sends size and startup only when the template declares them", () => {
		const coder = buildCoderRequestOptions({
			...base,
			templateId: "tpl-2",
			supportedParams: ["size", "startup_script"],
			size: "large",
			startupScript: "make dev",
		});
		expect(coder).toEqual({ templateId: "tpl-2", size: "large", startupScript: "make dev" });
	});

	it("sends size but not startup when only size is declared", () => {
		const coder = buildCoderRequestOptions({
			...base,
			templateId: "tpl-3",
			supportedParams: ["size"],
			size: "small",
			startupScript: "make dev",
		});
		expect(coder).toEqual({ templateId: "tpl-3", size: "small" });
	});

	it("omits an empty startup script even when declared", () => {
		const coder = buildCoderRequestOptions({
			...base,
			templateId: "tpl-4",
			supportedParams: ["size", "startup_script"],
			size: "medium",
			startupScript: "   ",
		});
		expect(coder).toEqual({ templateId: "tpl-4", size: "medium" });
	});

	it("carries extra repos independently of the template", () => {
		const coder = buildCoderRequestOptions({
			...base,
			templateId: "",
			extraRepos: [{ url: "https://github.com/owner/repo", branch: "main" }],
		});
		expect(coder).toEqual({ extraRepos: [{ url: "https://github.com/owner/repo", branch: "main" }] });
	});
});

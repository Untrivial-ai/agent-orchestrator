import { describe, expect, it } from "vitest";
import { isActiveInstallJob } from "./InstallDependencyDialog";

describe("isActiveInstallJob", () => {
	it.each(["running", "installing", "verifying"])("treats %s as active", (status) => {
		expect(isActiveInstallJob({ target: "codex", status } as never)).toBe(true);
	});

	it.each(["succeeded", "failed", "unsupported", "interrupted"])("treats %s as terminal", (status) => {
		expect(isActiveInstallJob({ target: "codex", status } as never)).toBe(false);
	});
});

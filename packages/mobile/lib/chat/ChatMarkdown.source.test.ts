import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// ChatMarkdown is a React Native component and this package has no component
// renderer, so the contract is pinned against its source — the same approach as
// ChatSessionScreen.source.test.ts.
const source = readFileSync(new URL("./ChatMarkdown.tsx", import.meta.url), "utf8");

describe("ChatMarkdown link handling", () => {
	// This renderer is the app's only Markdown surface, so every screen that
	// shows agent or reviewer prose reaches for it. Requiring a provider made a
	// forgotten one crash on the first body containing a URL — intermittent, and
	// invisible to tsc because the context is only read at runtime.
	it("does not require a provider", () => {
		expect(source).not.toMatch(/throw new Error\([^)]*ChatLinkProvider/);
		expect(source).toContain("useContext(OpenChatLink) ?? defaultOpenChatLink");
	});

	// The default has to keep the user inside AO, or a screen without a provider
	// silently reopens the bug this change exists to fix (#5646).
	it("falls back to the in-app browser, not the system one", () => {
		expect(source).toMatch(/function defaultOpenChatLink[\s\S]*?openGitHub\(url\)/);
		expect(source).not.toMatch(/function defaultOpenChatLink[\s\S]*?Linking\.openURL/);
	});
});

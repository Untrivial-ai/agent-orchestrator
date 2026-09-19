import { describe, expect, it, vi } from "vitest";

const { AgentLogo, Text, View } = vi.hoisted(() => ({
	AgentLogo: vi.fn(),
	Text: vi.fn(),
	View: vi.fn(),
}));

vi.mock("../AgentLogo", () => ({ AgentLogo }));
vi.mock("react-native", () => ({
	StyleSheet: { create: (styles: unknown) => styles },
	Text,
	View,
}));
vi.mock("../ThemeProvider", () => ({
	useTheme: () => ({ green: "green", orange: "orange", red: "red", amber: "amber" }),
	useThemedStyles: (factory: (theme: Record<string, string>) => unknown) => factory({
		textPrimary: "white",
		textTertiary: "gray",
	}),
}));

import { ConversationTitle } from "./ConversationTitle";

describe("ConversationTitle", () => {
	it("shows the active harness logo beside the worker title", () => {
		const element = ConversationTitle({
			title: "Say hi",
			subtitle: "meetyou · claude-code",
			harness: "claude-code",
			state: "ready",
		});

		const children = Array.isArray(element.props.children)
			? element.props.children
			: [element.props.children];
		expect(children[0].type).toBe(AgentLogo);
		expect(children[0].props).toMatchObject({ harness: "claude-code", size: 22 });
	});
});

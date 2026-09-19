import { describe, expect, it, vi } from "vitest";

const { Pill, View } = vi.hoisted(() => ({ Pill: vi.fn(), View: vi.fn() }));

vi.mock("react-native", () => ({
	StyleSheet: { create: (styles: unknown) => styles, hairlineWidth: 0.5 },
	View,
}));
vi.mock("./ui", () => ({ Pill }));
vi.mock("./ThemeProvider", () => ({
	useThemedStyles: (factory: (theme: Record<string, string>) => unknown) => factory({
		bgElevated: "opaque-surface",
		borderDefault: "border",
	}),
}));

import { PRFilterDock } from "./pr-filter-dock";

describe("PRFilterDock", () => {
	it("puts an opaque surface behind filters so list content cannot show through", () => {
		const element = PRFilterDock({
			filter: "open",
			counts: { open: 4, merged: 10, all: 16 },
			onChange: vi.fn(),
		});

		expect(element.props.style).toMatchObject({
			backgroundColor: "opaque-surface",
			borderColor: "border",
		});
	});
});

import { describe, expect, it, vi } from "vitest";

const { MenuView, Feather, View } = vi.hoisted(() => ({
	MenuView: vi.fn(),
	Feather: vi.fn(),
	View: vi.fn(),
}));

vi.mock("@expo/ui/community/menu", () => ({ MenuView }));
vi.mock("@expo/vector-icons", () => ({ Feather }));
vi.mock("react-native", () => ({
	StyleSheet: { create: (styles: unknown) => styles },
	View,
}));
vi.mock("../haptics", () => ({ haptics: { tap: vi.fn() } }));
vi.mock("../ThemeProvider", () => ({
	useTheme: () => ({ textFaint: "dim", textSecondary: "label" }),
	useThemedStyles: (factory: (theme: Record<string, string>) => unknown) => factory({
		textFaint: "dim",
		textSecondary: "label",
	}),
}));

import { ChatAttachmentMenu } from "./ChatAttachmentMenu";

describe("ChatAttachmentMenu", () => {
	it("offers only native photo and supported file actions", () => {
		const element = ChatAttachmentMenu({
			disabled: false,
			canAttachFile: true,
			onChoosePhoto: vi.fn(),
			onChooseFile: vi.fn(),
		});

		expect(element.type).toBe(MenuView);
		expect(element.props.actions).toEqual([
			{ id: "photo", title: "Choose Photo", image: "photo" },
			{ id: "file", title: "Choose File", image: "doc" },
		]);
	});

	it("routes the selected native action to its picker", () => {
		const onChoosePhoto = vi.fn();
		const onChooseFile = vi.fn();
		const element = ChatAttachmentMenu({ disabled: false, canAttachFile: true, onChoosePhoto, onChooseFile });

		element.props.onPressAction({ nativeEvent: { event: "file" } });

		expect(onChooseFile).toHaveBeenCalledOnce();
		expect(onChoosePhoto).not.toHaveBeenCalled();
	});
});

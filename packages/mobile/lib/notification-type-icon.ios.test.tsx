import { describe, expect, it, vi } from "vitest";

const { Host, Icon } = vi.hoisted(() => ({ Host: vi.fn(), Icon: vi.fn() }));

vi.mock("@expo/ui", () => ({ Icon }));
vi.mock("@expo/ui/swift-ui", () => ({ Host }));

import { NotificationTypeIcon } from "./notification-type-icon.ios";

describe("NotificationTypeIcon on iOS", () => {
	it("hosts the SwiftUI icon before mounting it in a React Native row", () => {
		const element = NotificationTypeIcon({ icon: "message-circle", color: "#fff" });

		expect(element.type).toBe(Host);
		expect(element.props.matchContents).toBe(true);
		expect(element.props.children.type).toBe(Icon);
	});
});

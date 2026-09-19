import type { RowProps } from "@expo/ui";
import { contentShape, shapes } from "@expo/ui/swift-ui/modifiers";

export const sidebarDestinationHitModifiers: NonNullable<RowProps["modifiers"]> = [
	contentShape(shapes.rectangle()),
];

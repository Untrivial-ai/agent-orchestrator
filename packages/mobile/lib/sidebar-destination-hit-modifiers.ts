import type { RowProps } from "@expo/ui";

// Compose's clickable modifier already occupies the Row's complete measured
// bounds. iOS needs an explicit SwiftUI content shape for transparent spacing.
export const sidebarDestinationHitModifiers: NonNullable<RowProps["modifiers"]> = [];

import { Platform, StyleSheet } from "react-native";

/**
 * Width of the divider between list rows.
 *
 * iOS keeps the true hairline. On Android `hairlineWidth` is one physical pixel
 * in dp — 0.38dp on a 2.625x screen — and a border that thin lands on
 * fractional y positions as rows lay out, so some rounds to zero pixels and
 * simply is not drawn: every few rows the divider went missing. One dp always
 * covers at least a whole pixel, so every divider draws.
 */
export const rowDividerWidth = Platform.OS === "android" ? 1 : StyleSheet.hairlineWidth;

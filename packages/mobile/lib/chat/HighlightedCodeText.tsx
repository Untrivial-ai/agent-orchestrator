import { memo, useMemo } from "react";
import { Text, type StyleProp, type TextStyle } from "react-native";
import { useTheme } from "../ThemeProvider";
import { highlightCode, type SyntaxTokenKind } from "./syntaxHighlight";

/**
 * Native, selectable syntax highlighting without a WebView or provider SDK.
 * Streaming code remains plain text so rapidly arriving deltas do not cause a
 * full grammar pass on every character; it is highlighted once settled.
 */
export const HighlightedCodeText = memo(function HighlightedCodeText({
	code,
	language,
	streaming = false,
	style,
}: {
	code: string;
	language?: string;
	streaming?: boolean;
	style?: StyleProp<TextStyle>;
}) {
	const t = useTheme();
	const tokens = useMemo(
		() => streaming ? undefined : highlightCode(code, language),
		[code, language, streaming],
	);
	return (
		<Text selectable style={style}>
			{tokens?.map((token, index) => (
				<Text key={`${index}:${token.text.length}`} style={{ color: tokenColor(token.kind, t) }}>
					{token.text}
				</Text>
			)) ?? code}
		</Text>
	);
});

function tokenColor(kind: SyntaxTokenKind, t: ReturnType<typeof useTheme>): string {
	switch (kind) {
		case "comment": return t.textFaint;
		case "string":
		case "addition": return t.green;
		case "number": return t.orange;
		case "keyword": return t.amber;
		case "type":
		case "meta": return t.textSecondary;
		case "deletion": return t.red;
		default: return t.textPrimary;
	}
}

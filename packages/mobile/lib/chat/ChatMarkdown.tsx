import { Feather } from "../icons";
import * as Clipboard from "expo-clipboard";
import { createContext, Fragment, memo, useContext, useState, type ReactNode } from "react";
import { Image, Pressable, ScrollView, StyleSheet, Text, View, type StyleProp, type TextStyle } from "react-native";
import { haptics } from "../haptics";
import { openGitHub } from "../openGitHub";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { HighlightedCodeText } from "./HighlightedCodeText";
import { parseBlocks } from "./markdownBlocks";
import { iconSize, microLabel, prose, radius, space, type } from "../tokens";

// The conversation screen decides how a tapped link opens (it knows the AO host
// that the agent's localhost links map onto); the renderer only reports the tap.
const OpenChatLink = createContext<((url: string) => void) | null>(null);

/**
 * What a tapped link does when the renderer is used outside a provider.
 *
 * `openGitHub` is already the whole rule for a web page — GitHub app when it has
 * a screen for it, in-app browser otherwise — so the only thing a caller loses
 * without a provider is the localhost-to-AO-host rewrite, which only the
 * conversation needs.
 *
 * This used to throw instead. `ChatMarkdown` is the app's only Markdown
 * renderer, so every new surface that shows agent or reviewer prose reaches for
 * it; a required provider turns forgetting one into a crash on the first body
 * that happens to contain a URL, which is both intermittent and invisible to
 * tsc. A default that still keeps the user inside the app is the safer contract.
 */
function defaultOpenChatLink(url: string): void {
	void openGitHub(url);
}

export function ChatLinkProvider({ onLinkOpen, children }: { onLinkOpen: (url: string) => void; children: ReactNode }) {
	return <OpenChatLink.Provider value={onLinkOpen}>{children}</OpenChatLink.Provider>;
}

/**
 * Small native CommonMark renderer for the conversation surface.
 *
 * It deliberately covers the structures coding agents emit constantly (code,
 * headings, lists, quotes, links and inline emphasis) without putting a WebView
 * inside every message. Unknown Markdown remains readable text; the renderer
 * never hides content because a provider used syntax it does not know.
 */
export const ChatMarkdown = memo(function ChatMarkdown({ text, streaming = false }: { text: string; streaming?: boolean }) {
	const styles = useThemedStyles(makeStyles);
	return (
		<View style={styles.root}>
			{parseBlocks(text).map((block, index) => {
				if (block.kind === "code") return <CodeBlock key={index} {...block} streaming={streaming} />;
				if (block.kind === "image") return <MarkdownImage key={index} alt={block.alt} url={block.url} />;
				if (block.kind === "table") return <MarkdownTable key={index} headers={block.headers} rows={block.rows} />;
				if (block.kind === "rule") return <View key={index} style={styles.rule} />;
				if (block.kind === "list") {
					return (
						<View key={index} style={styles.list}>
							{block.items.map((item, itemIndex) => (
								<View key={itemIndex} style={styles.listRow}>
									<Text style={styles.marker}>{item.checked !== undefined ? (item.checked ? "☑" : "☐") : block.ordered ? `${itemIndex + 1}.` : "•"}</Text>
									<Text style={[styles.body, item.checked && styles.taskDone]}>{inline(item.text, styles)}</Text>
								</View>
							))}
						</View>
					);
				}
				if (block.kind === "heading") {
					return (
						<Text key={index} style={[styles.heading, block.level > 2 && styles.smallHeading]}>
							{inline(block.text, styles)}
						</Text>
					);
				}
				if (block.kind === "quote") {
					return (
						<View key={index} style={styles.quote}>
							<Text style={styles.quoteText}>{inline(block.text, styles)}</Text>
						</View>
					);
				}
				return (
					<Text key={index} selectable style={styles.body}>
						{inline(block.text, styles)}
					</Text>
				);
			})}
		</View>
	);
});

let preferredCodeWrap = false;

function MarkdownImage({ alt, url }: { alt: string; url: string }) {
	const styles = useThemedStyles(makeStyles);
	const [failed, setFailed] = useState(false);
	if (failed) return <Text style={styles.imageFallback}>Image unavailable: {alt || url}</Text>;
	return <View style={styles.imageCard}><Image accessibilityLabel={alt || "Conversation image"} accessibilityIgnoresInvertColors source={{ uri: url }} resizeMode="contain" onError={() => setFailed(true)} style={styles.image} />{alt ? <Text style={styles.imageCaption}>{alt}</Text> : null}</View>;
}

function MarkdownTable({ headers, rows }: { headers: string[]; rows: string[][] }) {
	const styles = useThemedStyles(makeStyles);
	return <ScrollView horizontal showsHorizontalScrollIndicator><View style={styles.table}><View style={[styles.tableRow, styles.tableHeader]}>{headers.map((cell, index) => <Text key={index} style={[styles.tableCell, styles.tableHeaderText]}>{cell}</Text>)}</View>{rows.map((row, rowIndex) => <View key={rowIndex} style={styles.tableRow}>{headers.map((_, cellIndex) => <Text key={cellIndex} style={styles.tableCell}>{row[cellIndex] ?? ""}</Text>)}</View>)}</View></ScrollView>;
}

function CodeBlock({ text, language, streaming }: { text: string; language?: string; streaming?: boolean }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [copied, setCopied] = useState(false);
	const [wrap, setWrap] = useState(preferredCodeWrap);
	return (
		<View style={styles.codeCard}>
			<View style={styles.codeHeader}>
				<Text style={styles.codeLanguage}>{language || "code"}</Text>
				<Pressable accessibilityRole="button" accessibilityLabel={wrap ? "Disable code wrapping" : "Wrap code"} accessibilityState={{ selected: wrap }} onPress={() => { haptics.select(); preferredCodeWrap = !wrap; setWrap(!wrap); }} style={styles.copyButton}><Feather name="corner-down-left" size={iconSize.xs} color={wrap ? t.accent : t.textTertiary} /><Text style={[styles.copyLabel, wrap && { color: t.accent }]}>Wrap</Text></Pressable>
				<Pressable
					accessibilityRole="button"
					accessibilityLabel="Copy code"
					onPress={() => {
						void Clipboard.setStringAsync(text);
						setCopied(true);
						haptics.success();
						setTimeout(() => setCopied(false), 1_400);
					}}
					style={styles.copyButton}
				>
					<Feather name={copied ? "check" : "copy"} size={iconSize.xs} color={copied ? t.green : t.textTertiary} />
					<Text style={[styles.copyLabel, copied && { color: t.green }]}>{copied ? "Copied" : "Copy"}</Text>
				</Pressable>
			</View>
			<ScrollView horizontal={!wrap} showsHorizontalScrollIndicator={!wrap} contentContainerStyle={wrap ? styles.codeWrap : undefined}>
				<HighlightedCodeText code={text} language={language} streaming={streaming} style={[styles.codeText, wrap && styles.codeTextWrap]} />
			</ScrollView>
		</View>
	);
}

function MarkdownLink({ url, label, style }: { url: string; label: string; style: StyleProp<TextStyle> }) {
	const open = useContext(OpenChatLink) ?? defaultOpenChatLink;
	return <Text accessibilityRole="link" style={style} onPress={() => { haptics.tap(); open(url); }}>{label}</Text>;
}

function inline(text: string, styles: ReturnType<typeof makeStyles>): ReactNode[] {
	const pattern = /(\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)|<(https?:\/\/[^\s>]+)>|`([^`]+)`|\*\*([^*]+)\*\*|__([^_]+)__|~~([^~]+)~~|\*([^*\n]+)\*|_([^_\n]+)_|(https?:\/\/[^\s<]+))/g;
	const nodes: ReactNode[] = [];
	let at = 0;
	let match: RegExpExecArray | null;
	while ((match = pattern.exec(text))) {
		if (match.index > at) nodes.push(text.slice(at, match.index));
		if ((match[2] && match[3]) || match[4] || match[11]) {
			const url = match[3] ?? match[4] ?? match[11];
			const label = match[2] ?? url;
			nodes.push(<MarkdownLink key={`${match.index}-link`} url={url} label={label} style={styles.link} />);
		} else if (match[5]) {
			nodes.push(<Text key={`${match.index}-code`} style={styles.inlineCode}>{match[5]}</Text>);
		} else if (match[6] || match[7]) {
			nodes.push(<Text key={`${match.index}-strong`} style={styles.strong}>{match[6] ?? match[7]}</Text>);
		} else if (match[8]) {
			nodes.push(<Text key={`${match.index}-strike`} style={styles.strike}>{match[8]}</Text>);
		} else {
			nodes.push(<Text key={`${match.index}-em`} style={styles.em}>{match[9] ?? match[10]}</Text>);
		}
		at = match.index + match[0].length;
	}
	if (at < text.length) nodes.push(<Fragment key={`${at}-tail`}>{text.slice(at)}</Fragment>);
	return nodes;
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		root: { gap: 10 },
		body: { fontFamily: "Geist_400Regular", color: t.textPrimary, fontSize: prose.fontSize, lineHeight: prose.lineHeight },
		heading: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.title3.fontSize, lineHeight: type.title3.lineHeight, fontWeight: "600", marginTop: space.xs },
		smallHeading: { fontFamily: "Geist_400Regular", fontSize: type.callout.fontSize, lineHeight: type.callout.lineHeight },
		strong: { fontFamily: "Geist_600SemiBold", fontWeight: "600" },
		em: { fontStyle: "italic" },
		strike: { textDecorationLine: "line-through" },
		link: { color: t.accent, textDecorationLine: "underline" },
		inlineCode: { color: t.accent, fontFamily: t.fontMono, fontSize: type.subheadline.fontSize, backgroundColor: t.bgSubtle },
		list: { gap: space.xxs },
		listRow: { flexDirection: "row", alignItems: "flex-start", gap: space.sm, paddingRight: space.xxs },
		// The marker sits on the same line as prose, so it borrows prose leading.
		marker: { fontFamily: "Geist_400Regular", width: 20, color: t.textTertiary, fontSize: type.subheadline.fontSize, lineHeight: prose.lineHeight, textAlign: "right" },
		taskDone: { color: t.textTertiary, textDecorationLine: "line-through" },
		quote: { borderLeftWidth: 2, borderLeftColor: t.borderStrong, paddingLeft: 12 },
		quoteText: { fontFamily: "Geist_400Regular", color: t.textSecondary, fontSize: type.subheadline.fontSize, lineHeight: prose.lineHeight, fontStyle: "italic" },
		rule: { height: 1, backgroundColor: t.borderSubtle, marginVertical: space.xxs },
		codeCard: { borderRadius: radius.md, borderCurve: "continuous", overflow: "hidden", borderWidth: 1, borderColor: t.borderSubtle, backgroundColor: t.bgColumn },
		codeHeader: { minHeight: 34, paddingHorizontal: space.md, flexDirection: "row", alignItems: "center", borderBottomWidth: 1, borderBottomColor: t.borderSubtle },
		codeLanguage: { flex: 1, color: t.textTertiary, ...microLabel, fontFamily: t.fontMono, textTransform: "uppercase" },
		copyButton: { flexDirection: "row", alignItems: "center", gap: space.xxs, paddingVertical: space.xs, paddingLeft: space.md },
		copyLabel: { fontFamily: "Geist_600SemiBold", color: t.textTertiary, fontSize: type.caption2.fontSize, fontWeight: "600" },
		// Code keeps looser leading than UI text at the same size.
		codeText: { color: t.textPrimary, fontFamily: t.fontMono, fontSize: type.footnote.fontSize, lineHeight: 20, padding: space.md },
		codeWrap: { flexGrow: 1 },
		codeTextWrap: { flexShrink: 1 },
		imageCard: { overflow: "hidden", borderRadius: radius.md, borderCurve: "continuous", borderWidth: 1, borderColor: t.borderSubtle, backgroundColor: t.bgColumn },
		image: { width: "100%", minHeight: 190, maxHeight: 420 },
		imageCaption: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.caption2.fontSize, paddingHorizontal: space.sm, paddingVertical: space.xs, borderTopWidth: 1, borderTopColor: t.borderSubtle },
		imageFallback: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.caption1.fontSize, fontStyle: "italic" },
		table: { borderWidth: 1, borderColor: t.borderSubtle, borderRadius: radius.sm, borderCurve: "continuous", overflow: "hidden" },
		tableRow: { flexDirection: "row", borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle },
		tableHeader: { borderTopWidth: 0, backgroundColor: t.bgSubtle },
		tableCell: { fontFamily: "Geist_400Regular", minWidth: 110, maxWidth: 260, color: t.textSecondary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, paddingHorizontal: space.sm, paddingVertical: space.xs, borderLeftWidth: StyleSheet.hairlineWidth, borderLeftColor: t.borderSubtle },
		tableHeaderText: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontWeight: "600" },
	});

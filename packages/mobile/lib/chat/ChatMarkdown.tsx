import { Feather } from "@expo/vector-icons";
import * as Clipboard from "expo-clipboard";
import * as Linking from "expo-linking";
import { Fragment, memo, useState, type ReactNode } from "react";
import { Image, Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { HighlightedCodeText } from "./HighlightedCodeText";
import { parseBlocks } from "./markdownBlocks";

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
				<Pressable accessibilityRole="button" accessibilityLabel={wrap ? "Disable code wrapping" : "Wrap code"} accessibilityState={{ selected: wrap }} onPress={() => { haptics.select(); preferredCodeWrap = !wrap; setWrap(!wrap); }} style={styles.copyButton}><Feather name="corner-down-left" size={13} color={wrap ? t.blue : t.textTertiary} /><Text style={[styles.copyLabel, wrap && { color: t.blue }]}>Wrap</Text></Pressable>
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
					<Feather name={copied ? "check" : "copy"} size={13} color={copied ? t.green : t.textTertiary} />
					<Text style={[styles.copyLabel, copied && { color: t.green }]}>{copied ? "Copied" : "Copy"}</Text>
				</Pressable>
			</View>
			<ScrollView horizontal={!wrap} showsHorizontalScrollIndicator={!wrap} contentContainerStyle={wrap ? styles.codeWrap : undefined}>
				<HighlightedCodeText code={text} language={language} streaming={streaming} style={[styles.codeText, wrap && styles.codeTextWrap]} />
			</ScrollView>
		</View>
	);
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
			nodes.push(
				<Text
					key={`${match.index}-link`}
					accessibilityRole="link"
					style={styles.link}
					onPress={() => { haptics.tap(); void Linking.openURL(url).catch(() => haptics.error()); }}
				>
					{label}
				</Text>,
			);
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
		body: { color: t.textPrimary, fontSize: 16, lineHeight: 24 },
		heading: { color: t.textPrimary, fontSize: 19, lineHeight: 25, fontWeight: "700", marginTop: 6 },
		smallHeading: { fontSize: 16, lineHeight: 22 },
		strong: { fontWeight: "700" },
		em: { fontStyle: "italic" },
		strike: { textDecorationLine: "line-through" },
		link: { color: t.blue, textDecorationLine: "underline" },
		inlineCode: { color: t.blue, fontFamily: t.fontMono, fontSize: 14, backgroundColor: t.bgSubtle },
		list: { gap: 5 },
		listRow: { flexDirection: "row", alignItems: "flex-start", gap: 8, paddingRight: 4 },
		marker: { width: 20, color: t.textTertiary, fontSize: 15, lineHeight: 23, textAlign: "right" },
		taskDone: { color: t.textTertiary, textDecorationLine: "line-through" },
		quote: { borderLeftWidth: 2, borderLeftColor: t.borderStrong, paddingLeft: 12 },
		quoteText: { color: t.textSecondary, fontSize: 15, lineHeight: 23, fontStyle: "italic" },
		rule: { height: 1, backgroundColor: t.borderSubtle, marginVertical: 4 },
		codeCard: { borderRadius: 12, overflow: "hidden", borderWidth: 1, borderColor: t.borderSubtle, backgroundColor: t.bgColumn },
		codeHeader: { minHeight: 34, paddingHorizontal: 11, flexDirection: "row", alignItems: "center", borderBottomWidth: 1, borderBottomColor: t.borderSubtle },
		codeLanguage: { flex: 1, color: t.textTertiary, fontFamily: t.fontMono, fontSize: 11, textTransform: "uppercase" },
		copyButton: { flexDirection: "row", alignItems: "center", gap: 5, paddingVertical: 7, paddingLeft: 12 },
		copyLabel: { color: t.textTertiary, fontSize: 11, fontWeight: "600" },
		codeText: { color: t.textPrimary, fontFamily: t.fontMono, fontSize: 13, lineHeight: 20, padding: 12 },
		codeWrap: { flexGrow: 1 },
		codeTextWrap: { flexShrink: 1 },
		imageCard: { overflow: "hidden", borderRadius: 12, borderWidth: 1, borderColor: t.borderSubtle, backgroundColor: t.bgColumn },
		image: { width: "100%", minHeight: 190, maxHeight: 420 },
		imageCaption: { color: t.textTertiary, fontSize: 10, paddingHorizontal: 10, paddingVertical: 7, borderTopWidth: 1, borderTopColor: t.borderSubtle },
		imageFallback: { color: t.textTertiary, fontSize: 12, fontStyle: "italic" },
		table: { borderWidth: 1, borderColor: t.borderSubtle, borderRadius: 8, overflow: "hidden" },
		tableRow: { flexDirection: "row", borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle },
		tableHeader: { borderTopWidth: 0, backgroundColor: t.bgSubtle },
		tableCell: { minWidth: 110, maxWidth: 260, color: t.textSecondary, fontSize: 12, lineHeight: 17, paddingHorizontal: 9, paddingVertical: 7, borderLeftWidth: StyleSheet.hairlineWidth, borderLeftColor: t.borderSubtle },
		tableHeaderText: { color: t.textPrimary, fontWeight: "700" },
	});

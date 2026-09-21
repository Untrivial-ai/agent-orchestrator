import { Feather } from "@expo/vector-icons";
import { FlatList, Pressable, StyleSheet, Text, View } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { SheetHeader } from "../ui";
import type { ConversationActionsEntry } from "./chatSheetRegistry";
import { contextReadout } from "./conversationChrome";
import { conversationMenuSections, type ConversationMenuAction } from "./conversationMenuModel";
import { can } from "./types";

export function ConversationActionsSheet({ entry, onAction }: { entry: ConversationActionsEntry; onAction(action: () => void): void }) {
	const styles = useThemedStyles(makeStyles);
	const turnInFlight = entry.snapshot.turns.some((turn) => turn.state === "running" || turn.state === "queued");
	const sections = conversationMenuSections({
		canRename: can(entry.snapshot, "rename"),
		canPin: entry.canPin,
		canCompact: entry.compactSupported,
		canReloadMcp: entry.mcpReloadSupported,
		canDelete: entry.canDelete,
	});
	const context = contextReadout(entry.snapshot.usage);

	const details = (action: ConversationMenuAction) => {
		switch (action) {
			case "shell": return { icon: "terminal" as const, label: entry.openingShell ? "Opening shell…" : "Open worktree shell", disabled: entry.openingShell, run: entry.onOpenShell };
			case "preview": return { icon: "globe" as const, label: "Open preview", run: entry.onPreview };
			case "pull_requests": return { icon: "git-pull-request" as const, label: "Pull requests", run: entry.onPullRequests };
			case "map": return { icon: "list" as const, label: "Conversation history", run: entry.onMap };
			case "refresh": return { icon: "refresh-cw" as const, label: entry.refreshing ? "Refreshing conversation…" : "Refresh conversation", disabled: entry.refreshing, run: entry.onRefresh };
			case "settings": return { icon: "sliders" as const, label: "Turn settings", value: entry.snapshot.settings.model || "Default", run: entry.onSettings };
			case "rename": return { icon: "edit-2" as const, label: "Rename conversation", run: entry.onRename };
			case "pin": return { icon: "bookmark" as const, label: entry.pinned ? "Unpin worker" : "Pin worker", run: entry.onTogglePin };
			case "compact": return { icon: "archive" as const, label: entry.compacting ? "Compacting history…" : "Compact history", disabled: turnInFlight || entry.compacting, run: entry.onCompact };
			case "terminal_ui": return { icon: "repeat" as const, label: entry.interfaceSwitching ? "Switching interface…" : "Open Terminal UI", hint: !entry.interfaceSupported ? entry.interfaceReason || "This agent does not support a compatible handoff" : undefined, disabled: !entry.interfaceSupported || entry.interfaceSwitching, run: entry.onSwitchInterface };
			case "delete": return { icon: "trash-2" as const, label: "Delete session", hint: "Terminates the agent. Conversation and worktree are kept.", destructive: true, run: entry.onDelete };
			case "reload_mcp": return { icon: "tool" as const, label: entry.mcpReloading ? "Reloading MCP servers…" : "Reload MCP servers", disabled: turnInFlight || entry.mcpReloading, run: entry.onReload };
		}
	};

	return <FlatList
		style={styles.list}
		contentContainerStyle={styles.content}
		data={sections}
		keyExtractor={(section) => section.title}
		nestedScrollEnabled
		keyboardShouldPersistTaps="handled"
		ListHeaderComponent={<SheetHeader title={entry.snapshot.title || "Untitled conversation"} subtitle={`Session · ${entry.sessionTitle}`} />}
		renderItem={({ item: section }) => <View style={styles.section}>
				<Text style={styles.sectionTitle}>{section.title}</Text>
				<View style={styles.group}>{section.actions.map((action, index) => {
						const item = details(action);
						return <ActionRow key={action} {...item} divider={index < section.actions.length - 1} onPress={() => onAction(item.run)} />;
					})}</View>
			</View>}
		ListFooterComponent={entry.snapshot.usage || entry.snapshot.rateLimits ? <View style={styles.usage}>
				<Text style={styles.sectionTitle}>Context & usage</Text>
				{entry.snapshot.usage ? <><Text style={styles.usageText}>{formatTokens(entry.snapshot.usage.contextUsed)} / {formatTokens(entry.snapshot.usage.contextWindow)} context · {formatTokens(entry.snapshot.usage.inputTokens)} in · {formatTokens(entry.snapshot.usage.outputTokens)} out{entry.snapshot.usage.cost != null ? ` · ${entry.snapshot.usage.currency || "$"}${entry.snapshot.usage.cost.toFixed(4)}` : ""}</Text>{context?.fillPercent !== undefined ? <View style={styles.contextTrack}><View style={[styles.contextFill, { width: `${context.fillPercent}%` }]} /></View> : null}</> : null}
				{entry.snapshot.rateLimits ? <Text style={styles.usageText}>{entry.snapshot.rateLimits.planLabel || "Primary limit"} · {Math.round(entry.snapshot.rateLimits.primaryUsedPercent)}% used{formatReset(entry.snapshot.rateLimits.primaryResetsInSeconds)}</Text> : null}
			</View> : null}
	/>;
}

function ActionRow({ icon, label, hint, value, disabled, destructive, divider, onPress }: { icon: keyof typeof Feather.glyphMap; label: string; hint?: string; value?: string; disabled?: boolean; destructive?: boolean; divider: boolean; onPress(): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <Pressable accessibilityRole="button" accessibilityState={{ disabled }} disabled={disabled} onPress={() => { destructive ? haptics.warning() : haptics.tap(); onPress(); }} style={({ pressed }) => [styles.row, divider && styles.rowDivider, pressed && styles.rowPressed, disabled && { opacity: 0.42 }]}>
		<Feather name={icon} size={18} color={destructive ? t.red : t.textSecondary} style={styles.rowIcon} />
		<View style={{ flex: 1 }}><Text style={[styles.rowLabel, destructive && { color: t.red }]}>{label}</Text>{hint ? <Text style={styles.rowHint}>{hint}</Text> : null}</View>
		{value ? <Text numberOfLines={1} style={styles.rowValue}>{value}</Text> : null}
		<Feather name="chevron-right" size={16} color={t.textFaint} />
	</Pressable>;
}

function formatTokens(value: number): string { return value >= 1_000 ? `${(value / 1_000).toFixed(value >= 10_000 ? 0 : 1)}k` : String(value); }
function formatReset(seconds?: number): string { if (seconds === undefined || seconds < 0) return ""; if (seconds < 60) return ` · resets in ${Math.ceil(seconds)}s`; if (seconds < 3600) return ` · resets in ${Math.ceil(seconds / 60)}m`; return ` · resets in ${Math.ceil(seconds / 3600)}h`; }

const makeStyles = (t: Theme) => StyleSheet.create({
	list: { flex: 1, backgroundColor: t.bgBase },
	content: { paddingHorizontal: 20, paddingTop: 22, paddingBottom: 32 },
	section: { marginTop: 18 },
	sectionTitle: { color: t.textTertiary, fontSize: 12, fontWeight: "600", marginBottom: 5 },
	group: { overflow: "hidden", borderRadius: 16, borderCurve: "continuous", backgroundColor: t.bgElevated },
	row: { minHeight: 52, flexDirection: "row", alignItems: "center", gap: 11, paddingHorizontal: 14, paddingVertical: 9 },
	rowDivider: { borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: t.borderSubtle },
	rowPressed: { opacity: 0.58 },
	rowIcon: { width: 22, textAlign: "center" },
	rowLabel: { color: t.textPrimary, fontSize: 15, fontWeight: "500" },
	rowHint: { color: t.textTertiary, fontSize: 11, lineHeight: 15, marginTop: 2 },
	rowValue: { maxWidth: 110, color: t.textTertiary, fontSize: 13 },
	usage: { marginTop: 25, paddingTop: 16, borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle, gap: 7 },
	usageText: { color: t.textTertiary, fontSize: 11, lineHeight: 16 },
	contextTrack: { height: 4, borderRadius: 2, backgroundColor: t.bgSubtle, overflow: "hidden" },
	contextFill: { height: 4, borderRadius: 2, backgroundColor: t.blue },
});

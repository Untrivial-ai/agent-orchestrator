import { Feather } from "../icons";
import { ActivityIndicator, Pressable, ScrollView, StyleSheet, Switch, Text, View } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { SheetHeader } from "../ui";
import type { ChatConfigOption, ChatModel, ConversationSnapshot, TurnSettings } from "./types";
import { can } from "./types";
import { iconSize, microLabel, space, type } from "../tokens";

const APPROVALS = [
	{ id: "default", label: "Default", hint: "The worktree is the safety boundary" },
	{ id: "accept-edits", label: "Ask outside worktree", hint: "Edits here are allowed; anything else asks" },
	{ id: "auto", label: "Ask when unsure", hint: "The agent decides when to check with you" },
	{ id: "bypass-permissions", label: "Never ask", hint: "No approvals or sandbox prompts" },
] as const;

export function ChatSettingsSheet({
	snapshot,
	models,
	options,
	disabled,
	refreshing,
	error,
	onRefresh,
	onSettings,
	onOption,
}: {
	snapshot: ConversationSnapshot;
	models: ChatModel[];
	options: ChatConfigOption[];
	disabled?: boolean;
	refreshing?: boolean;
	error?: string;
	onRefresh(): void;
	onSettings(settings: TurnSettings): void;
	onOption(id: string, value: { value: string } | { enabled: boolean }): void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const selected = models.find((model) => model.id === snapshot.settings.model) ?? models.find((model) => model.default);
	const efforts = selected?.efforts ?? [];
	const usesProviderOptions = can(snapshot, "config_options");
	const hasProviderModel = options.some(
		(option) => option.category === "model" || option.id === "model" || option.id === "agent",
	);
	const hasProviderMode = options.some(
		(option) => option.category === "mode" || option.id === "mode");
	return (
		<ScrollView style={styles.screen} contentContainerStyle={styles.content}>
			<SheetHeader title="Turn settings" subtitle="Changes apply to the next message." right={<Pressable accessibilityRole="button" accessibilityLabel="Refresh turn settings" disabled={refreshing} onPress={() => { haptics.tap(); onRefresh(); }} style={styles.refresh}>{refreshing ? <ActivityIndicator size="small" color={t.accent} /> : <><Feather name="refresh-cw" size={iconSize.xs} color={t.accent} /><Text style={styles.refreshText}>Refresh</Text></>}</Pressable>} />
					{error ? <View accessibilityRole="alert" style={styles.error}><Feather name="alert-circle" size={iconSize.sm} color={t.red} /><Text style={styles.errorText}>{error}</Text></View> : null}
					{snapshot.modelReroute ? <View style={styles.reroute}><Feather name="shuffle" size={iconSize.sm} color={t.amber} /><View style={{ flex: 1 }}><Text style={styles.rerouteTitle}>Currently answered by {snapshot.modelReroute.toModel}</Text><Text style={styles.rerouteCopy}>{snapshot.modelReroute.fromModel ? `${snapshot.modelReroute.fromModel} was requested. ` : ""}{snapshot.modelReroute.reason || "The provider selected a fallback model for this conversation."}</Text></View></View> : null}
					{(!usesProviderOptions || !hasProviderModel) && models.length ? <SettingsSection icon="cpu" title="Model">
						{models.map((model) => <Choice key={model.id} label={model.displayName} hint={model.description || (model.default ? "Provider default" : undefined)} selected={model.id === selected?.id} disabled={disabled} onPress={() => onSettings({ ...snapshot.settings, model: model.id, reasoningEffort: undefined })} />)}
					</SettingsSection> : null}
					{(!usesProviderOptions || !hasProviderModel) && efforts.length ? <SettingsSection icon="activity" title="Reasoning effort">
						{efforts.map((effort) => <Choice key={effort} label={capitalize(effort)} selected={effort === (snapshot.settings.reasoningEffort ?? selected?.defaultEffort)} disabled={disabled} onPress={() => onSettings({ ...snapshot.settings, reasoningEffort: effort })} />)}
					</SettingsSection> : null}
					{(!usesProviderOptions || !hasProviderMode) ? <SettingsSection icon="shield" title="Approvals">
						{APPROVALS.map((mode) => <Choice key={mode.id} label={mode.label} hint={mode.hint} selected={mode.id === (snapshot.settings.approvalMode ?? "default")} disabled={disabled} onPress={() => onSettings({ ...snapshot.settings, approvalMode: mode.id })} />)}
					</SettingsSection> : null}
					{options.map((option) => <SettingsSection key={option.id} icon={configOptionIcon(option)} title={option.name} description={option.description}>
						{option.type === "boolean" ? <View style={styles.switchRow}><Text style={styles.choiceLabel}>{option.currentBoolean ? "On" : "Off"}</Text><Switch disabled={disabled} value={Boolean(option.currentBoolean)} onValueChange={(enabled) => onOption(option.id, { enabled })} trackColor={{ true: t.green }} /></View> : <GroupedChoices option={option} disabled={disabled} onOption={onOption} />}
					</SettingsSection>)}
					{usesProviderOptions && options.length === 0 ? <Text style={styles.empty}>The provider has not advertised any turn controls yet.</Text> : null}
		</ScrollView>
	);
}

function GroupedChoices({ option, disabled, onOption }: { option: ChatConfigOption; disabled?: boolean; onOption(id: string, value: { value: string }): void }) {
	const styles = useThemedStyles(makeStyles);
	const groups = new Map<string, typeof option.choices>();
	for (const choice of option.choices) {
		const key = choice.groupName || choice.group || "";
		groups.set(key, [...(groups.get(key) ?? []), choice]);
	}
	return <>{[...groups.entries()].map(([group, choices]) => <View key={group || "default"}>{group ? <Text style={styles.groupLabel}>{group}</Text> : null}{choices.map((choice) => <Choice key={choice.value} label={choice.name} hint={choice.description} selected={choice.value === option.currentValue} disabled={disabled} onPress={() => onOption(option.id, { value: choice.value })} />)}</View>)}</>;
}

function SettingsSection({ icon, title, description, children }: { icon: keyof typeof Feather.glyphMap; title: string; description?: string; children: React.ReactNode }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <View style={styles.section}><View style={styles.sectionTitle}><Feather name={icon} size={iconSize.sm} color={t.textTertiary} /><Text style={styles.sectionLabel}>{title}</Text></View>{description ? <Text style={styles.sectionDescription}>{description}</Text> : null}<View style={styles.group}>{children}</View></View>;
}

function Choice({ label, hint, selected, disabled, onPress }: { label: string; hint?: string; selected: boolean; disabled?: boolean; onPress(): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <Pressable accessibilityRole="radio" accessibilityState={{ selected, disabled }} disabled={disabled} onPress={() => { haptics.select(); onPress(); }} style={({ pressed }) => [styles.choice, pressed && { backgroundColor: t.bgSubtle }]}><View style={{ flex: 1 }}><Text style={[styles.choiceLabel, selected && { color: t.accent }]}>{label}</Text>{hint ? <Text style={styles.choiceHint}>{hint}</Text> : null}</View>{selected ? <Feather name="check" size={iconSize.sm} color={t.accent} /> : null}</Pressable>;
}

function capitalize(value: string): string { return value ? value[0].toUpperCase() + value.slice(1) : value; }
function configOptionIcon(option: ChatConfigOption): keyof typeof Feather.glyphMap { if (option.id === "fast") return "zap"; if (option.id === "agent") return "user"; return option.category === "model" ? "cpu" : option.category === "thought_level" ? "activity" : option.category === "mode" ? "shield" : "sliders"; }

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgSurface },
	content: { padding: space.lg, paddingBottom: 42, gap: space.xl },
	refresh: { flexDirection: "row", alignItems: "center", gap: space.xxs },
	refreshText: { fontFamily: "Geist_600SemiBold", color: t.accent, fontSize: type.footnote.fontSize, fontWeight: "600" },
	error: { flexDirection: "row", alignItems: "flex-start", gap: space.sm, borderRadius: 8, borderCurve: "continuous", backgroundColor: t.tintRed, padding: space.sm },
	errorText: { fontFamily: "Geist_400Regular", flex: 1, color: t.red, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight },
	reroute: { flexDirection: "row", alignItems: "flex-start", gap: space.sm, borderRadius: 12, borderCurve: "continuous", borderWidth: 1, borderColor: t.borderDefault, backgroundColor: t.tintAmber, padding: space.md },
	rerouteTitle: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.caption1.fontSize, fontWeight: "600" },
	rerouteCopy: { fontFamily: "Geist_400Regular", color: t.textSecondary, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight, marginTop: space.hair },
	section: { gap: space.xs },
	sectionTitle: { flexDirection: "row", alignItems: "center", gap: space.sm },
	sectionLabel: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.footnote.fontSize, fontWeight: "600" },
	sectionDescription: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight },
	group: { backgroundColor: t.bgElevated, borderWidth: 1, borderColor: t.borderSubtle, borderRadius: 12, borderCurve: "continuous", overflow: "hidden" },
	groupLabel: { color: t.textTertiary, backgroundColor: t.bgSubtle, paddingHorizontal: space.md, paddingVertical: space.xs, ...microLabel, textTransform: "uppercase" },
	empty: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, textAlign: "center", paddingVertical: space.xxl },
	choice: { minHeight: 48, flexDirection: "row", alignItems: "center", gap: space.sm, paddingHorizontal: space.md, paddingVertical: space.sm, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: t.borderSubtle },
	choiceLabel: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.footnote.fontSize, fontWeight: "600" },
	choiceHint: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight, marginTop: space.hair },
	switchRow: { minHeight: 49, flexDirection: "row", alignItems: "center", justifyContent: "space-between", paddingHorizontal: space.md },
});

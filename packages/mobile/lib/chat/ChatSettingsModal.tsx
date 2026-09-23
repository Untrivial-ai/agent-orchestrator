import { Feather } from "@expo/vector-icons";
import { ActivityIndicator, Pressable, ScrollView, StyleSheet, Switch, Text, View } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { SheetHeader } from "../ui";
import type { ChatConfigOption, ChatModel, ConversationSnapshot, TurnSettings } from "./types";
import { can } from "./types";

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
			<SheetHeader title="Turn settings" subtitle="Changes apply to the next message." right={<Pressable accessibilityRole="button" accessibilityLabel="Refresh turn settings" disabled={refreshing} onPress={() => { haptics.tap(); onRefresh(); }} style={styles.refresh}>{refreshing ? <ActivityIndicator size="small" color={t.blue} /> : <><Feather name="refresh-cw" size={13} color={t.blue} /><Text style={styles.refreshText}>Refresh</Text></>}</Pressable>} />
					{error ? <View accessibilityRole="alert" style={styles.error}><Feather name="alert-circle" size={14} color={t.red} /><Text style={styles.errorText}>{error}</Text></View> : null}
					{snapshot.modelReroute ? <View style={styles.reroute}><Feather name="shuffle" size={14} color={t.amber} /><View style={{ flex: 1 }}><Text style={styles.rerouteTitle}>Currently answered by {snapshot.modelReroute.toModel}</Text><Text style={styles.rerouteCopy}>{snapshot.modelReroute.fromModel ? `${snapshot.modelReroute.fromModel} was requested. ` : ""}{snapshot.modelReroute.reason || "The provider selected a fallback model for this conversation."}</Text></View></View> : null}
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
						{option.type === "boolean" ? <View style={styles.switchRow}><Text style={styles.choiceLabel}>{option.currentBoolean ? "On" : "Off"}</Text><Switch disabled={disabled} value={Boolean(option.currentBoolean)} onValueChange={(enabled) => onOption(option.id, { enabled })} trackColor={{ true: t.blue }} /></View> : <GroupedChoices option={option} disabled={disabled} onOption={onOption} />}
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
	return <View style={styles.section}><View style={styles.sectionTitle}><Feather name={icon} size={14} color={t.textTertiary} /><Text style={styles.sectionLabel}>{title}</Text></View>{description ? <Text style={styles.sectionDescription}>{description}</Text> : null}<View style={styles.group}>{children}</View></View>;
}

function Choice({ label, hint, selected, disabled, onPress }: { label: string; hint?: string; selected: boolean; disabled?: boolean; onPress(): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <Pressable accessibilityRole="radio" accessibilityState={{ selected, disabled }} disabled={disabled} onPress={() => { haptics.select(); onPress(); }} style={({ pressed }) => [styles.choice, pressed && { backgroundColor: t.bgSubtle }]}><View style={{ flex: 1 }}><Text style={[styles.choiceLabel, selected && { color: t.blue }]}>{label}</Text>{hint ? <Text style={styles.choiceHint}>{hint}</Text> : null}</View>{selected ? <Feather name="check" size={16} color={t.blue} /> : null}</Pressable>;
}

function capitalize(value: string): string { return value ? value[0].toUpperCase() + value.slice(1) : value; }
function configOptionIcon(option: ChatConfigOption): keyof typeof Feather.glyphMap { if (option.id === "fast") return "zap"; if (option.id === "agent") return "user"; return option.category === "model" ? "cpu" : option.category === "thought_level" ? "activity" : option.category === "mode" ? "shield" : "sliders"; }

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgSurface },
	content: { padding: 16, paddingBottom: 42, gap: 22 },
	refresh: { flexDirection: "row", alignItems: "center", gap: 5 },
	refreshText: { color: t.blue, fontSize: 13, fontWeight: "600" },
	error: { flexDirection: "row", alignItems: "flex-start", gap: 8, borderRadius: 10, backgroundColor: t.tintRed, padding: 10 },
	errorText: { flex: 1, color: t.red, fontSize: 11, lineHeight: 16 },
	reroute: { flexDirection: "row", alignItems: "flex-start", gap: 9, borderRadius: 11, borderWidth: 1, borderColor: t.borderDefault, backgroundColor: t.tintAmber, padding: 11 },
	rerouteTitle: { color: t.textPrimary, fontSize: 12, fontWeight: "700" },
	rerouteCopy: { color: t.textSecondary, fontSize: 10, lineHeight: 14, marginTop: 2 },
	section: { gap: 7 },
	sectionTitle: { flexDirection: "row", alignItems: "center", gap: 8 },
	sectionLabel: { color: t.textPrimary, fontSize: 13, fontWeight: "700" },
	sectionDescription: { color: t.textTertiary, fontSize: 11, lineHeight: 16 },
	group: { backgroundColor: t.bgElevated, borderWidth: 1, borderColor: t.borderSubtle, borderRadius: 12, overflow: "hidden" },
	groupLabel: { color: t.textTertiary, backgroundColor: t.bgSubtle, paddingHorizontal: 13, paddingVertical: 6, fontSize: 9, fontWeight: "700", textTransform: "uppercase", letterSpacing: 0.7 },
	empty: { color: t.textTertiary, fontSize: 12, lineHeight: 18, textAlign: "center", paddingVertical: 28 },
	choice: { minHeight: 48, flexDirection: "row", alignItems: "center", gap: 10, paddingHorizontal: 13, paddingVertical: 9, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: t.borderSubtle },
	choiceLabel: { color: t.textPrimary, fontSize: 13, fontWeight: "600" },
	choiceHint: { color: t.textTertiary, fontSize: 11, lineHeight: 15, marginTop: 2 },
	switchRow: { minHeight: 49, flexDirection: "row", alignItems: "center", justifyContent: "space-between", paddingHorizontal: 13 },
});

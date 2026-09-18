import { Feather } from "@expo/vector-icons";
import { Host, Slider, Switch as NativeSwitch } from "@expo/ui";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { ActivityIndicator, Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles, useThemeState } from "../ThemeProvider";
import { SheetHeader } from "../ui";
import type { ChatConfigOption, ChatModel, ConversationSnapshot, TurnSettings } from "./types";
import { fastControlEnabled, fastControlValue, orderedProviderControls, providerTurnControlKind } from "./turnSettingsModel";
import { can } from "./types";
import { type, space } from "../tokens";

const APPROVALS = [
	{ id: "default", label: "Default", description: "The worktree remains the safety boundary" },
	{ id: "accept-edits", label: "Ask outside worktree", description: "Edits here are allowed; anything else asks" },
	{ id: "auto", label: "Ask when unsure", description: "The agent requests approval when it needs it" },
	{ id: "bypass-permissions", label: "Never ask", description: "No approval or sandbox prompts" },
] as const;

type Choice = { value: string; label: string; description?: string };
type OpenChoice = { title: string; value: string; items: Choice[]; onChange(value: string): void } | null;
type Props = {
	snapshot: ConversationSnapshot;
	models: ChatModel[];
	options: ChatConfigOption[];
	disabled?: boolean;
	refreshing?: boolean;
	error?: string;
	onRefresh(): void;
	onSettings(settings: TurnSettings): void;
	onOption(id: string, value: { value: string } | { enabled: boolean }): void;
};

export function ChatSettingsSheet({ snapshot, models, options, disabled, refreshing, error, onRefresh, onSettings, onOption }: Props) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [openChoice, setOpenChoice] = useState<OpenChoice>(null);
	const selected = models.find((model) => model.id === snapshot.settings.model) ?? models.find((model) => model.default);
	const usesProviderOptions = can(snapshot, "config_options");
	const providerControls = useMemo(() => orderedProviderControls([...options]), [options]);
	const modelOption = providerControls.find((option) => providerTurnControlKind(option) === "model");
	const effortOption = providerControls.find((option) => providerTurnControlKind(option) === "effort");
	const fastOption = providerControls.find((option) => providerTurnControlKind(option) === "fast");
	const permissionOption = providerControls.find((option) => providerTurnControlKind(option) === "permissions");
	const advancedOptions = providerControls.filter((option) => providerTurnControlKind(option) === "other");
	const modelChoices = modelOption
		? modelOption.choices.map((model) => ({ value: model.value, label: model.name, description: model.description }))
		: models.map((model) => ({ value: model.id, label: model.displayName, description: model.description || (model.default ? "Provider default" : undefined) }));
	const selectedModel = modelOption?.currentValue ?? selected?.id ?? modelChoices[0]?.value ?? "";
	const effortChoices = effortOption
		? effortOption.choices.map((effort) => ({ value: effort.value, label: capitalize(effort.name) }))
		: (selected?.efforts ?? []).map((effort) => ({ value: effort, label: capitalize(effort) }));
	const selectedEffort = effortOption?.currentValue ?? snapshot.settings.reasoningEffort ?? selected?.defaultEffort ?? effortChoices[0]?.value ?? "";
	const permissionChoices = permissionOption
		? permissionOption.choices.map((choice) => ({ value: choice.value, label: choice.name, description: choice.description }))
		: APPROVALS.map((item) => ({ value: item.id, label: item.label, description: item.description }));
	const selectedPermission = permissionOption?.currentValue ?? snapshot.settings.approvalMode ?? "default";
	const permissionDescription = permissionChoices.find((choice) => choice.value === selectedPermission)?.description;
	const choose = (title: string, value: string, items: Choice[], onChange: (value: string) => void) => {
		if (disabled) return;
		haptics.tap();
		setOpenChoice({ title, value, items, onChange });
	};

	// Choices open as a page inside this sheet, never as a second sheet over it:
	// Android stacked two grabbers and only the top one answered a swipe down.
	if (openChoice) return <ChoicePage choice={openChoice} onBack={() => setOpenChoice(null)} />;

	return <View style={[styles.screen, { backgroundColor: t.bgSurface }]}>
		<View style={styles.header}>
			<SheetHeader title="Turn settings" subtitle="Changes apply to the next message." right={<Pressable accessibilityRole="button" accessibilityLabel="Refresh turn settings" disabled={refreshing} onPress={() => { haptics.tap(); onRefresh(); }} style={styles.refresh}>
				{refreshing ? <ActivityIndicator size="small" color={t.accent} /> : <Feather name="refresh-cw" size={15} color={t.accent} />}
				<Text style={styles.refreshText}>Refresh</Text>
			</Pressable>} />
			{error ? <Notice color={t.red} background={t.tintRed} icon="alert-circle" text={error} /> : null}
			{snapshot.modelReroute ? <Notice color={t.amber} background={t.tintAmber} icon="shuffle" text={`Currently answered by ${snapshot.modelReroute.toModel}`} /> : null}
		</View>

		<ScrollView contentContainerStyle={styles.content} showsVerticalScrollIndicator={false}>
			{fastOption || modelChoices.length || effortChoices.length ? <SettingsGroup title="RESPONSE">
				{fastOption ? <FastModeRow option={fastOption} disabled={disabled} onOption={onOption} /> : null}
				{modelChoices.length ? <SettingRow icon="cpu" label="Model" value={modelChoices.find((choice) => choice.value === selectedModel)?.label ?? "Choose"} description="Choose the model for the next message" disabled={disabled} onPress={() => choose("Model", selectedModel, modelChoices, (model) => {
					if (modelOption) onOption(modelOption.id, { value: model });
					else onSettings({ ...snapshot.settings, model, reasoningEffort: undefined });
				})} /> : null}
				{effortChoices.length ? <EffortSlider choices={effortChoices} selected={selectedEffort} disabled={disabled} onChange={(reasoningEffort) => {
					if (effortOption) onOption(effortOption.id, { value: reasoningEffort });
					else onSettings({ ...snapshot.settings, reasoningEffort });
				}} /> : null}
			</SettingsGroup> : null}

			{permissionChoices.length ? <SettingsGroup title="PERMISSIONS">
				<SettingRow icon="shield" label="Permission mode" value={permissionChoices.find((choice) => choice.value === selectedPermission)?.label ?? "Default"} description={permissionDescription} disabled={disabled} onPress={() => choose("Permission mode", selectedPermission, permissionChoices, (value) => {
					if (permissionOption) onOption(permissionOption.id, { value });
					else onSettings({ ...snapshot.settings, approvalMode: value as TurnSettings["approvalMode"] });
				})} />
			</SettingsGroup> : null}

			{advancedOptions.length ? <SettingsGroup title="ADVANCED">
				{advancedOptions.map((option) => option.type === "boolean"
					? <ToggleRow key={option.id} label={option.name} description={option.description} value={Boolean(option.currentBoolean)} disabled={disabled} onChange={(enabled) => onOption(option.id, { enabled })} />
					: <SettingRow key={option.id} icon="sliders" label={option.name} value={choiceLabel(option)} description={option.description} disabled={disabled} onPress={() => choose(option.name, option.currentValue ?? option.choices[0]?.value ?? "", option.choices.map((choice) => ({ value: choice.value, label: choice.groupName || choice.group ? `${choice.groupName || choice.group} · ${choice.name}` : choice.name, description: choice.description })), (value) => onOption(option.id, { value }))} />,
				)}
			</SettingsGroup> : null}

			{usesProviderOptions && options.length === 0 && models.length === 0 ? <Text style={styles.empty}>No turn controls are available for this provider.</Text> : null}
		</ScrollView>
	</View>;
}

function SettingsGroup({ title, children }: { title: string; children: ReactNode }) {
	const styles = useThemedStyles(makeStyles);
	return <View style={styles.section}><Text style={styles.sectionTitle}>{title}</Text><View style={styles.group}>{children}</View></View>;
}

function SettingRow({ icon, label, value, description, disabled, onPress }: { icon: keyof typeof Feather.glyphMap; label: string; value: string; description?: string; disabled?: boolean; onPress(): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <Pressable accessibilityRole="button" accessibilityState={{ disabled }} disabled={disabled} onPress={onPress} style={({ pressed }) => [styles.row, pressed && styles.rowPressed, disabled && styles.disabled]}>
		<Feather name={icon} size={17} color={t.textSecondary} />
		<View style={styles.rowCopy}><Text numberOfLines={1} style={styles.rowLabel}>{label}</Text>{description ? <Text numberOfLines={2} style={styles.rowDescription}>{description}</Text> : null}</View>
		<Text numberOfLines={1} style={styles.rowValue}>{value}</Text>
		<Feather name="chevron-right" size={17} color={t.textFaint} />
	</Pressable>;
}

function FastModeRow({ option, disabled, onOption }: { option: ChatConfigOption; disabled?: boolean; onOption(id: string, value: { value: string } | { enabled: boolean }): void }) {
	const enabled = fastControlEnabled(option);
	const selectValue = fastControlValue(option, !enabled);
	const toggleDisabled = disabled || (option.type === "select" && !selectValue);
	return <ToggleRow label={option.name} description={option.description || "Faster responses on supported models"} value={enabled} disabled={toggleDisabled} onChange={(next) => {
		if (option.type === "boolean") onOption(option.id, { enabled: next });
		else {
			const value = fastControlValue(option, next);
			if (value) onOption(option.id, { value });
		}
	}} />;
}

function ToggleRow({ label, description, value, disabled, onChange }: { label: string; description?: string; value: boolean; disabled?: boolean; onChange(value: boolean): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const { scheme } = useThemeState();
	return <View style={[styles.row, disabled && styles.disabled]}><Feather name="zap" size={17} color={t.textSecondary} /><View style={styles.rowCopy}><Text style={styles.rowLabel}>{label}</Text>{description ? <Text numberOfLines={2} style={styles.rowDescription}>{description}</Text> : null}</View><Host style={styles.switchHost} colorScheme={scheme} seedColor={t.accent}><NativeSwitch value={value} disabled={disabled} onValueChange={(next) => { haptics.select(); onChange(next); }} /></Host></View>;
}

function EffortSlider({ choices, selected, disabled, onChange }: { choices: Choice[]; selected: string; disabled?: boolean; onChange(value: string): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const { scheme } = useThemeState();
	const selectedIndex = Math.max(0, choices.findIndex((choice) => choice.value === selected));
	const [index, setIndex] = useState(selectedIndex);

	useEffect(() => setIndex(selectedIndex), [selectedIndex]);
	useEffect(() => {
		const next = choices[index]?.value;
		if (!next || next === selected) return;
		const timer = setTimeout(() => { haptics.select(); onChange(next); }, 180);
		return () => clearTimeout(timer);
	}, [choices, index, onChange, selected]);

	return <View style={[styles.effort, disabled && styles.disabled]}>
		<View style={styles.effortHeader}><Feather name="activity" size={17} color={t.textSecondary} /><View style={styles.rowCopy}><Text style={styles.rowLabel}>Reasoning effort</Text><Text style={styles.rowDescription}>More effort can improve harder tasks</Text></View><Text style={styles.effortValue}>{choices[index]?.label}</Text></View>
		<Host style={styles.sliderHost} colorScheme={scheme} seedColor={t.accent}><Slider value={index} min={0} max={Math.max(0, choices.length - 1)} step={1} disabled={disabled} onValueChange={(value) => setIndex(Math.round(value))} testID="turn-settings-effort" /></Host>
		<View style={styles.effortLabels}>{choices.map((choice, choiceIndex) => <Text key={choice.value} style={[styles.effortLabel, choiceIndex === index && { color: t.accent }]}>{choice.label}</Text>)}</View>
	</View>;
}

function ChoicePage({ choice, onBack }: { choice: NonNullable<OpenChoice>; onBack(): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <View style={[styles.screen, { backgroundColor: t.bgSurface }]}>
		<Pressable accessibilityRole="button" accessibilityLabel={`Back to turn settings`} onPress={() => { haptics.tap(); onBack(); }} style={({ pressed }) => [styles.choiceBack, pressed && styles.rowPressed]}>
			<Feather name="chevron-left" size={20} color={t.textSecondary} />
			<Text style={styles.choiceTitle}>{choice.title}</Text>
		</Pressable>
		<ScrollView contentContainerStyle={styles.choiceList} showsVerticalScrollIndicator={false}><View style={styles.choiceCard}>{choice.items.map((item, index) => {
			const selected = item.value === choice.value;
			return <Pressable key={item.value} accessibilityRole="button" accessibilityState={{ selected }} onPress={() => { haptics.select(); choice.onChange(item.value); onBack(); }} style={[styles.choiceRow, index > 0 && styles.choiceDivider, selected && styles.choiceSelected]}>
				<View style={styles.choiceCopy}><Text style={[styles.choiceLabel, selected && styles.choiceLabelSelected]}>{item.label}</Text>{item.description ? <Text style={styles.choiceDescription}>{item.description}</Text> : null}</View>
				{selected ? <Feather name="check" size={20} color={t.accent} style={styles.choiceCheck} /> : null}
			</Pressable>;
		})}</View></ScrollView>
	</View>;
}

function Notice({ color, background, icon, text }: { color: string; background: string; icon: keyof typeof Feather.glyphMap; text: string }) {
	const styles = useThemedStyles(makeStyles);
	return <View accessibilityRole="alert" style={[styles.notice, { backgroundColor: background }]}><Feather name={icon} size={15} color={color} /><Text style={[styles.noticeText, { color }]}>{text}</Text></View>;
}

function choiceLabel(option: ChatConfigOption): string {
	const current = option.choices.find((choice) => choice.value === option.currentValue);
	return current?.name ?? option.currentValue ?? "Choose";
}

function capitalize(value: string): string { return value ? value[0].toUpperCase() + value.slice(1) : value; }

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1 },
	header: { paddingHorizontal: space.lg, paddingTop: space.md, gap: space.sm },
	refresh: { minHeight: 36, flexDirection: "row", alignItems: "center", gap: space.xs, paddingHorizontal: space.xxs },
	refreshText: { color: t.accent, fontSize: type.footnote.fontSize, fontWeight: "700" },
	notice: { minHeight: 38, flexDirection: "row", alignItems: "center", gap: space.sm, borderRadius: 12, paddingHorizontal: space.md, paddingVertical: space.sm },
	noticeText: { flex: 1, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight },
	content: { paddingHorizontal: space.lg, paddingTop: space.md, paddingBottom: space.xxl, gap: space.lg },
	section: { gap: space.xs },
	sectionTitle: { paddingHorizontal: space.xxs, color: t.textTertiary, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight, letterSpacing: 1.05, fontWeight: "700" },
	group: { overflow: "hidden", borderRadius: 16, borderCurve: "continuous", backgroundColor: t.bgElevated },
	row: { minHeight: 58, flexDirection: "row", alignItems: "center", gap: space.md, paddingHorizontal: space.md, paddingVertical: space.sm },
	rowPressed: { backgroundColor: t.bgElevatedHover },
	disabled: { opacity: 0.45 },
	rowCopy: { flex: 1, minWidth: 0, gap: space.none },
	rowLabel: { color: t.textPrimary, fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight, fontWeight: "600" },
	rowDescription: { color: t.textTertiary, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight },
	rowValue: { maxWidth: "42%", color: t.textSecondary, fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight },
	switchHost: { width: 54, height: 34 },
	effort: { paddingHorizontal: space.md, paddingTop: space.md, paddingBottom: space.sm, gap: space.hair },
	// Match the standard settings row's icon-to-copy spacing so the Model and
	// Reasoning effort labels share the same text column.
	effortHeader: { flexDirection: "row", alignItems: "center", gap: space.md },
	effortValue: { color: t.accent, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, fontWeight: "700" },
	// Expo UI maps this Host to Compose on Android, where percentage widths are
	// not valid native layout values. Let the parent stretch it instead.
	sliderHost: { height: 36, alignSelf: "stretch" },
	effortLabels: { flexDirection: "row", justifyContent: "space-between", gap: space.xxs },
	effortLabel: { flex: 1, color: t.textTertiary, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight, textAlign: "center" },
	empty: { color: t.textTertiary, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, paddingHorizontal: space.xxs },
	choiceBack: { minHeight: 52, flexDirection: "row", alignItems: "center", gap: space.xs, paddingHorizontal: space.md },
	choiceTitle: { color: t.textPrimary, fontSize: type.title3.fontSize, lineHeight: type.title3.lineHeight, fontWeight: "700" },
	choiceList: { paddingHorizontal: space.lg, paddingBottom: space.xl },
	choiceCard: { borderRadius: 16, overflow: "hidden", backgroundColor: t.bgElevated },
	choiceRow: { minHeight: 52, flexDirection: "row", alignItems: "flex-start", gap: space.sm, paddingHorizontal: space.lg, paddingVertical: space.sm },
	choiceDivider: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle },
	choiceSelected: { backgroundColor: t.accentTint },
	choiceCopy: { flex: 1, minWidth: 0, gap: space.none },
	choiceLabel: { color: t.textPrimary, fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight },
	choiceDescription: { color: t.textTertiary, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight },
	choiceCheck: { alignSelf: "center" },
	choiceLabelSelected: { color: t.accent, fontWeight: "700" },
});

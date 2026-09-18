import { Feather } from "@expo/vector-icons";
import { Host, Slider, Switch as NativeSwitch } from "@expo/ui";
import BottomSheet, { BottomSheetView } from "@expo/ui/community/bottom-sheet";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { ActivityIndicator, Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles, useThemeState } from "../ThemeProvider";
import { SheetHeader } from "../ui";
import type { ChatConfigOption, ChatModel, ConversationSnapshot, TurnSettings } from "./types";
import { fastControlEnabled, fastControlValue, orderedProviderControls, providerTurnControlKind } from "./turnSettingsModel";
import { can } from "./types";

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

	return <View style={[styles.screen, { backgroundColor: t.bgSurface }]}>
		<View style={styles.header}>
			<SheetHeader title="Turn settings" subtitle="Changes apply to the next message." right={<Pressable accessibilityRole="button" accessibilityLabel="Refresh turn settings" disabled={refreshing} onPress={() => { haptics.tap(); onRefresh(); }} style={styles.refresh}>
				{refreshing ? <ActivityIndicator size="small" color={t.blue} /> : <Feather name="refresh-cw" size={14} color={t.blue} />}
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
		<OptionSheet choice={openChoice} onDismiss={() => setOpenChoice(null)} />
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
		<Feather name={icon} size={18} color={t.textSecondary} />
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
	return <View style={[styles.row, disabled && styles.disabled]}><Feather name="zap" size={18} color={t.textSecondary} /><View style={styles.rowCopy}><Text style={styles.rowLabel}>{label}</Text>{description ? <Text numberOfLines={2} style={styles.rowDescription}>{description}</Text> : null}</View><Host style={styles.switchHost} colorScheme={scheme} seedColor={t.blue}><NativeSwitch value={value} disabled={disabled} onValueChange={(next) => { haptics.select(); onChange(next); }} /></Host></View>;
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
		<View style={styles.effortHeader}><Feather name="activity" size={18} color={t.textSecondary} /><View style={styles.rowCopy}><Text style={styles.rowLabel}>Reasoning effort</Text><Text style={styles.rowDescription}>More effort can improve harder tasks</Text></View><Text style={styles.effortValue}>{choices[index]?.label}</Text></View>
		<Host style={styles.sliderHost} colorScheme={scheme} seedColor={t.blue}><Slider value={index} min={0} max={Math.max(0, choices.length - 1)} step={1} disabled={disabled} onValueChange={(value) => setIndex(Math.round(value))} testID="turn-settings-effort" /></Host>
		<View style={styles.effortLabels}>{choices.map((choice, choiceIndex) => <Text key={choice.value} style={[styles.effortLabel, choiceIndex === index && { color: t.blue }]}>{choice.label}</Text>)}</View>
	</View>;
}

function OptionSheet({ choice, onDismiss }: { choice: OpenChoice; onDismiss(): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const rows = useMemo(() => choice?.items ?? [], [choice]);
	return <BottomSheet
		index={choice ? 0 : -1}
		enablePanDownToClose
		enableDynamicSizing
		backgroundStyle={{ backgroundColor: t.bgSurface }}
		onClose={onDismiss}
	>
		<BottomSheetView style={styles.choiceSheet}>
				<Text style={styles.choiceTitle}>{choice?.title}</Text>
				<ScrollView style={styles.choiceList}>{rows.map((item, index) => {
					const selected = item.value === choice?.value;
					return <Pressable key={item.value} accessibilityRole="button" accessibilityState={{ selected }} onPress={() => { haptics.select(); choice?.onChange(item.value); onDismiss(); }} style={[styles.choiceRow, index > 0 && styles.choiceDivider, selected && styles.choiceSelected]}>
						<View style={styles.choiceCopy}><Text style={[styles.choiceLabel, selected && styles.choiceLabelSelected]}>{item.label}</Text>{item.description ? <Text style={styles.choiceDescription}>{item.description}</Text> : null}</View>
						{selected ? <Feather name="check" size={19} color={t.blue} style={styles.choiceCheck} /> : null}
					</Pressable>;
				})}</ScrollView>
		</BottomSheetView>
	</BottomSheet>;
}

function Notice({ color, background, icon, text }: { color: string; background: string; icon: keyof typeof Feather.glyphMap; text: string }) {
	const styles = useThemedStyles(makeStyles);
	return <View accessibilityRole="alert" style={[styles.notice, { backgroundColor: background }]}><Feather name={icon} size={14} color={color} /><Text style={[styles.noticeText, { color }]}>{text}</Text></View>;
}

function choiceLabel(option: ChatConfigOption): string {
	const current = option.choices.find((choice) => choice.value === option.currentValue);
	return current?.name ?? option.currentValue ?? "Choose";
}

function capitalize(value: string): string { return value ? value[0].toUpperCase() + value.slice(1) : value; }

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1 },
	header: { paddingHorizontal: 16, paddingTop: 12, gap: 8 },
	refresh: { minHeight: 36, flexDirection: "row", alignItems: "center", gap: 6, paddingHorizontal: 4 },
	refreshText: { color: t.blue, fontSize: 13, fontWeight: "700" },
	notice: { minHeight: 38, flexDirection: "row", alignItems: "center", gap: 8, borderRadius: 12, paddingHorizontal: 11, paddingVertical: 8 },
	noticeText: { flex: 1, fontSize: 12, lineHeight: 17 },
	content: { paddingHorizontal: 16, paddingTop: 14, paddingBottom: 28, gap: 18 },
	section: { gap: 7 },
	sectionTitle: { paddingHorizontal: 4, color: t.textTertiary, fontSize: 11, lineHeight: 15, letterSpacing: 1.05, fontWeight: "700" },
	group: { overflow: "hidden", borderRadius: 16, borderCurve: "continuous", backgroundColor: t.bgElevated },
	row: { minHeight: 58, flexDirection: "row", alignItems: "center", gap: 11, paddingHorizontal: 14, paddingVertical: 8 },
	rowPressed: { backgroundColor: t.bgElevatedHover },
	disabled: { opacity: 0.45 },
	rowCopy: { flex: 1, minWidth: 0, gap: 0 },
	rowLabel: { color: t.textPrimary, fontSize: 15, lineHeight: 19, fontWeight: "600" },
	rowDescription: { color: t.textTertiary, fontSize: 11, lineHeight: 14 },
	rowValue: { maxWidth: "42%", color: t.textSecondary, fontSize: 14, lineHeight: 19 },
	switchHost: { width: 54, height: 34 },
	effort: { paddingHorizontal: 14, paddingTop: 11, paddingBottom: 9, gap: 3 },
	// Match the standard settings row's icon-to-copy spacing so the Model and
	// Reasoning effort labels share the same text column.
	effortHeader: { flexDirection: "row", alignItems: "center", gap: 11 },
	effortValue: { color: t.blue, fontSize: 13, lineHeight: 18, fontWeight: "700" },
	// Expo UI maps this Host to Compose on Android, where percentage widths are
	// not valid native layout values. Let the parent stretch it instead.
	sliderHost: { height: 36, alignSelf: "stretch" },
	effortLabels: { flexDirection: "row", justifyContent: "space-between", gap: 4 },
	effortLabel: { flex: 1, color: t.textTertiary, fontSize: 9, lineHeight: 13, textAlign: "center" },
	empty: { color: t.textTertiary, fontSize: 13, lineHeight: 19, paddingHorizontal: 4 },
	choiceSheet: { paddingTop: 6, paddingHorizontal: 16, paddingBottom: 14, backgroundColor: t.bgSurface },
	choiceTitle: { color: t.textPrimary, fontSize: 19, lineHeight: 25, fontWeight: "700", paddingHorizontal: 4, paddingBottom: 10 },
	choiceList: { maxHeight: 460, overflow: "hidden", borderRadius: 16, backgroundColor: t.bgElevated },
	choiceRow: { minHeight: 52, flexDirection: "row", alignItems: "flex-start", gap: 10, paddingHorizontal: 15, paddingVertical: 10 },
	choiceDivider: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle },
	choiceSelected: { backgroundColor: t.tintBlue },
	choiceCopy: { flex: 1, minWidth: 0, gap: 1 },
	choiceLabel: { color: t.textPrimary, fontSize: 15, lineHeight: 20 },
	choiceDescription: { color: t.textTertiary, fontSize: 11, lineHeight: 15 },
	choiceCheck: { alignSelf: "center" },
	choiceLabelSelected: { color: t.blue, fontWeight: "700" },
});

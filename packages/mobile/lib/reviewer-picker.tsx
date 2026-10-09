import { Feather } from "@expo/vector-icons";
import { useState } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";
import { AgentLogo } from "./AgentLogo";
import { haptics } from "./haptics";
import { reviewerLabel, type ReviewerPickerProps } from "./reviewer-picker.types";
import type { Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";

type Picker = "reviewer" | "model" | null;

/** Android/web reviewer and model selectors, expanded inline like dropdowns. */
export function ReviewerPicker({ reviewers, selectedReviewer, effectiveReviewer, onSelectReviewer, model, onSelectModel, busy }: ReviewerPickerProps) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [expanded, setExpanded] = useState<Picker>(null);
	const reviewer = selectedReviewer || effectiveReviewer;
	const reviewerText = selectedReviewer
		? reviewerLabel(reviewers, selectedReviewer)
		: effectiveReviewer ? `Project default · ${reviewerLabel(reviewers, effectiveReviewer)}` : "Project default";

	function toggle(which: Exclude<Picker, null>) {
		haptics.tap();
		setExpanded((current) => current === which ? null : which);
	}

	function chooseReviewer(id: string) {
		haptics.select();
		setExpanded(null);
		onSelectReviewer(id);
	}

	function chooseModel(value: string) {
		haptics.select();
		setExpanded(null);
		onSelectModel(value);
	}

	return <View>
		<Pressable accessibilityRole="button" accessibilityLabel={`Reviewer: ${reviewerText}`} accessibilityState={{ expanded: expanded === "reviewer", disabled: busy }} disabled={busy} onPress={() => toggle("reviewer")} style={({ pressed }) => [styles.selector, pressed && styles.pressed, busy && styles.disabled]}>
			<Text style={styles.label}>Reviewer</Text>
			<View style={styles.valueGroup}>
				{reviewer ? <AgentLogo harness={reviewer} size={20} /> : <Feather name="users" size={17} color={t.textTertiary} />}
				<Text numberOfLines={1} ellipsizeMode="tail" style={styles.value}>{reviewerText}</Text>
				<Feather name={expanded === "reviewer" ? "chevron-up" : "chevron-down"} size={16} color={t.textTertiary} />
			</View>
		</Pressable>
		{expanded === "reviewer" ? <View style={styles.options}>
			<Option title="Project default" subtitle={effectiveReviewer ? reviewerLabel(reviewers, effectiveReviewer) : undefined} icon="users" selected={!selectedReviewer} disabled={busy} onPress={() => chooseReviewer("")} />
			{reviewers.map((agent) => <Option key={agent.id} title={agent.label} harness={agent.id} selected={selectedReviewer === agent.id} disabled={busy} onPress={() => chooseReviewer(agent.id)} />)}
		</View> : null}
		{model.choices.length ? <>
			<Pressable accessibilityRole="button" accessibilityLabel={`${model.title}: ${model.text}`} accessibilityState={{ expanded: expanded === "model", disabled: busy }} disabled={busy} onPress={() => toggle("model")} style={({ pressed }) => [styles.selector, styles.modelSelector, pressed && styles.pressed, busy && styles.disabled]}>
				<Text style={styles.label}>{model.title}</Text>
				<View style={styles.valueGroup}>
					<Text numberOfLines={1} ellipsizeMode="tail" style={styles.value}>{model.text}</Text>
					<Feather name={expanded === "model" ? "chevron-up" : "chevron-down"} size={16} color={t.textTertiary} />
				</View>
			</Pressable>
			{expanded === "model" ? <View style={styles.options}>
				{model.followAgent ? <Option title={model.followAgent.label} icon={null} selected={model.followAgent.selected} disabled={busy} onPress={() => chooseModel("")} /> : null}
				{model.choices.map((choice) => <Option key={choice.id} title={choice.label} icon={null} selected={choice.selected} disabled={busy} onPress={() => chooseModel(choice.value)} />)}
			</View> : null}
		</> : null}
	</View>;
}

function Option({ title, subtitle, icon, harness, selected, disabled, onPress }: { title: string; subtitle?: string; icon?: keyof typeof Feather.glyphMap | null; harness?: string; selected: boolean; disabled: boolean; onPress: () => void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <Pressable accessibilityRole="button" accessibilityState={{ selected, disabled }} disabled={disabled} onPress={onPress} style={({ pressed }) => [styles.option, pressed && styles.pressed, disabled && styles.disabled]}>
		{harness ? <AgentLogo harness={harness} size={20} /> : icon !== null ? <Feather name={icon ?? "user"} size={17} color={selected ? t.accent : t.textTertiary} /> : null}
		<View style={styles.copy}><Text style={styles.title}>{title}</Text>{subtitle ? <Text style={styles.subtitle}>{subtitle}</Text> : null}</View>
		{selected ? <Feather name="check" size={17} color={t.accent} /> : null}
	</Pressable>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	selector: { minHeight: 52, flexDirection: "row", alignItems: "center", justifyContent: "space-between", gap: 10, paddingVertical: 8, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: t.borderSubtle },
	modelSelector: { marginTop: 2 },
	label: { color: t.textPrimary, fontSize: 14, fontWeight: "600" },
	valueGroup: { flex: 1, minWidth: 0, flexDirection: "row", alignItems: "center", justifyContent: "flex-end", gap: 7 },
	value: { flexShrink: 1, color: t.textSecondary, fontSize: 14 },
	options: { paddingLeft: 10, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: t.borderSubtle },
	option: { minHeight: 44, flexDirection: "row", alignItems: "center", gap: 10, paddingVertical: 7, paddingRight: 2, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: t.borderSubtle },
	copy: { flex: 1, minWidth: 0, gap: 2 },
	title: { color: t.textPrimary, fontSize: 14, fontWeight: "500" },
	subtitle: { color: t.textTertiary, fontSize: 12 },
	pressed: { opacity: 0.6 },
	disabled: { opacity: 0.5 },
});

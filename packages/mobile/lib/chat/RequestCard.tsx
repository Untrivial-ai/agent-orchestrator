import { Feather } from "@expo/vector-icons";
import { useCallback, useState } from "react";
import { ActivityIndicator, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import { Directions, Gesture, GestureDetector } from "react-native-gesture-handler";
import Animated, { SlideInLeft, SlideInRight, SlideOutLeft, SlideOutRight } from "react-native-reanimated";

import { haptics } from "../haptics";
import { PAGE_SLIDE_MS } from "../motion";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { fontScaleCap, space, type } from "../tokens";
import { useReducedMotion } from "../useReducedMotion";
import type { RequestDockModel, RequestDockPage } from "./requestDockModel";

/**
 * The pending request, standing in for the composer.
 *
 * A blocking question is the next thing you have to do, so it takes the place
 * you were going to type in rather than sitting above it as a pointer to a card
 * somewhere up the timeline. Multiple questions page in place — chevrons, or a
 * swipe.
 *
 * The inline cards stay in the timeline as the record, and anything this card
 * cannot express honestly (a form, a URL handshake) is handed back to them.
 */
export function RequestCard({
	model,
	onDecide,
	onResolveInput,
	onShow,
	onDismiss,
}: {
	model: RequestDockModel;
	onDecide(requestId: string, decisionId: string): Promise<void>;
	onResolveInput(requestId: string, action: "accept" | "decline" | "cancel", content?: Record<string, unknown>): Promise<void>;
	onShow(sequence: number): void;
	onDismiss(): void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const reduceMotion = useReducedMotion();
	const [page, setPage] = useState(0);
	const [values, setValues] = useState<Record<string, unknown>>({});
	const [draft, setDraft] = useState("");
	const [submitting, setSubmitting] = useState(false);
	const [error, setError] = useState<string>();
	// Which way the next page should travel: forward pages come in from the
	// right, back pages from the left, so the motion agrees with the swipe.
	const [forward, setForward] = useState(true);

	const total = model.pages.length;
	const current: RequestDockPage | undefined = model.pages[page];
	const busy = model.busy || submitting;

	const goTo = useCallback((next: number) => {
		if (next < 0 || next >= total) return;
		setForward(next > page);
		haptics.select();
		setError(undefined);
		// Restore what was typed for the page being shown. Clearing it meant
		// stepping away and back silently discarded an answer already given.
		const target = model.pages[next];
		const stored = target?.freeText ? values[target.freeText.name] : undefined;
		setDraft(stored === undefined || stored === null ? "" : String(stored));
		setPage(next);
	}, [model.pages, page, total, values]);

	// A fling is enough for "swipe between questions" and never competes with the
	// vertical scroll behind the card.
	const swipe = Gesture.Race(
		Gesture.Fling().direction(Directions.RIGHT).onEnd(() => goTo(page - 1)).runOnJS(true),
		Gesture.Fling().direction(Directions.LEFT).onEnd(() => goTo(page + 1)).runOnJS(true),
	);

	if (!current) return null;

	const skippable = !current.required && model.kind === "input" && Boolean(model.requestId);

	const run = (work: Promise<void>) => {
		setSubmitting(true);
		setError(undefined);
		work
			.then(() => haptics.success())
			.catch((cause) => {
				setError(cause instanceof Error ? cause.message : String(cause));
				haptics.error();
			})
			.finally(() => setSubmitting(false));
	};

	const answer = (value: unknown, property?: string) => {
		if (busy || !model.requestId) return;
		if (model.kind === "approval") {
			run(onDecide(model.requestId, String(value)));
			return;
		}
		const name = property ?? current.propertyName;
		if (!name) return;
		const next = { ...values, [name]: value };
		setValues(next);
		// Last question answered — send the lot.
		if (page >= total - 1) run(onResolveInput(model.requestId, "accept", next));
		else goTo(page + 1);
	};

	const sendTyped = () => {
		const typed = draft.trim();
		if (!typed || !current.freeText) return;
		answer(current.freeText.numeric ? Number(typed) : typed, current.freeText.name);
	};

	// Deferred requests get a name and a way back to the card that owns them.
	if (!model.canAnswerInline) {
		return (
			<View style={styles.card}>
				<View style={styles.head}>
					<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.eyebrow}>{model.title}</Text>
					<Pressable accessibilityRole="button" accessibilityLabel="Dismiss and type instead" hitSlop={10} onPress={() => { haptics.tap(); onDismiss(); }}>
						<Feather name="x" size={17} color={t.textTertiary} />
					</Pressable>
				</View>
				{current.question ? <Text maxFontSizeMultiplier={fontScaleCap.title} style={styles.question}>{current.question}</Text> : null}
				<Pressable
					accessibilityRole="button"
					accessibilityLabel="Open the full request"
					onPress={() => { haptics.tap(); onShow(model.sequence); }}
					style={({ pressed }) => [styles.openForm, pressed && styles.pressed]}
				>
					<Feather name="arrow-up" size={15} color={t.accent} />
					<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.openFormText}>Open the full request</Text>
				</Pressable>
			</View>
		);
	}

	return (
		<GestureDetector gesture={swipe}>
			<View style={styles.card}>
				<View style={styles.head}>
					{total > 1 ? <View style={styles.pager}>
						<Pressable accessibilityRole="button" accessibilityLabel="Previous question" disabled={page === 0} hitSlop={10} onPress={() => goTo(page - 1)}>
							<Feather name="chevron-left" size={17} color={page === 0 ? t.textFaint : t.textSecondary} />
						</Pressable>
						<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.pagerText}>{page + 1} of {total}</Text>
						<Pressable accessibilityRole="button" accessibilityLabel="Next question" disabled={page >= total - 1} hitSlop={10} onPress={() => goTo(page + 1)}>
							<Feather name="chevron-right" size={17} color={page >= total - 1 ? t.textFaint : t.textSecondary} />
						</Pressable>
					</View> : <Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.eyebrow}>{model.title}</Text>}
					{/* One exit, in one place. Skip takes the corner where it exists;
					    otherwise — an approval, or a required question — the close holds
					    it, since the card has replaced the composer and that is the only
					    way back to it. */}
					{skippable ? (
						<Pressable
							accessibilityRole="button"
							accessibilityLabel="Skip this question"
							disabled={busy}
							hitSlop={10}
							onPress={() => { haptics.tap(); if (page < total - 1) goTo(page + 1); else run(onResolveInput(model.requestId ?? "", "accept", values)); }}
							style={({ pressed }) => [styles.skip, pressed && styles.pressed, busy && styles.dim]}
						>
							<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.skipText}>Skip</Text>
						</Pressable>
					) : (
						<Pressable accessibilityRole="button" accessibilityLabel="Dismiss and type instead" hitSlop={10} onPress={() => { haptics.tap(); onDismiss(); }}>
							<Feather name="x" size={17} color={t.textTertiary} />
						</Pressable>
					)}
				</View>

				<Animated.View
					key={page}
					entering={reduceMotion ? undefined : (forward ? SlideInRight : SlideInLeft).duration(PAGE_SLIDE_MS)}
					exiting={reduceMotion ? undefined : (forward ? SlideOutLeft : SlideOutRight).duration(PAGE_SLIDE_MS)}
				>
					{current.question ? <Text maxFontSizeMultiplier={fontScaleCap.title} style={styles.question}>{current.question}</Text> : null}

					{current.options.map((option, index) => {
						// The answers were always kept — they accumulate until the last
						// page submits them — but nothing said so on a second visit.
						const selected = current.propertyName ? values[current.propertyName] === option.id : false;
						return (
							<Pressable
								key={option.id}
								accessibilityRole="button"
								accessibilityLabel={option.label}
								accessibilityState={{ disabled: busy, selected }}
								disabled={busy}
								onPress={() => { haptics.tap(); answer(option.id); }}
								style={({ pressed }) => [styles.option, index > 0 && styles.optionDivider, pressed && styles.pressed, busy && styles.dim]}
							>
								<View style={[styles.ordinal, selected && styles.ordinalSelected]}>
									{selected
										? <Feather name="check" size={12} color={t.bgBase} />
										: <Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.ordinalText}>{index + 1}</Text>}
								</View>
								<View style={styles.optionBody}>
									<Text maxFontSizeMultiplier={fontScaleCap.body} style={[styles.optionLabel, selected && styles.optionLabelSelected]}>{option.label}</Text>
									{option.description ? <Text maxFontSizeMultiplier={fontScaleCap.body} style={styles.optionHint}>{option.description}</Text> : null}
								</View>
							</Pressable>
						);
					})}

					{current.freeText ? <View style={[styles.typed, current.options.length > 0 && styles.optionDivider]}>
						<View style={styles.ordinal}><Feather name="edit-2" size={12} color={t.textTertiary} /></View>
						<TextInput
							accessibilityLabel={current.freeText.placeholder}
							editable={!busy}
							value={draft}
							onChangeText={setDraft}
							placeholder={current.freeText.placeholder}
							placeholderTextColor={t.textTertiary}
							keyboardType={current.freeText.numeric ? "number-pad" : "default"}
							maxLength={current.freeText.maxLength}
							returnKeyType="send"
							onSubmitEditing={sendTyped}
							maxFontSizeMultiplier={fontScaleCap.body}
							style={styles.typedInput}
						/>
						{draft.trim() ? <Pressable
							accessibilityRole="button"
							accessibilityLabel="Send answer"
							accessibilityState={{ disabled: busy }}
							disabled={busy}
							onPress={sendTyped}
							style={({ pressed }) => [styles.send, pressed && styles.pressed]}
						>
							{busy ? <ActivityIndicator size="small" color={t.bgBase} /> : <Feather name="arrow-up" size={15} color={t.bgBase} />}
						</Pressable> : null}
					</View> : null}
				</Animated.View>


				{busy && !draft.trim() ? <View style={styles.busy}><ActivityIndicator size="small" color={t.textTertiary} /></View> : null}
				{error ? <Text accessibilityRole="alert" maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.error}>{error}</Text> : null}
			</View>
		</GestureDetector>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		card: {
			backgroundColor: t.bgElevated,
			borderWidth: StyleSheet.hairlineWidth,
			borderColor: t.borderDefault,
			borderRadius: 20,
			borderCurve: "continuous",
			paddingHorizontal: space.md,
			paddingTop: space.sm,
			paddingBottom: space.xs,
			overflow: "hidden",
		},
		head: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", minHeight: 26 },
		pager: { flexDirection: "row", alignItems: "center", gap: space.sm },
		pagerText: { color: t.textSecondary, fontSize: type.caption1.fontSize, fontWeight: "600", fontVariant: ["tabular-nums"] },
		eyebrow: { color: t.textSecondary, fontSize: type.caption2.fontSize, fontWeight: "700", letterSpacing: 0.4, textTransform: "uppercase" },
		question: { color: t.textPrimary, fontSize: type.body.fontSize, lineHeight: type.body.lineHeight, fontWeight: "600", marginTop: space.xxs, marginBottom: space.sm },
		option: { flexDirection: "row", alignItems: "center", gap: space.md, paddingVertical: space.md },
		optionDivider: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle },
		ordinal: { width: 26, height: 26, borderRadius: 12, alignItems: "center", justifyContent: "center", backgroundColor: t.bgSubtle },
		ordinalText: { color: t.textSecondary, fontSize: type.caption1.fontSize, fontWeight: "700" },
		// Neutral, like the Orchestrator badge: the filled circle and the check
		// carry the selection, so it needs no colour of its own.
		ordinalSelected: { backgroundColor: t.textSecondary },
		optionLabelSelected: { fontWeight: "700" },
		optionBody: { flex: 1, minWidth: 0, gap: space.hair },
		optionLabel: { color: t.textPrimary, fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight },
		optionHint: { color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight },
		typed: { flexDirection: "row", alignItems: "center", gap: space.md, paddingVertical: space.xs },
		typedInput: { flex: 1, minHeight: 38, color: t.textPrimary, fontSize: type.subheadline.fontSize, paddingVertical: space.sm },
		send: { width: 34, height: 34, borderRadius: 16, alignItems: "center", justifyContent: "center", backgroundColor: t.textPrimary },
		skip: { paddingVertical: space.hair, paddingHorizontal: space.hair },
		skipText: { color: t.textSecondary, fontSize: type.footnote.fontSize, fontWeight: "600" },
		openForm: { flexDirection: "row", alignItems: "center", gap: space.xs, paddingVertical: space.sm },
		openFormText: { color: t.accent, fontSize: type.subheadline.fontSize, fontWeight: "600" },
		busy: { paddingVertical: space.sm, alignItems: "center" },
		error: { color: t.red, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, paddingBottom: space.xs },
		pressed: { opacity: 0.6 },
		dim: { opacity: 0.5 },
	});

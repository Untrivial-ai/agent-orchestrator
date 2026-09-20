import { Feather } from "./icons";
import { Children, memo, useEffect, useRef, type ReactNode } from "react";
import {
	ActivityIndicator,
	Animated,
	Image,
	Pressable,
	StyleSheet,
	Switch,
	Text,
	View,
	type StyleProp,
	type TextStyle,
	type ViewStyle,
} from "react-native";
import { haptics } from "./haptics";
import { BREATHE_MS, shouldBreathe } from "./motion";
import { useEnterTransition, usePressScale } from "./motionHooks";
import { NativeHeaderButton, type NativeHeaderButtonIcon } from "./native-header-button";
import { useOptionalSidebarNavigation } from "./sidebar-navigation-context";
import { useReducedMotion } from "./useReducedMotion";
import { fontScaleCap, press, space, type } from "./tokens";
import type { ConnStatus } from "./store";
import { statusVisual, type Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";
// AO mascot glyph (transparent) shown beside each screen heading.
import MASCOT from "../assets/mascot.png";

// A gently breathing dot - the only motion in the UI, reserved for "working".
// Memoized so an unrelated parent re-render doesn't tear down and restart the
// Animated loop (which causes a visible flicker and per-tick allocations).
export const Dot = memo(function Dot({
	color,
	size = 9,
	breathing = false,
}: {
	color: string;
	size?: number;
	breathing?: boolean;
}) {
	const pulse = useRef(new Animated.Value(1)).current;
	// Consumed here rather than at the call sites: this is the most-repeated
	// animation in the app, so honouring the setting once inside the primitive
	// fixes every `<Dot breathing>` — status badges, the connection lamp, project
	// rows — without touching any of them.
	const reduceMotion = useReducedMotion();
	const animate = shouldBreathe(reduceMotion, breathing);
	useEffect(() => {
		// Deliberately not a zero duration: a zero-length loop is a busy loop, so
		// the animation must not start at all.
		if (!animate) return;
		const loop = Animated.loop(
			Animated.sequence([
				Animated.timing(pulse, {
					toValue: 0.35,
					duration: BREATHE_MS,
					useNativeDriver: true,
				}),
				Animated.timing(pulse, {
					toValue: 1,
					duration: BREATHE_MS,
					useNativeDriver: true,
				}),
			]),
		);
		loop.start();
		return () => {
			loop.stop();
			// Leave the dot at full opacity; a stopped loop otherwise freezes it
			// mid-fade, which reads as a rendering bug rather than a resting state.
			pulse.setValue(1);
		};
	}, [animate, pulse]);

	return (
		<Animated.View
			style={{
				width: size,
				height: size,
				borderRadius: size / 2,
				backgroundColor: color,
				opacity: animate ? pulse : 1,
			}}
		/>
	);
});

// A selectable pill - used by the project switcher, PR filters, and spawn picker
// so the active/inactive color logic lives in exactly one place.
export function Pill({
	label,
	active,
	onPress,
	style,
	textStyle,
}: {
	label: string;
	active: boolean;
	onPress: () => void;
	style?: StyleProp<ViewStyle>;
	textStyle?: StyleProp<TextStyle>;
}) {
	const s = useThemedStyles(makeStyles);
	return (
		<Pressable
			onPress={() => {
				haptics.select();
				onPress();
			}}
			style={[s.pill, active && s.pillActive, style]}
		>
			<Text
				numberOfLines={1}
				maxFontSizeMultiplier={fontScaleCap.chrome}
				style={[s.pillText, active && s.pillTextActive, textStyle]}
			>
				{label}
			</Text>
		</Pressable>
	);
}

export function StatusBadge({ status }: { status?: string | null }) {
	const t = useTheme();
	const s = useThemedStyles(makeStyles);
	const v = statusVisual(t, status);
	return (
		<View style={s.badge}>
			<Dot color={v.color} breathing={v.breathing} size={8} />
			<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={[s.badgeText, { color: v.color }]}>
				{v.label}
			</Text>
		</View>
	);
}

export function Chip({
	label,
	color,
	tint,
	mono = false,
	icon,
}: {
	label: string;
	color?: string;
	tint?: string;
	mono?: boolean;
	icon?: keyof typeof Feather.glyphMap;
}) {
	const t = useTheme();
	const s = useThemedStyles(makeStyles);
	// Resolved here rather than as default parameter values: a default reads the
	// palette at module load, which would pin every unstyled chip to dark.
	const fg = color ?? t.textSecondary;
	const bg = tint ?? t.bgSubtle;
	return (
		<View style={[s.chip, { backgroundColor: bg }]}>
			{icon ? <Feather name={icon} size={12} color={fg} style={{ marginRight: space.xxs }} /> : null}
			<Text
				style={[s.chipText, { color: fg }, mono && { fontFamily: t.fontMono, fontSize: type.caption2.fontSize }]}
				numberOfLines={1}
				maxFontSizeMultiplier={fontScaleCap.chrome}
			>
				{label}
			</Text>
		</View>
	);
}

export function Card({
	children,
	onPress,
	style,
}: {
	children: ReactNode;
	onPress?: () => void;
	style?: StyleProp<ViewStyle>;
}) {
	const s = useThemedStyles(makeStyles);
	const pressFx = usePressScale();
	if (!onPress) return <View style={[s.card, style]}>{children}</View>;
	return (
		// The animated transform lives on a wrapper: `Animated.createAnimatedComponent`
		// drops function styles, and the card's pressed state needs one.
		<Animated.View style={[style, pressFx.style]}>
			<Pressable
				onPressIn={pressFx.onPressIn}
				onPressOut={pressFx.onPressOut}
				onPress={() => {
					haptics.tap();
					onPress();
				}}
				style={({ pressed }) => [s.card, pressed && s.cardPressed]}
			>
				{children}
			</Pressable>
		</Animated.View>
	);
}

export function SectionHeader({ label, color, count }: { label: string; color: string; count?: number }) {
	const s = useThemedStyles(makeStyles);
	return (
		<View style={s.sectionHeader}>
			<View style={[s.sectionBar, { backgroundColor: color }]} />
			<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={s.sectionLabel}>
				{label.toUpperCase()}
			</Text>
			{count !== undefined ? (
				<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={s.sectionCount}>
					{count}
				</Text>
			) : null}
		</View>
	);
}

// A bare glyph for the header's trailing slot. Deliberately not `IconButton`,
// which is a bordered card action and reads as a box when dropped into a header.
export function HeaderIconButton({
	icon,
	label,
	onPress,
	badge = 0,
}: {
	icon: NativeHeaderButtonIcon;
	/** Required — the control has no visible text. */
	label: string;
	onPress: () => void;
	/** Non-zero shows an unread dot. The number itself is not drawn. */
	badge?: number;
}) {
	const s = useThemedStyles(makeStyles);
	return (
		<View style={s.headerIconBtn} accessibilityLabel={badge > 0 ? `${label}, ${badge} unread` : label}>
			<NativeHeaderButton
				icon={icon}
				label={badge > 0 ? `${label}, ${badge} unread` : label}
				onPress={() => {
					haptics.tap();
					onPress();
				}}
			/>
			{badge > 0 ? <View style={s.headerBadge} /> : null}
		</View>
	);
}

// The mascot, with the tip of its wand doubling as the connection lamp: it glows
// green when the daemon is reachable and goes dark when it isn't. The tip sits
// at ~85% across and ~7% down `mascot.png` — where `wandTip`/`wandHalo` below
// get their offsets from.
export function MascotLamp({ status, size = 40 }: { status?: ConnStatus; size?: number }) {
	const t = useTheme();
	const s = useThemedStyles(makeStyles);
	const color = status === "open" ? t.green : status === "connecting" ? t.amber : t.textFaint;
	const lit = status === "open" || status === "connecting";
	const label = status === "open" ? "Connected" : status === "connecting" ? "Connecting" : "Offline";
	// Every offset below is a fraction of the artwork's 40x35 box, so the lamp
	// stays on the wand tip at whatever size the logo is drawn.
	const k = size / 40;
	return (
		<View
			style={[s.mascotWrap, { width: size, height: 35 * k }]}
			accessible
			accessibilityRole="image"
			accessibilityLabel={status ? `AO mascot, ${label}` : "AO mascot"}
		>
			<Image source={MASCOT} style={{ width: size, height: 35 * k }} resizeMode="contain" />
			{status ? (
				<>
					{/* Halo first, dot on top: RN has no boxShadow, so the glow is a
					    larger translucent circle plus a platform shadow/elevation. */}
					{lit ? <View style={[s.wandHalo, { left: 26 * k, top: -4 * k, width: 16 * k, height: 16 * k, borderRadius: 8 * k, backgroundColor: color, shadowColor: color }]} /> : null}
					<View style={[s.wandTip, { left: 30.5 * k }]}>
						<Dot color={color} size={7 * k} breathing={status === "connecting"} />
					</View>
				</>
			) : null}
		</View>
	);
}

export function ScreenHeader({
	title,
	left,
	right,
}: {
	title: string;
	/** Detail routes can supply a back action instead of the sidebar button. */
	left?: ReactNode;
	right?: ReactNode;
}) {
	const s = useThemedStyles(makeStyles);
	const sidebar = useOptionalSidebarNavigation();
	return (
		<View style={s.screenHeader}>
			{left ?? (sidebar ? <HeaderIconButton icon="menu" label="Open navigation" onPress={sidebar.openSidebar} /> : null)}
			<View style={{ flex: 1 }}>
				<Text maxFontSizeMultiplier={fontScaleCap.title} style={s.screenTitle}>
					{title}
				</Text>
			</View>
			{right}
		</View>
	);
}

/**
 * A board group's heading. With `onToggle` it folds its section away — same
 * typography and rule as before, plus the chevron that says so.
 *
 * The chevron sits beside the label rather than at the far edge so the row still
 * reads as one heading with a rule running out of it, which is what the board
 * looked like before sections could be folded.
 */
export function ListSectionHeader({
	label,
	count,
	open = true,
	onToggle,
}: {
	label: string;
	count?: number;
	open?: boolean;
	onToggle?: () => void;
}) {
	const t = useTheme();
	const s = useThemedStyles(makeStyles);
	const body = (
		<>
			<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={s.listSectionLabel}>
				{label}
			</Text>
			{onToggle ? (
				<Feather name={open ? "chevron-down" : "chevron-right"} size={15} color={t.textTertiary} />
			) : null}
			{count !== undefined ? (
				<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={s.listSectionCount}>
					{count}
				</Text>
			) : null}
		</>
	);

	if (!onToggle) return <View style={s.listSectionHeader}>{body}</View>;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityState={{ expanded: open }}
			accessibilityLabel={`${label} section`}
			onPress={onToggle}
			style={({ pressed }) => [s.listSectionHeader, pressed && { opacity: 0.6 }]}
		>
			{body}
		</Pressable>
	);
}

export function Button({
	title,
	onPress,
	variant = "primary",
	loading = false,
	disabled = false,
	icon,
	style,
}: {
	title: string;
	onPress: () => void;
	variant?: "primary" | "ghost" | "danger";
	loading?: boolean;
	disabled?: boolean;
	icon?: keyof typeof Feather.glyphMap;
	style?: StyleProp<ViewStyle>;
}) {
	const t = useTheme();
	const s = useThemedStyles(makeStyles);
	const isPrimary = variant === "primary";
	const isDanger = variant === "danger";
	// `onAccent`, not a literal: near-black reads best on the dark theme's light
	// accent, but is invisible on light mode's darker one, which needs white.
	const fg = isPrimary ? t.onAccent : isDanger ? t.red : t.accent;
	const pressFx = usePressScale(disabled || loading);
	return (
		<Animated.View style={[style, pressFx.style]}>
			<Pressable
				onPressIn={pressFx.onPressIn}
				onPressOut={pressFx.onPressOut}
				onPress={() => {
					// Danger actions get a cautionary buzz; everything else a light tap.
					if (isDanger) haptics.warning();
					else haptics.tap();
					onPress();
				}}
				disabled={disabled || loading}
				style={({ pressed }) => [
					s.btn,
					isPrimary && s.btnPrimary,
					!isPrimary && s.btnGhost,
					isDanger && s.btnDanger,
					(disabled || loading) && { opacity: 0.5 },
					pressed && { opacity: press.opacity },
				]}
			>
				{loading ? (
					<ActivityIndicator color={fg} size="small" />
				) : (
					<View style={s.btnInner}>
						{icon ? <Feather name={icon} size={15} color={fg} style={{ marginRight: space.xs }} /> : null}
						<Text maxFontSizeMultiplier={fontScaleCap.body} style={[s.btnText, { color: fg }]}>
							{title}
						</Text>
					</View>
				)}
			</Pressable>
		</Animated.View>
	);
}

// A numbered instruction row. Used by both onboarding screens: the welcome
// screen passes a `hint` for the full "how it works" list, the scanner omits it
// so the steps stay one line each above the viewfinder.
export function NumberedStep({
	n,
	title,
	hint,
	compact = false,
}: {
	n: number;
	title: string;
	hint?: string;
	compact?: boolean;
}) {
	const s = useThemedStyles(makeStyles);
	return (
		<View style={[s.step, compact && s.stepCompact]}>
			<View style={[s.stepBadge, compact && s.stepBadgeCompact]}>
				<Text style={[s.stepNum, compact && s.stepNumCompact]}>{n}</Text>
			</View>
			<View style={{ flex: 1 }}>
				<Text style={[s.stepTitle, compact && s.stepTitleCompact]}>{title}</Text>
				{hint ? <Text style={s.stepHint}>{hint}</Text> : null}
			</View>
		</View>
	);
}

// The body of a sheet. The sheet chrome itself — the card, the rounded corners,
// the grabber and the drag-to-dismiss — is now the OS's job: these render inside
// native `formSheet` routes (see `app/sheets/*` and the registrations in
// `app/_layout.tsx`). Re-implementing that in JS on top of RN's `Modal` never
// felt native, because `Modal` is a plain container with no gesture support.
/**
 * The title block of a sheet. Separate from `SheetScreen` because the scrolling
 * sheets pass it as their list's `ListHeaderComponent` rather than rendering it
 * as a sibling of the list: a scroller sitting next to static siblings inside a
 * sheet laid the two on top of each other. One scrolling root, header inside it,
 * has no such interplay — and it matches how iOS's own sheets behave.
 */
export function SheetHeader({
	title,
	subtitle,
	right,
}: {
	title: string;
	subtitle?: string;
	/** Trailing control on the title row — the agent sheet's Refresh lives here. */
	right?: ReactNode;
}) {
	const s = useThemedStyles(makeStyles);
	return (
		<View style={s.sheetHeader}>
			<View style={s.sheetTitleRow}>
				<Text style={[s.sheetTitle, { flex: 1 }]}>{title}</Text>
				{right}
			</View>
			{subtitle ? <Text style={s.sheetSubtitle}>{subtitle}</Text> : null}
		</View>
	);
}

/**
 * `contentContainerStyle` for a scrolling sheet — the inset its header and rows
 * share. Pure padding, so it needs no theme and can be a plain const.
 */
export const SHEET_SCROLL_CONTENT = {
	paddingHorizontal: space.xl,
	paddingTop: space.xl,
	paddingBottom: space.xxl,
};

/** A sheet whose content is short and fixed, so it needs no scrolling root. */
export function SheetScreen({
	title,
	subtitle,
	right,
	children,
}: {
	title: string;
	subtitle?: string;
	right?: ReactNode;
	children: ReactNode;
}) {
	const s = useThemedStyles(makeStyles);
	return (
		<View style={s.sheetScreen}>
			<SheetHeader title={title} subtitle={subtitle} right={right} />
			{children}
		</View>
	);
}

// ---- Grouped settings list --------------------------------------------------
// An iOS-Settings-style grouped list, hand-rolled rather than pulled from
// @expo/ui: that library's Form/Section only exist on its SwiftUI side, so
// Android would need this implementation anyway. Rendering both platforms from
// the same primitives is what keeps them aligned.

// One titled group of rows in a single rounded container. `footer` carries the
// explanatory prose that used to sit inline above the controls.
export function SettingsGroup({
	title,
	footer,
	children,
	style,
}: {
	title?: string;
	footer?: string;
	children: ReactNode;
	style?: StyleProp<ViewStyle>;
}) {
	const s = useThemedStyles(makeStyles);
	// Separators belong *between* rows, so they're injected here rather than
	// drawn by each row — a row can't know whether it is the last one.
	// Children.toArray flattens fragments, drops nulls from conditional rows, and
	// hands back stable keys, so a conditionally-rendered row can't shift them.
	const rows = Children.toArray(children);
	return (
		<View style={[s.group, style]}>
			{title ? <Text style={s.groupTitle}>{title.toUpperCase()}</Text> : null}
			<View style={s.groupBody}>
				{rows.map((row, i) => (
					<View key={(row as { key?: string }).key ?? i}>
						{i > 0 ? <View style={s.separator} /> : null}
						{row}
					</View>
				))}
			</View>
			{footer ? <Text style={s.groupFooter}>{footer}</Text> : null}
		</View>
	);
}

// A single row: icon, label, right-aligned value, chevron when tappable.
// `right` replaces the value/chevron entirely (used by SettingsToggle).
export function SettingsRow({
	icon,
	label,
	value,
	valueColor,
	leading,
	onPress,
	destructive = false,
	loading = false,
	disabled = false,
	right,
}: {
	icon?: keyof typeof Feather.glyphMap;
	label: string;
	value?: string;
	valueColor?: string;
	// Rendered immediately before the value — the connection dot lives here.
	leading?: ReactNode;
	onPress?: () => void;
	destructive?: boolean;
	loading?: boolean;
	disabled?: boolean;
	right?: ReactNode;
}) {
	const t = useTheme();
	const s = useThemedStyles(makeStyles);
	const labelColor = destructive ? t.red : t.textPrimary;
	const iconColor = destructive ? t.red : t.textSecondary;
	const pressFx = usePressScale();
	const body = (
		<>
			{icon ? <Feather name={icon} size={17} color={iconColor} style={s.rowIcon} /> : null}
			<Text
				style={[s.rowLabel, { color: labelColor }]}
				numberOfLines={1}
				maxFontSizeMultiplier={fontScaleCap.body}
			>
				{label}
			</Text>
			{/* The accessory never shrinks: at large text sizes the label has to be the
			    one that gives way, or it draws underneath the switch. */}
			<View style={s.rowAccessory}>
				{right ?? (
					<>
						{loading ? <ActivityIndicator size="small" color={t.textTertiary} /> : null}
						{!loading && leading ? leading : null}
						{!loading && value ? (
							<Text
								style={[s.rowValue, valueColor ? { color: valueColor } : null]}
								numberOfLines={1}
								maxFontSizeMultiplier={fontScaleCap.chrome}
							>
								{value}
							</Text>
						) : null}
						{onPress ? <Feather name="chevron-right" size={17} color={t.textFaint} style={s.rowChevron} /> : null}
					</>
				)}
			</View>
		</>
	);

	if (!onPress) return <View style={[s.row, disabled && s.rowDisabled]}>{body}</View>;
	return (
		<Animated.View style={pressFx.style}>
			<Pressable
				disabled={disabled || loading}
				onPressIn={pressFx.onPressIn}
				onPressOut={pressFx.onPressOut}
				onPress={() => {
					if (destructive) haptics.warning();
					else haptics.tap();
					onPress();
				}}
				style={({ pressed }) => [s.row, pressed && s.rowPressed, (disabled || loading) && s.rowDisabled]}
			>
				{body}
			</Pressable>
		</Animated.View>
	);
}

// A row whose accessory is a switch. Same metrics as SettingsRow so the two
// line up inside one group.
export function SettingsToggle({
	icon,
	label,
	value,
	onValueChange,
	disabled = false,
	busy = false,
}: {
	icon?: keyof typeof Feather.glyphMap;
	label: string;
	value: boolean;
	onValueChange: (v: boolean) => void;
	disabled?: boolean;
	busy?: boolean;
}) {
	const t = useTheme();
	return (
		<SettingsRow
			icon={icon}
			label={label}
			disabled={disabled}
			right={
				busy ? (
					<ActivityIndicator size="small" color={t.textTertiary} />
				) : (
					<Switch
						value={value}
						onValueChange={(v) => {
							haptics.select();
							onValueChange(v);
						}}
						disabled={disabled}
						trackColor={{ true: t.accent, false: t.borderStrong }}
					/>
				)
			}
		/>
	);
}

/**
 * A compact square action, for the cluster that sits bottom-right on a card.
 *
 * Cards carry two or three of these, so they are icon-only: a row of labelled
 * buttons would dominate a card whose point is the content above it.
 */
export function IconButton({
	icon,
	label,
	onPress,
	destructive = false,
	disabled = false,
	loading = false,
}: {
	icon: keyof typeof Feather.glyphMap;
	/** Required — the control has no visible text of its own. */
	label: string;
	onPress: () => void;
	destructive?: boolean;
	disabled?: boolean;
	loading?: boolean;
}) {
	const t = useTheme();
	const s = useThemedStyles(makeStyles);
	const pressFx = usePressScale(disabled || loading);
	return (
		<Animated.View style={pressFx.style}>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={label}
				accessibilityState={{ disabled: disabled || loading }}
				disabled={disabled || loading}
				hitSlop={6}
				onPressIn={pressFx.onPressIn}
				onPressOut={pressFx.onPressOut}
				onPress={() => {
					if (destructive) haptics.warning();
					else haptics.tap();
					onPress();
				}}
				style={({ pressed }) => [
					s.iconBtn,
					destructive && { borderColor: t.tintRed },
					pressed && (destructive ? s.iconBtnPressedDanger : s.iconBtnPressed),
					(disabled || loading) && { opacity: 0.4 },
				]}
			>
				{loading ? (
					<ActivityIndicator size="small" color={t.textSecondary} />
				) : (
					<Feather name={icon} size={15} color={destructive ? t.red : t.textSecondary} />
				)}
			</Pressable>
		</Animated.View>
	);
}

export function EmptyState({
	icon = "inbox",
	title,
	message,
	action,
}: {
	icon?: keyof typeof Feather.glyphMap;
	title: string;
	message?: string;
	action?: ReactNode;
}) {
	const t = useTheme();
	const s = useThemedStyles(makeStyles);
	// An empty state appears once per screen and then sits still, so it can afford
	// a quiet entrance. Lists and rows stay unstaggered.
	const enter = useEnterTransition(space.xxs);
	return (
		<Animated.View style={[s.empty, enter]}>
			<View style={s.emptyIcon}>
				<Feather name={icon} size={24} color={t.textTertiary} />
			</View>
			<Text maxFontSizeMultiplier={fontScaleCap.body} style={s.emptyTitle}>
				{title}
			</Text>
			{message ? (
				<Text maxFontSizeMultiplier={fontScaleCap.body} style={s.emptyMsg}>
					{message}
				</Text>
			) : null}
			{action ? <View style={{ marginTop: space.lg }}>{action}</View> : null}
		</Animated.View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		listSectionHeader: {
			flexDirection: "row",
			alignItems: "center",
			gap: space.sm,
			paddingHorizontal: space.lg,
			paddingTop: space.lg,
			paddingBottom: space.xxs,
		},
		listSectionLabel: { fontFamily: "Geist_500Medium", color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "500" },
		// Mono and tabular so a count changing from 9 to 10 does not shift the rule.
		listSectionCount: { color: t.textFaint, fontSize: type.caption1.fontSize, fontWeight: "600", fontFamily: t.fontMono },
		badge: { flexDirection: "row", alignItems: "center", gap: space.xs },
		badgeText: { fontFamily: "Geist_600SemiBold", fontSize: type.caption1.fontSize, fontWeight: "600" },

		pill: {
			paddingHorizontal: space.md,
			paddingVertical: space.xs,
			borderRadius: 20,
			borderWidth: 1,
			borderColor: t.borderDefault,
			backgroundColor: t.bgElevated,
		},
		pillActive: { backgroundColor: t.accentTint, borderColor: t.accent },
		pillText: { fontFamily: "Geist_600SemiBold", color: t.textSecondary, fontSize: type.footnote.fontSize, fontWeight: "600" },
		pillTextActive: { color: t.accent },

		chip: {
			flexDirection: "row",
			alignItems: "center",
			paddingHorizontal: space.sm,
			paddingVertical: space.hair,
			borderRadius: 4,
		},
		chipText: { fontFamily: "Geist_600SemiBold", fontSize: type.caption2.fontSize, fontWeight: "600" },

		card: {
			backgroundColor: t.bgElevated,
			borderRadius: 12,
			borderWidth: 1,
			borderColor: t.borderSubtle,
			padding: space.md,
		},
		cardPressed: {
			backgroundColor: t.bgElevatedHover,
			borderColor: t.borderDefault,
		},

		sectionHeader: {
			flexDirection: "row",
			alignItems: "center",
			paddingHorizontal: space.lg,
			paddingTop: space.xl,
			paddingBottom: space.sm,
			gap: space.sm,
		},
		sectionBar: { width: 3, height: 13, borderRadius: 2 },
		sectionLabel: { fontFamily: "Geist_600SemiBold",
			color: t.textSecondary,
			fontSize: type.caption2.fontSize,
			letterSpacing: 1.2,
			fontWeight: "600",
			flex: 1,
		},
		sectionCount: {
			color: t.textTertiary,
			fontSize: type.caption1.fontSize,
			fontWeight: "600",
			fontFamily: t.fontMono,
		},

		screenHeader: {
			flexDirection: "row",
			alignItems: "center",
			paddingHorizontal: space.lg,
			paddingTop: space.sm,
			paddingBottom: space.sm,
			gap: space.md,
		},
		titleRow: { flexDirection: "row", alignItems: "center", gap: space.sm },
		// Enlarged from 30x26: the wand-tip lamp replaced the "live" pill, so the tip
		// has to be big enough for the dot to actually read as a status light.
		mascotWrap: { width: 40, height: 35, marginTop: space.hair },
		mascot: { width: 40, height: 35 },
		// Positioned on the wand tip — see the note above MascotLamp. Offsets are the
		// tip fraction of the 40x35 box, less half the dot/halo so they sit centred.
		// zIndex, not paint order: Android orders siblings by elevation/zIndex, and
		// the halo used to carry `elevation` — which put a 28%-opacity circle *over*
		// the dot and washed it out. Stating both keeps the dot on top everywhere.
		wandTip: { position: "absolute", left: 30.5, top: 0, zIndex: 1 },
		wandHalo: {
			position: "absolute",
			left: 26,
			top: -4,
			width: 16,
			height: 16,
			borderRadius: 8,
			opacity: 0.28,
			zIndex: 0,
			// iOS-only; Android has no glow-style shadow, so there the halo circle
			// alone stands in for it. No `elevation` — that draws a drop shadow
			// beneath the view and reorders it, neither of which is a glow.
			shadowOpacity: 0.9,
			shadowRadius: 6,
			shadowOffset: { width: 0, height: 0 },
		},
		screenTitle: { fontFamily: "Geist_600SemiBold",
			color: t.textPrimary,
			fontSize: type.title1.fontSize,
			fontWeight: "600",
			letterSpacing: -0.5,
		},
		screenSubtitle: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.caption1.fontSize, marginTop: space.none },
		headerIconBtn: {
			alignItems: "center",
			justifyContent: "center",
			padding: space.hair,
		},
		headerBadge: {
			position: "absolute",
			top: 0,
			right: 1,
			width: 9,
			height: 9,
			borderRadius: 4,
			backgroundColor: t.accent,
			borderWidth: 1.5,
			borderColor: t.bgBase,
		},

		btn: {
			borderRadius: 8,
			paddingVertical: space.md,
			paddingHorizontal: space.lg,
			alignItems: "center",
		},
		btnInner: { flexDirection: "row", alignItems: "center" },
		btnPrimary: { backgroundColor: t.accent },
		btnGhost: {
			borderWidth: 1,
			borderColor: t.borderStrong,
			backgroundColor: t.bgElevated,
		},
		btnDanger: { borderColor: t.tintRed, backgroundColor: t.tintRed },
		btnText: { fontFamily: "Geist_600SemiBold", fontSize: type.subheadline.fontSize, fontWeight: "600" },

		step: {
			flexDirection: "row",
			alignItems: "flex-start",
			gap: space.md,
			paddingVertical: space.md,
		},
		stepCompact: { paddingVertical: space.xs, alignItems: "center", gap: space.md },
		stepBadge: {
			width: 30,
			height: 30,
			borderRadius: 8,
			backgroundColor: t.bgElevated,
			borderWidth: 1,
			borderColor: t.borderSubtle,
			alignItems: "center",
			justifyContent: "center",
		},
		stepBadgeCompact: { width: 23, height: 23, borderRadius: 12 },
		stepNum: { fontFamily: "Geist_600SemiBold", color: t.textSecondary, fontSize: type.footnote.fontSize, fontWeight: "600" },
		stepNumCompact: { fontFamily: "Geist_400Regular", fontSize: type.caption2.fontSize },
		stepTitle: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.subheadline.fontSize, fontWeight: "600" },
		stepTitleCompact: { fontFamily: "Geist_600SemiBold", fontSize: type.subheadline.fontSize, fontWeight: "600" },
		stepHint: { fontFamily: "Geist_400Regular",
			color: t.textTertiary,
			fontSize: type.footnote.fontSize,
			lineHeight: type.footnote.lineHeight,
			marginTop: space.hair,
		},

		// No card, corners or grabber here — the native sheet draws all of that. The
		// top padding leaves room for the OS grabber so the title doesn't sit under it.
		// No bottom safe-area inset on either of these: the native sheet already
		// reserves room for the home indicator, and adding it again left a dead strip.
		sheetScreen: {
			backgroundColor: t.bgSurface,
			paddingHorizontal: space.xl,
			paddingTop: space.xl,
			paddingBottom: space.xl,
		},
		// Padding lives on the scroll content rather than a wrapper, so a scrolling
		// sheet's list can run edge to edge while its rows keep the same inset.
		sheetHeader: { paddingBottom: space.hair },
		sheetTitleRow: { flexDirection: "row", alignItems: "center", gap: space.md },
		sheetTitle: { fontFamily: "Geist_600SemiBold",
			color: t.textPrimary,
			fontSize: type.title3.fontSize,
			fontWeight: "600",
			letterSpacing: -0.3,
		},
		sheetSubtitle: { fontFamily: "Geist_400Regular",
			color: t.textSecondary,
			fontSize: type.footnote.fontSize,
			lineHeight: type.footnote.lineHeight,
			marginTop: space.xxs,
		},

		group: { marginBottom: space.xxl },
		groupTitle: { fontFamily: "Geist_600SemiBold",
			color: t.textTertiary,
			fontSize: type.caption2.fontSize,
			letterSpacing: 1.2,
			fontWeight: "600",
			marginBottom: space.sm,
			marginLeft: space.xxs,
		},
		groupBody: {
			backgroundColor: t.bgElevated,
			borderRadius: 12,
			borderWidth: 1,
			borderColor: t.borderSubtle,
			overflow: "hidden",
		},
		groupFooter: { fontFamily: "Geist_400Regular",
			color: t.textTertiary,
			fontSize: type.caption1.fontSize,
			lineHeight: type.caption1.lineHeight,
			marginTop: space.sm,
			marginHorizontal: space.xxs,
		},
		// Inset to the label's x-origin (row padding + icon + gap), the iOS detail
		// that makes a stack of rows read as one grouped list.
		separator: {
			height: StyleSheet.hairlineWidth,
			backgroundColor: t.borderDefault,
			marginLeft: 43,
		},
		row: {
			flexDirection: "row",
			alignItems: "center",
			minHeight: 48,
			paddingVertical: space.md,
			paddingHorizontal: space.md,
			gap: space.sm,
		},
		rowPressed: { backgroundColor: t.bgElevatedHover },
		rowDisabled: { opacity: 0.45 },
		rowIcon: { width: 17, marginRight: space.xxs },
		rowLabel: { fontFamily: "Geist_500Medium",
			flex: 1,
			minWidth: 0,
			color: t.textPrimary,
			fontSize: type.subheadline.fontSize,
			fontWeight: "500",
		},
		rowValue: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.subheadline.fontSize, flexShrink: 1 },
		rowChevron: { marginRight: -3 },
		rowAccessory: { flexShrink: 0, flexDirection: "row", alignItems: "center" },

		iconBtn: {
			width: 32,
			height: 32,
			borderRadius: 8,
			borderWidth: 1,
			borderColor: t.borderDefault,
			backgroundColor: t.bgSubtle,
			alignItems: "center",
			justifyContent: "center",
		},
		iconBtnPressed: { backgroundColor: t.accentTint, borderColor: t.accent },
		iconBtnPressedDanger: { backgroundColor: t.tintRed, borderColor: t.red },

		empty: {
			flex: 1,
			alignItems: "center",
			justifyContent: "center",
			padding: space.huge,
			minHeight: 320,
		},
		emptyIcon: {
			width: 64,
			height: 64,
			borderRadius: 16,
			backgroundColor: t.bgElevated,
			borderWidth: 1,
			borderColor: t.borderSubtle,
			alignItems: "center",
			justifyContent: "center",
			marginBottom: space.lg,
		},
		emptyTitle: { fontFamily: "Geist_600SemiBold",
			color: t.textPrimary,
			fontSize: type.body.fontSize,
			fontWeight: "600",
			textAlign: "center",
		},
		emptyMsg: { fontFamily: "Geist_400Regular",
			color: t.textSecondary,
			fontSize: type.footnote.fontSize,
			lineHeight: type.footnote.lineHeight,
			textAlign: "center",
			marginTop: space.sm,
			maxWidth: 300,
		},
	});

/**
 * The shell every card in the app shares: session cards, PR cards, and the
 * orchestrator's project card.
 *
 * These three had byte-identical style blocks, each with a comment
 * acknowledging the duplication ("Matches the session card shell so a PR card and
 * a session card read as siblings"). Comments cannot keep them in step — a
 * radius changed in one place would quietly make one card a different shape
 * from its neighbours in the same scroll view. This is what those comments were
 * describing, made real.
 *
 * Spread it and add whatever a specific card needs on top:
 *   card: { ...cardShell(t), marginBottom: 0 }
 */
export function cardShell(t: Theme): ViewStyle {
	return {
		backgroundColor: t.bgElevated,
		borderRadius: 12,
		borderWidth: 1,
		borderColor: t.borderSubtle,
		paddingHorizontal: space.md,
		paddingVertical: space.md,
		marginHorizontal: space.md,
		marginVertical: space.xxs,
	};
}

/** The pressed state for a tappable cardShell. */
export function cardShellPressed(t: Theme): ViewStyle {
	return { backgroundColor: t.bgElevatedHover, borderColor: t.borderDefault };
}

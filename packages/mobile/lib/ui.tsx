import { Feather } from "@expo/vector-icons";
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
import { NativeHeaderButton, type NativeHeaderButtonIcon } from "./native-header-button";
import { useOptionalSidebarNavigation } from "./sidebar-navigation-shell";
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
	useEffect(() => {
		if (!breathing) return;
		const loop = Animated.loop(
			Animated.sequence([
				Animated.timing(pulse, {
					toValue: 0.35,
					duration: 1200,
					useNativeDriver: true,
				}),
				Animated.timing(pulse, {
					toValue: 1,
					duration: 1200,
					useNativeDriver: true,
				}),
			]),
		);
		loop.start();
		return () => loop.stop();
	}, [breathing, pulse]);

	return (
		<Animated.View
			style={{
				width: size,
				height: size,
				borderRadius: size / 2,
				backgroundColor: color,
				opacity: breathing ? pulse : 1,
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
			<Text numberOfLines={1} style={[s.pillText, active && s.pillTextActive, textStyle]}>
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
			<Text style={[s.badgeText, { color: v.color }]}>{v.label}</Text>
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
			{icon ? <Feather name={icon} size={11} color={fg} style={{ marginRight: 4 }} /> : null}
			<Text style={[s.chipText, { color: fg }, mono && { fontFamily: t.fontMono, fontSize: 11 }]} numberOfLines={1}>
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
	if (!onPress) return <View style={[s.card, style]}>{children}</View>;
	return (
		<Pressable
			onPress={() => {
				haptics.tap();
				onPress();
			}}
			style={({ pressed }) => [s.card, pressed && s.cardPressed, style]}
		>
			{children}
		</Pressable>
	);
}

export function SectionHeader({ label, color, count }: { label: string; color: string; count?: number }) {
	const s = useThemedStyles(makeStyles);
	return (
		<View style={s.sectionHeader}>
			<View style={[s.sectionBar, { backgroundColor: color }]} />
			<Text style={s.sectionLabel}>{label.toUpperCase()}</Text>
			{count !== undefined ? <Text style={s.sectionCount}>{count}</Text> : null}
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
function MascotLamp({ status }: { status?: ConnStatus }) {
	const t = useTheme();
	const s = useThemedStyles(makeStyles);
	const color = status === "open" ? t.green : status === "connecting" ? t.amber : t.textFaint;
	const lit = status === "open" || status === "connecting";
	const label = status === "open" ? "Connected" : status === "connecting" ? "Connecting" : "Offline";
	return (
		<View
			style={s.mascotWrap}
			accessible
			accessibilityRole="image"
			accessibilityLabel={status ? `AO mascot, ${label}` : "AO mascot"}
		>
			<Image source={MASCOT} style={s.mascot} resizeMode="contain" />
			{status ? (
				<>
					{/* Halo first, dot on top: RN has no boxShadow, so the glow is a
					    larger translucent circle plus a platform shadow/elevation. */}
					{lit ? <View style={[s.wandHalo, { backgroundColor: color, shadowColor: color }]} /> : null}
					<View style={s.wandTip}>
						<Dot color={color} size={7} breathing={status === "connecting"} />
					</View>
				</>
			) : null}
		</View>
	);
}

export function ScreenHeader({
	title,
	subtitle,
	left,
	right,
	status,
}: {
	title: string;
	subtitle?: string;
	/** Detail routes can supply a back action instead of the sidebar button. */
	left?: ReactNode;
	right?: ReactNode;
	/** Drives the wand-tip lamp. Omit to render the mascot with no lamp. */
	status?: ConnStatus;
}) {
	const s = useThemedStyles(makeStyles);
	const sidebar = useOptionalSidebarNavigation();
	return (
		<View style={s.screenHeader}>
			{left ?? (sidebar ? <HeaderIconButton icon="menu" label="Open navigation" onPress={sidebar.openSidebar} /> : null)}
			<View style={{ flex: 1 }}>
				<View style={s.titleRow}>
					<Text style={s.screenTitle}>{title}</Text>
					<MascotLamp status={status} />
				</View>
				{subtitle ? (
					<Text style={s.screenSubtitle} numberOfLines={1}>
						{subtitle}
					</Text>
				) : null}
			</View>
			{right}
		</View>
	);
}

export function ListSectionHeader({ label }: { label: string }) {
	const s = useThemedStyles(makeStyles);
	return (
		<View style={s.listSectionHeader}>
			<Text style={s.listSectionLabel}>{label}</Text>
			<View style={s.listSectionRule} />
		</View>
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
	const fg = isPrimary ? t.onAccent : isDanger ? t.red : t.blue;
	return (
		<Pressable
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
				pressed && { opacity: 0.8 },
				style,
			]}
		>
			{loading ? (
				<ActivityIndicator color={fg} size="small" />
			) : (
				<View style={s.btnInner}>
					{icon ? <Feather name={icon} size={15} color={fg} style={{ marginRight: 7 }} /> : null}
					<Text style={[s.btnText, { color: fg }]}>{title}</Text>
				</View>
			)}
		</Pressable>
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
	paddingHorizontal: 20,
	paddingTop: 22,
	paddingBottom: 24,
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
	const body = (
		<>
			{icon ? <Feather name={icon} size={17} color={iconColor} style={s.rowIcon} /> : null}
			<Text style={[s.rowLabel, { color: labelColor }]} numberOfLines={1}>
				{label}
			</Text>
			{right ?? (
				<>
					{loading ? <ActivityIndicator size="small" color={t.textTertiary} /> : null}
					{!loading && leading ? leading : null}
					{!loading && value ? (
						<Text style={[s.rowValue, valueColor ? { color: valueColor } : null]} numberOfLines={1}>
							{value}
						</Text>
					) : null}
					{onPress ? <Feather name="chevron-right" size={17} color={t.textFaint} style={s.rowChevron} /> : null}
				</>
			)}
		</>
	);

	if (!onPress) return <View style={[s.row, disabled && s.rowDisabled]}>{body}</View>;
	return (
		<Pressable
			disabled={disabled || loading}
			onPress={() => {
				if (destructive) haptics.warning();
				else haptics.tap();
				onPress();
			}}
			style={({ pressed }) => [s.row, pressed && s.rowPressed, (disabled || loading) && s.rowDisabled]}
		>
			{body}
		</Pressable>
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
						trackColor={{ true: t.blue, false: t.borderStrong }}
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
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			accessibilityState={{ disabled: disabled || loading }}
			disabled={disabled || loading}
			hitSlop={6}
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
	return (
		<View style={s.empty}>
			<View style={s.emptyIcon}>
				<Feather name={icon} size={26} color={t.textTertiary} />
			</View>
			<Text style={s.emptyTitle}>{title}</Text>
			{message ? <Text style={s.emptyMsg}>{message}</Text> : null}
			{action ? <View style={{ marginTop: 18 }}>{action}</View> : null}
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		listSectionHeader: {
			flexDirection: "row",
			alignItems: "center",
			gap: 10,
			paddingHorizontal: 18,
			paddingTop: 18,
			paddingBottom: 5,
		},
		listSectionLabel: { color: t.textTertiary, fontSize: 12, lineHeight: 16, fontWeight: "500" },
		listSectionRule: { flex: 1, height: StyleSheet.hairlineWidth, backgroundColor: t.borderSubtle },
		badge: { flexDirection: "row", alignItems: "center", gap: 6 },
		badgeText: { fontSize: 12, fontWeight: "600" },

		pill: {
			paddingHorizontal: 14,
			paddingVertical: 7,
			borderRadius: 20,
			borderWidth: 1,
			borderColor: t.borderDefault,
			backgroundColor: t.bgElevated,
		},
		pillActive: { backgroundColor: t.tintBlue, borderColor: t.blue },
		pillText: { color: t.textSecondary, fontSize: 13, fontWeight: "600" },
		pillTextActive: { color: t.blue },

		chip: {
			flexDirection: "row",
			alignItems: "center",
			paddingHorizontal: 8,
			paddingVertical: 3,
			borderRadius: 6,
		},
		chipText: { fontSize: 11, fontWeight: "600" },

		card: {
			backgroundColor: t.bgElevated,
			borderRadius: 12,
			borderWidth: 1,
			borderColor: t.borderSubtle,
			padding: 14,
		},
		cardPressed: {
			backgroundColor: t.bgElevatedHover,
			borderColor: t.borderDefault,
		},

		sectionHeader: {
			flexDirection: "row",
			alignItems: "center",
			paddingHorizontal: 16,
			paddingTop: 20,
			paddingBottom: 10,
			gap: 9,
		},
		sectionBar: { width: 3, height: 13, borderRadius: 2 },
		sectionLabel: {
			color: t.textSecondary,
			fontSize: 11,
			letterSpacing: 1.2,
			fontWeight: "700",
			flex: 1,
		},
		sectionCount: {
			color: t.textTertiary,
			fontSize: 12,
			fontWeight: "700",
			fontFamily: t.fontMono,
		},

		screenHeader: {
			flexDirection: "row",
			alignItems: "center",
			paddingHorizontal: 16,
			paddingTop: 8,
			paddingBottom: 10,
			gap: 12,
		},
		titleRow: { flexDirection: "row", alignItems: "center", gap: 9 },
		// Enlarged from 30x26: the wand-tip lamp replaced the "live" pill, so the tip
		// has to be big enough for the dot to actually read as a status light.
		mascotWrap: { width: 40, height: 35, marginTop: 3 },
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
		screenTitle: {
			color: t.textPrimary,
			fontSize: 26,
			fontWeight: "800",
			letterSpacing: -0.5,
		},
		screenSubtitle: { color: t.textTertiary, fontSize: 12, marginTop: 1 },
		headerIconBtn: {
			alignItems: "center",
			justifyContent: "center",
			padding: 2,
		},
		headerBadge: {
			position: "absolute",
			top: 0,
			right: 1,
			width: 9,
			height: 9,
			borderRadius: 4.5,
			backgroundColor: t.blue,
			borderWidth: 1.5,
			borderColor: t.bgBase,
		},

		btn: {
			borderRadius: 10,
			paddingVertical: 13,
			paddingHorizontal: 16,
			alignItems: "center",
		},
		btnInner: { flexDirection: "row", alignItems: "center" },
		btnPrimary: { backgroundColor: t.blue },
		btnGhost: {
			borderWidth: 1,
			borderColor: t.borderStrong,
			backgroundColor: t.bgElevated,
		},
		btnDanger: { borderColor: t.tintRed, backgroundColor: t.tintRed },
		btnText: { fontSize: 15, fontWeight: "700" },

		step: {
			flexDirection: "row",
			alignItems: "flex-start",
			gap: 13,
			paddingVertical: 14,
		},
		stepCompact: { paddingVertical: 6, alignItems: "center", gap: 11 },
		stepBadge: {
			width: 30,
			height: 30,
			borderRadius: 9,
			backgroundColor: t.bgElevated,
			borderWidth: 1,
			borderColor: t.borderSubtle,
			alignItems: "center",
			justifyContent: "center",
		},
		stepBadgeCompact: { width: 23, height: 23, borderRadius: 12 },
		stepNum: { color: t.textSecondary, fontSize: 13, fontWeight: "700" },
		stepNumCompact: { fontSize: 11 },
		stepTitle: { color: t.textPrimary, fontSize: 15, fontWeight: "700" },
		stepTitleCompact: { fontSize: 14, fontWeight: "600" },
		stepHint: {
			color: t.textTertiary,
			fontSize: 13,
			lineHeight: 19,
			marginTop: 3,
		},

		// No card, corners or grabber here — the native sheet draws all of that. The
		// top padding leaves room for the OS grabber so the title doesn't sit under it.
		// No bottom safe-area inset on either of these: the native sheet already
		// reserves room for the home indicator, and adding it again left a dead strip.
		sheetScreen: {
			backgroundColor: t.bgSurface,
			paddingHorizontal: 20,
			paddingTop: 22,
			paddingBottom: 20,
		},
		// Padding lives on the scroll content rather than a wrapper, so a scrolling
		// sheet's list can run edge to edge while its rows keep the same inset.
		sheetHeader: { paddingBottom: 2 },
		sheetTitleRow: { flexDirection: "row", alignItems: "center", gap: 12 },
		sheetTitle: {
			color: t.textPrimary,
			fontSize: 19,
			fontWeight: "800",
			letterSpacing: -0.3,
		},
		sheetSubtitle: {
			color: t.textSecondary,
			fontSize: 13,
			lineHeight: 19,
			marginTop: 5,
		},

		group: { marginBottom: 26 },
		groupTitle: {
			color: t.textTertiary,
			fontSize: 11,
			letterSpacing: 1.2,
			fontWeight: "700",
			marginBottom: 8,
			marginLeft: 4,
		},
		groupBody: {
			backgroundColor: t.bgElevated,
			borderRadius: 12,
			borderWidth: 1,
			borderColor: t.borderSubtle,
			overflow: "hidden",
		},
		groupFooter: {
			color: t.textTertiary,
			fontSize: 12,
			lineHeight: 17,
			marginTop: 8,
			marginHorizontal: 4,
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
			paddingVertical: 11,
			paddingHorizontal: 14,
			gap: 8,
		},
		rowPressed: { backgroundColor: t.bgElevatedHover },
		rowDisabled: { opacity: 0.45 },
		rowIcon: { width: 17, marginRight: 4 },
		rowLabel: {
			flex: 1,
			color: t.textPrimary,
			fontSize: 15,
			fontWeight: "500",
		},
		rowValue: { color: t.textTertiary, fontSize: 14, flexShrink: 1 },
		rowChevron: { marginRight: -3 },

		iconBtn: {
			width: 32,
			height: 32,
			borderRadius: 9,
			borderWidth: 1,
			borderColor: t.borderDefault,
			backgroundColor: t.bgSubtle,
			alignItems: "center",
			justifyContent: "center",
		},
		iconBtnPressed: { backgroundColor: t.tintBlue, borderColor: t.blue },
		iconBtnPressedDanger: { backgroundColor: t.tintRed, borderColor: t.red },

		empty: {
			flex: 1,
			alignItems: "center",
			justifyContent: "center",
			padding: 40,
			minHeight: 320,
		},
		emptyIcon: {
			width: 64,
			height: 64,
			borderRadius: 18,
			backgroundColor: t.bgElevated,
			borderWidth: 1,
			borderColor: t.borderSubtle,
			alignItems: "center",
			justifyContent: "center",
			marginBottom: 18,
		},
		emptyTitle: {
			color: t.textPrimary,
			fontSize: 17,
			fontWeight: "700",
			textAlign: "center",
		},
		emptyMsg: {
			color: t.textSecondary,
			fontSize: 13,
			lineHeight: 20,
			textAlign: "center",
			marginTop: 8,
			maxWidth: 300,
		},
	});

/**
 * The shell every card in the app shares: session cards, PR cards, and the
 * orchestrator's project card.
 *
 * These three had byte-identical style blocks, each with a comment
 * acknowledging the duplication ("Matches SessionCard's shell so a PR card and
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
		paddingHorizontal: 14,
		paddingVertical: 13,
		marginHorizontal: 12,
		marginVertical: 5,
	};
}

/** The pressed state for a tappable cardShell. */
export function cardShellPressed(t: Theme): ViewStyle {
	return { backgroundColor: t.bgElevatedHover, borderColor: t.borderDefault };
}

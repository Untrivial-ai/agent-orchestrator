import { Feather } from "./icons";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Animated, Modal, Pressable, StyleSheet, Text, View } from "react-native";
import { Gesture, GestureDetector } from "react-native-gesture-handler";
import { haptics } from "./haptics";
import { type Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { boundWorkerActionTranslation, WORKER_ACTION_REVEAL_WIDTH, resolveWorkerActionRail } from "./worker-row-swipe-model";
import { workerActionGlyph, type WorkerActionId } from "./worker-action-model";
import type { WorkerRowInteractionProps } from "./worker-row-interaction.types";
import { press, space, type } from "./tokens";

const GESTURE_DISTANCE = 16;
const LONG_PRESS_DISTANCE = 12;
const longPressDelayMs = 450;

export function WorkerRowInteraction({
	sessionId,
	enabled,
	activeSwipeId,
	children,
	rightActions,
	shellStyle,
	foregroundStyle,
	rowStyle,
	accessibilityLabel,
	accessibilityHint,
	onPress,
	actions,
	onAction,
	onSwipeOpen,
	onSwipeClose,
	onReady,
}: WorkerRowInteractionProps) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [actionsOpen, setActionsOpen] = useState(false);
	const [menuVisible, setMenuVisible] = useState(false);
	const closeRef = useRef<() => void>(() => {});
	const translationX = useRef(new Animated.Value(0)).current;
	const translationXRef = useRef(0);

	const moveTo = useCallback((value: number, animated = true) => {
		const target = boundWorkerActionTranslation(value);
		translationXRef.current = target;
		translationX.stopAnimation();
		if (!animated) {
			translationX.setValue(target);
			return;
		}
		Animated.timing(translationX, {
			toValue: target,
			duration: 180,
			useNativeDriver: true,
		}).start();
	}, [translationX]);
	const trackFinger = useCallback((value: number) => {
		moveTo(value, false);
	}, [moveTo]);

	const settleRail = useCallback((open: boolean) => {
		setActionsOpen(open);
		if (open) {
			haptics.select();
			onSwipeOpen(sessionId, () => closeRef.current());
			return;
		}
		onSwipeClose(sessionId);
	}, [onSwipeClose, onSwipeOpen, sessionId]);
	const closeActions = useCallback(() => {
		moveTo(0);
		settleRail(false);
	}, [moveTo, settleRail]);
	closeRef.current = closeActions;

	useEffect(() => {
		onReady?.(closeActions);
	}, [closeActions, onReady]);
	useEffect(() => {
		if (activeSwipeId && activeSwipeId !== sessionId) closeActions();
	}, [activeSwipeId, closeActions, sessionId]);

	const handleTap = useCallback(() => {
		if (actionsOpen) {
			closeActions();
			return;
		}
		onPress();
	}, [actionsOpen, closeActions, onPress]);
	const showMenu = useCallback(() => {
		if (actionsOpen) closeActions();
		haptics.tap();
		setMenuVisible(true);
	}, [actionsOpen, closeActions]);
	const choose = useCallback((id: WorkerActionId) => {
		setMenuVisible(false);
		onAction(id);
	}, [onAction]);

	const gesture = useMemo(() => {
		let startingOffset = 0;
		const pan = Gesture.Pan()
			.runOnJS(true)
			.activeOffsetX([-GESTURE_DISTANCE, GESTURE_DISTANCE])
			.failOffsetY([-LONG_PRESS_DISTANCE, LONG_PRESS_DISTANCE])
			.onStart(() => {
				startingOffset = translationXRef.current;
			})
			.onUpdate((event) => {
				trackFinger(startingOffset + event.translationX);
			})
			.onEnd((event) => {
				const nextTranslation = boundWorkerActionTranslation(startingOffset + event.translationX);
				const target = resolveWorkerActionRail({
					translationX: nextTranslation,
					velocityX: event.velocityX,
				});
				moveTo(target);
				settleRail(target !== 0);
			});
		const longPress = Gesture.LongPress()
			.runOnJS(true)
			.minDuration(longPressDelayMs)
			.maxDistance(LONG_PRESS_DISTANCE)
			.onStart(showMenu);
		const tap = Gesture.Tap()
			.runOnJS(true)
			.maxDistance(LONG_PRESS_DISTANCE)
			.onEnd((_event, success) => {
				if (success) handleTap();
			});
		return Gesture.Race(pan, longPress, tap);
	}, [handleTap, moveTo, settleRail, showMenu, trackFinger]);

	return (
		<>
			<View style={shellStyle}>
				<View pointerEvents={enabled ? "auto" : "none"} style={styles.actionRail}>
					{rightActions}
				</View>
				{enabled ? (
					<GestureDetector gesture={gesture}>
						<Animated.View
							// Gesture Handler requires a concrete native view here. Without this,
							// React Native can flatten the wrapper and the child tap target wins
							// when the finger is released instead of delivering the long press.
							collapsable={false}
							accessibilityRole="button"
							accessibilityLabel={accessibilityLabel}
							accessibilityHint={accessibilityHint}
							style={[foregroundStyle, rowStyle, { transform: [{ translateX: translationX }] }]}
						>
							{children}
						</Animated.View>
					</GestureDetector>
				) : (
					<View style={[foregroundStyle, rowStyle]}>{children}</View>
				)}
			</View>

			<Modal
				transparent
				visible={menuVisible}
				animationType="fade"
				onRequestClose={() => setMenuVisible(false)}
			>
				<View style={styles.modalScrim}>
					<Pressable accessibilityLabel="Dismiss worker options" onPress={() => setMenuVisible(false)} style={StyleSheet.absoluteFill} />
					<View accessibilityViewIsModal style={styles.menu}>
						<Text style={styles.menuTitle}>Worker options</Text>
						{/* A real action list now, rather than a Rename/Cancel pair. Rows are
						    48dp so they clear Material's touch minimum, and the destructive
						    one is tinted rather than separated, matching the native menu. */}
						<View style={styles.menuList}>
							{actions.map((action) => (
								<Pressable
									key={action.id}
									accessibilityRole="button"
									accessibilityLabel={action.title}
									onPress={() => choose(action.id)}
									android_ripple={{ color: action.destructive ? t.tintRed : t.accentTint }}
									style={({ pressed }) => [styles.menuRow, pressed && styles.menuButtonPressed]}
								>
									{/* A vector font, not a drawable: this list is an in-app Modal,
									    so it never reaches the bundled icons the native menu uses.
									    Pin comes from the same set as the swipe rail, tilted to
									    match it. */}
									<MenuGlyph id={action.id} color={action.destructive ? t.red : t.textSecondary} />
									<Text style={[styles.menuRowText, action.destructive && styles.menuRowTextDestructive]}>
										{action.title}
									</Text>
								</Pressable>
							))}
						</View>
						<Pressable
							accessibilityRole="button"
							accessibilityLabel="Cancel worker options"
							onPress={() => setMenuVisible(false)}
							style={({ pressed }) => [styles.menuButton, styles.cancelButton, pressed && styles.menuButtonPressed]}
						>
							<Text style={styles.cancelButtonText}>Cancel</Text>
						</Pressable>
					</View>
				</View>
			</Modal>
		</>
	);
}

/**
 * One action's icon. Pin and unpin come from the swipe rail's set and carry its
 * tilt, so the same action reads the same whichever way you reach it.
 */
function MenuGlyph({ id, color }: { id: WorkerActionId; color: string }) {
	const glyph = workerActionGlyph(id);
	return <Feather name={glyph.name} size={20} color={color} />;
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		actionRail: {
			position: "absolute",
			top: 0,
			right: 0,
			bottom: 0,
			width: WORKER_ACTION_REVEAL_WIDTH,
			backgroundColor: t.bgElevated,
			borderLeftWidth: StyleSheet.hairlineWidth,
			borderLeftColor: t.borderSubtle,
		},
		modalScrim: {
			flex: 1,
			alignItems: "center",
			justifyContent: "center",
			padding: space.xxl,
			backgroundColor: "rgba(0, 0, 0, 0.58)",
		},
		menu: {
			width: "100%",
			maxWidth: 340,
			padding: space.xl,
			gap: space.sm,
			borderRadius: 20,
			borderWidth: StyleSheet.hairlineWidth,
			borderColor: t.borderDefault,
			backgroundColor: t.bgElevated,
			elevation: 12,
		},
		menuTitle: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.title3.fontSize, lineHeight: type.title3.lineHeight, fontWeight: "600" },
		menuList: { marginTop: space.xs, marginHorizontal: -8 },
		menuRow: { minHeight: 48, paddingHorizontal: space.sm, flexDirection: "row", alignItems: "center", gap: space.md, borderRadius: 8 },
		menuRowText: { fontFamily: "Geist_500Medium", color: t.textPrimary, fontSize: type.callout.fontSize, lineHeight: type.callout.lineHeight, fontWeight: "500" },
		menuRowTextDestructive: { color: t.red },
		menuButton: { minHeight: 42, paddingHorizontal: space.lg, borderRadius: 20, alignItems: "center", justifyContent: "center", marginTop: space.sm, alignSelf: "flex-end" },
		cancelButton: { backgroundColor: t.bgElevatedHover, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault },
		cancelButtonText: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight, fontWeight: "600" },
		menuButtonPressed: { opacity: 0.76 },
	});

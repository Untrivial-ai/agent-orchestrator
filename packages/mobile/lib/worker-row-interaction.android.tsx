import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Animated, Modal, Pressable, StyleSheet, Text, View } from "react-native";
import { Gesture, GestureDetector } from "react-native-gesture-handler";
import { haptics } from "./haptics";
import { type Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { boundWorkerActionTranslation, WORKER_ACTION_REVEAL_WIDTH, resolveWorkerActionRail } from "./worker-row-swipe-model";
import type { WorkerRowInteractionProps } from "./worker-row-interaction.types";

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
	onRenameRequest,
	onSwipeOpen,
	onSwipeClose,
	onReady,
}: WorkerRowInteractionProps) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [actionsOpen, setActionsOpen] = useState(false);
	const [renameMenuVisible, setRenameMenuVisible] = useState(false);
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
	const showRenameMenu = useCallback(() => {
		if (actionsOpen) closeActions();
		haptics.tap();
		setRenameMenuVisible(true);
	}, [actionsOpen, closeActions]);
	const chooseRename = useCallback(() => {
		setRenameMenuVisible(false);
		onRenameRequest();
	}, [onRenameRequest]);

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
			.onStart(showRenameMenu);
		const tap = Gesture.Tap()
			.runOnJS(true)
			.maxDistance(LONG_PRESS_DISTANCE)
			.onEnd((_event, success) => {
				if (success) handleTap();
			});
		return Gesture.Race(pan, longPress, tap);
	}, [handleTap, moveTo, settleRail, showRenameMenu, trackFinger]);

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
				visible={renameMenuVisible}
				animationType="fade"
				onRequestClose={() => setRenameMenuVisible(false)}
			>
				<View style={styles.modalScrim}>
					<Pressable accessibilityLabel="Dismiss worker options" onPress={() => setRenameMenuVisible(false)} style={StyleSheet.absoluteFill} />
					<View accessibilityViewIsModal style={styles.menu}>
						<Text style={styles.menuTitle}>Worker options</Text>
						<Text numberOfLines={1} style={styles.menuDescription}>Rename this worker in place.</Text>
						<View style={styles.menuActions}>
							<Pressable
								accessibilityRole="button"
								accessibilityLabel="Rename worker"
								onPress={chooseRename}
								style={({ pressed }) => [styles.menuButton, styles.renameButton, pressed && styles.menuButtonPressed]}
							>
								<Text style={styles.renameButtonText}>Rename</Text>
							</Pressable>
							<Pressable
								accessibilityRole="button"
								accessibilityLabel="Cancel worker options"
								onPress={() => setRenameMenuVisible(false)}
								style={({ pressed }) => [styles.menuButton, styles.cancelButton, pressed && styles.menuButtonPressed]}
							>
								<Text style={styles.cancelButtonText}>Cancel</Text>
							</Pressable>
						</View>
					</View>
				</View>
			</Modal>
		</>
	);
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
			padding: 24,
			backgroundColor: "rgba(0, 0, 0, 0.58)",
		},
		menu: {
			width: "100%",
			maxWidth: 340,
			padding: 20,
			gap: 8,
			borderRadius: 24,
			borderWidth: StyleSheet.hairlineWidth,
			borderColor: t.borderDefault,
			backgroundColor: t.bgElevated,
			elevation: 12,
		},
		menuTitle: { color: t.textPrimary, fontSize: 20, lineHeight: 25, fontWeight: "700" },
		menuDescription: { color: t.textSecondary, fontSize: 14, lineHeight: 20 },
		menuActions: { flexDirection: "row", justifyContent: "flex-end", gap: 10, marginTop: 12 },
		menuButton: { minHeight: 42, paddingHorizontal: 16, borderRadius: 21, alignItems: "center", justifyContent: "center" },
		renameButton: { backgroundColor: t.blue },
		cancelButton: { backgroundColor: t.bgElevatedHover, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault },
		renameButtonText: { color: t.onAccent, fontSize: 14, lineHeight: 18, fontWeight: "700" },
		cancelButtonText: { color: t.textPrimary, fontSize: 14, lineHeight: 18, fontWeight: "600" },
		menuButtonPressed: { opacity: 0.76 },
	});

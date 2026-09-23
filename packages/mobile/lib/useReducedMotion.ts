import { useEffect, useState } from "react";
import { AccessibilityInfo } from "react-native";

/**
 * Whether the user has asked the OS to reduce motion.
 *
 * Lifted verbatim from the two sidebar shells, which each carried an identical
 * copy of this effect and were the only things in the app that honoured the
 * setting at all. Everything else — most visibly `Dot`, which loops forever —
 * animated regardless.
 *
 * Live, not read-once: the subscription matters because a user who turns the
 * setting on while the app is foregrounded expects it to take effect without
 * relaunching. The initial read is guarded by `mounted` because it resolves a
 * promise that can outlive the component.
 *
 * The pure decisions that consume this live in motion.ts, so the rules stay
 * unit-testable without a React Native runtime — the same split as pushStatus.ts
 * against the screens that read it.
 */
export function useReducedMotion(): boolean {
	const [reduceMotion, setReduceMotion] = useState(false);

	useEffect(() => {
		let mounted = true;
		void AccessibilityInfo.isReduceMotionEnabled()
			.then((enabled) => {
				if (mounted) setReduceMotion(enabled);
			})
			.catch(() => {});
		const subscription = AccessibilityInfo.addEventListener("reduceMotionChanged", setReduceMotion);
		return () => {
			mounted = false;
			subscription.remove();
		};
	}, []);

	return reduceMotion;
}

import type { ReactNode } from "react";
import Animated, { FadeIn, FadeOut, LinearTransition } from "react-native-reanimated";

import { ROW_ENTER_MS, ROW_MOVE_MS } from "./motion";
import { useReducedMotion } from "./useReducedMotion";

/**
 * Animates a board row as it moves, arrives or leaves.
 *
 * Pinning a worker used to read as a glitch: the row vanished from its section
 * and materialised in Pinned, with nothing connecting the two. The same applied
 * to a session promoted by a delivery event while you were looking at the board.
 *
 * The move is the important part, and it works because the row's key —
 * `projectId:id` — is unchanged when it changes section. React sees the same
 * element in a new position, so LinearTransition animates a real move rather
 * than an unmount/remount pair. Entering and exiting only cover rows genuinely
 * appearing or disappearing (a new worker, a deleted one), and are deliberately
 * shorter so they never outlast the movement they accompany.
 *
 * Returns children unwrapped under Reduce Motion: a zero-duration layout
 * animation still schedules work every frame, and the setting asks for no
 * movement rather than instant movement.
 */
export function BoardRowTransition({ children }: { children: ReactNode }) {
	const reduceMotion = useReducedMotion();
	if (reduceMotion) return <>{children}</>;

	return (
		<Animated.View
			layout={LinearTransition.duration(ROW_MOVE_MS)}
			entering={FadeIn.duration(ROW_ENTER_MS)}
			exiting={FadeOut.duration(ROW_ENTER_MS)}
		>
			{children}
		</Animated.View>
	);
}

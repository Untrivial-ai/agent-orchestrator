import type { NativeStackHeaderItemCustom, NativeStackNavigationOptions } from "expo-router";
import type { ReactElement } from "react";
import { Platform } from "react-native";

type HeaderSide = "left" | "right";

/**
 * Where a *glass* control goes when it lives in a native header.
 *
 * iOS 26 gives every bar-button item a shared glass background of its own, and
 * that is the correct default: it is what makes a system back button and a
 * system text button read as one material. Our controls are the exception. They
 * *are* the material — `glassCircle` on a `Group` sized to the same 44pt — so
 * the system background landed under ours and the control came out wearing two
 * outlines, a squarer one outside a rounder one, visibly larger and duller than
 * the identical button on the home page.
 *
 * `hidesSharedBackground` is that background's off switch on iOS 26 and later.
 * It only exists on the header-items API, so glass controls are declared as
 * items there. Below iOS 26 the flag is ignored, which is what we want: there is
 * no shared background to hide, and the button keeps its own material.
 *
 * Android has no shared background at all — its header controls are ordinary
 * pressables — so it keeps the plain `headerLeft`/`headerRight` slots and this
 * helper only picks the platform's spelling.
 *
 * Pass no element to vacate the slot; native-stack holds the previous item for a
 * frame otherwise, which is what `resetHeaderRightForSwap` exists to work
 * around.
 */
export function glassHeaderControl(
	side: HeaderSide,
	element?: ReactElement,
): NativeStackNavigationOptions {
	if (Platform.OS !== "ios") {
		return side === "right"
			? { headerRight: element ? () => element : undefined }
			: { headerLeft: element ? () => element : undefined };
	}
	// An empty item list is not "no items" to native-stack — it is a list that
	// renders nothing, and it outranks `headerRight`/`headerLeft`. Vacating the
	// slot has to clear the items outright, or a screen that swaps a glass
	// control for a plain one (the terminal's browser toggles) would be left with
	// a blank bar.
	if (!element) {
		return side === "right"
			? { headerRight: undefined, unstable_headerRightItems: undefined }
			: { headerLeft: undefined, unstable_headerLeftItems: undefined };
	}
	const items = (): NativeStackHeaderItemCustom[] =>
		[{ type: "custom", element, hidesSharedBackground: true }];
	if (side === "right") {
		return { headerRight: undefined, unstable_headerRightItems: items };
	}
	// Our control replaces the back button rather than joining it. Left to its
	// default the bar would keep the system chevron and draw ours beside it,
	// because the item API does not imply the replacement the way `headerLeft`
	// does.
	return {
		headerLeft: undefined,
		headerBackVisible: false,
		unstable_headerLeftItems: items,
	};
}

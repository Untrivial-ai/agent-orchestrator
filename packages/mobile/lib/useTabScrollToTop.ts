import { useScrollToTop } from "expo-router/react-navigation";
import { type RefObject, useRef } from "react";

// The ref shape the hook accepts (ScrollView, FlatList, SectionList, ...).
// It isn't exported, so derive it from the hook itself.
type ScrollableRef = Parameters<typeof useScrollToTop>[0];

/**
 * Ref for a tab screen's scroll container, wired so that tapping the
 * already-active tab scrolls it back to the top - the standard iOS tab-bar
 * gesture. Pressing a *different* tab just switches, as usual.
 *
 * Attach the returned ref to the screen's scroll container:
 *
 *     const scrollRef = useTabScrollToTop<ScrollView>();
 *     <ScrollView ref={scrollRef} ... />
 *
 * This is also the single place we reach into expo-router's react-navigation
 * entry point, keeping that coupling in one file. SDK 56+ forbids importing
 * `@react-navigation/*` directly; `expo-router/react-navigation` is the
 * supported re-export.
 */
export function useTabScrollToTop<T>(): RefObject<T | null> {
	const ref = useRef<T>(null);
	// Callers always pass a scrollable; the generic keeps the JSX ref precise.
	useScrollToTop(ref as ScrollableRef);
	return ref;
}

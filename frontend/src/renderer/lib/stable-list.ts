/**
 * Object identity for lists that are rebuilt from JSON.
 *
 * A polled endpoint hands back a new object for every entry on every refetch,
 * even when nothing changed. React's `memo` compares props by identity, so a
 * surface fed that way re-renders in full on a timer no matter how carefully its
 * children are memoized.
 *
 * These restore identity — and only identity. An entry is replaced by the
 * previous object solely when the two compare structurally equal, so no value is
 * ever preserved, cached, or overridden. The server's answer still decides
 * everything; the substitution is invisible except to `===`.
 */

import { useMemo, useRef } from "react";

/**
 * The list, with unchanged entries carrying their previous object identity.
 *
 * `keyOf` and `equal` must be stable references — module-level functions, not
 * closures — or the memo they feed is rebuilt on every render.
 */
export function useStableList<T>(
	list: T[],
	keyOf: (entry: T) => string,
	equal: (a: T, b: T) => boolean,
): T[] {
	const seen = useRef(new Map<string, T>());
	const previousList = useRef<T[] | undefined>(undefined);
	return useMemo(() => {
		const next = new Map<string, T>();
		const stable = list.map((entry) => {
			const before = seen.current.get(keyOf(entry));
			const kept = before !== undefined && equal(before, entry) ? before : entry;
			next.set(keyOf(kept), kept);
			return kept;
		});
		// Entries that dropped out of the list are dropped here too, so a long-lived
		// surface does not accumulate the whole history of everything it has shown.
		seen.current = next;
		// A fresh array invalidates downstream memo dependencies even when each row
		// retained identity. Reuse the old array for a wholly unchanged list too.
		const previous = previousList.current;
		const result =
			previous &&
			previous.length === stable.length &&
			previous.every((entry, index) => entry === stable[index])
				? previous
				: stable;
		previousList.current = result;
		return result;
	}, [list, keyOf, equal]);
}

/**
 * Structural equality, walked rather than hand-listed per field.
 *
 * The intended inputs are JSON-shaped: primitives, plain objects, arrays. A walk
 * is therefore total by construction, where a hand-written field comparison would
 * be faster and would go stale the first time a shape grows a field — which shows
 * up as a row that silently stops updating.
 */
export function sameContent(a: unknown, b: unknown): boolean {
	if (a === b) return true;
	if (typeof a !== "object" || typeof b !== "object" || a === null || b === null) return false;
	if (Array.isArray(a) || Array.isArray(b)) {
		if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false;
		return a.every((entry, index) => sameContent(entry, b[index]));
	}
	const keys = Object.keys(a);
	if (keys.length !== Object.keys(b).length) return false;
	return keys.every((key) =>
		sameContent((a as Record<string, unknown>)[key], (b as Record<string, unknown>)[key]),
	);
}

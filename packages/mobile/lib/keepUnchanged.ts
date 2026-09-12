/**
 * Deep equality for the daemon's JSON read model.
 *
 * Deep rather than a field list like `sameServerConfig`: a `DashboardSession`
 * carries two dozen fields plus a nested PR, and a hand-written comparison goes
 * blind to the next field someone adds — dropping a real update. Everything
 * compared here came from `JSON.parse` and a pure mapping, so there are no
 * functions, Dates or cycles to handle.
 */
export function sameJson(a: unknown, b: unknown): boolean {
	if (Object.is(a, b)) return true;
	if (a === null || b === null || typeof a !== "object" || typeof b !== "object") return false;
	if (Array.isArray(a) !== Array.isArray(b)) return false;
	if (Array.isArray(a) && Array.isArray(b)) {
		if (a.length !== b.length) return false;
		for (let i = 0; i < a.length; i += 1) {
			if (!sameJson(a[i], b[i])) return false;
		}
		return true;
	}
	const left = a as Record<string, unknown>;
	const right = b as Record<string, unknown>;
	const keys = Object.keys(left);
	if (keys.length !== Object.keys(right).length) return false;
	for (const key of keys) {
		// A present `undefined` and an absent key count as different; both sides
		// come from the same mapper, so equal input cannot differ that way.
		if (!Object.prototype.hasOwnProperty.call(right, key)) return false;
		if (!sameJson(left[key], right[key])) return false;
	}
	return true;
}

/**
 * The previous value when the new one carries the same data, so state that is
 * re-fetched on a timer only changes identity when something moved:
 *
 *     setSessions((prev) => keepUnchanged(prev, next));
 */
export function keepUnchanged<T>(prev: T, next: T): T {
	return sameJson(prev, next) ? prev : next;
}

import { realpathSync } from "node:fs";
import path from "node:path";

export function canonicalPathKey(value: string): string {
	const resolved = path.resolve(value);
	let existing = resolved;
	const missing: string[] = [];

	for (;;) {
		try {
			const canonical = path.join(realpathSync.native(existing), ...missing);
			return process.platform === "win32" ? canonical.toLowerCase() : canonical;
		} catch {
			const parent = path.dirname(existing);
			if (parent === existing) {
				return process.platform === "win32" ? resolved.toLowerCase() : resolved;
			}
			missing.unshift(path.basename(existing));
			existing = parent;
		}
	}
}

export function sameCanonicalPath(a: string, b: string): boolean {
	return canonicalPathKey(a) === canonicalPathKey(b);
}

export function canonicalPathInside(child: string, parent: string): boolean {
	const childKey = canonicalPathKey(child);
	const parentKey = canonicalPathKey(parent);
	return childKey === parentKey || childKey.startsWith(parentKey + path.sep);
}

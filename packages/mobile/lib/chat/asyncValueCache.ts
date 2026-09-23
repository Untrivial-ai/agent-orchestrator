type CachedValue<V> = { value: V; expiresAt: number };
type PendingValue<V> = { promise: Promise<V> };

export type AsyncValueCache<K, V> = {
	load(key: K, loader: () => Promise<V>): Promise<V>;
	set(key: K, value: V): void;
	delete(key: K): void;
};

// This stays local intentionally: Chat needs only a bounded TTL map with
// in-flight request sharing and explicit invalidation. Pulling in a query-cache
// framework for those four semantics would add app-wide lifecycle behavior the
// callers neither configure nor use.
export function createAsyncValueCache<K, V>(
	maxEntries: number,
	ttlMs: number,
	now: () => number = Date.now,
): AsyncValueCache<K, V> {
	const entries = new Map<K, CachedValue<V> | PendingValue<V>>();
	const limit = Math.max(1, maxEntries);
	const ttl = Math.max(0, ttlMs);

	const touch = (key: K, entry: CachedValue<V> | PendingValue<V>) => {
		entries.delete(key);
		entries.set(key, entry);
	};
	const trim = () => {
		while (entries.size > limit) {
			const oldest = entries.keys().next().value as K | undefined;
			if (oldest === undefined) return;
			entries.delete(oldest);
		}
	};
	const set = (key: K, value: V) => {
		touch(key, { value, expiresAt: now() + ttl });
		trim();
	};

	return {
		load(key, loader) {
			const current = entries.get(key);
			if (current && "promise" in current) {
				touch(key, current);
				return current.promise;
			}
			if (current && current.expiresAt > now()) {
				touch(key, current);
				return Promise.resolve(current.value);
			}
			entries.delete(key);
			let promise: Promise<V>;
			try {
				promise = loader();
			} catch (error) {
				promise = Promise.reject(error);
			}
			const pending: PendingValue<V> = { promise };
			touch(key, pending);
			trim();
			return promise.then(
				(value) => {
					if (entries.get(key) === pending) set(key, value);
					return value;
				},
				(error) => {
					if (entries.get(key) === pending) entries.delete(key);
					throw error;
				},
			);
		},
		set,
		delete(key) {
			entries.delete(key);
		},
	};
}

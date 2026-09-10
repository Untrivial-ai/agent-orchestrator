export type RequestGate = {
	begin(): number;
	isCurrent(request: number): boolean;
	invalidate(): void;
};

/** Prevent an older asynchronous request from publishing after a newer one. */
export function createRequestGate(): RequestGate {
	let current = 0;
	return {
		begin: () => ++current,
		isCurrent: (request) => request === current,
		invalidate: () => { current += 1; },
	};
}

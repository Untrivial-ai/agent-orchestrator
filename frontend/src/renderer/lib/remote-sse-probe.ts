// An opened EventSource only proves that response headers arrived. A proxy may
// buffer the body indefinitely, so remote streams must deliver a frame before
// they replace the REST polling fallback.
export function probeRemoteSse(
	url: string,
	eventNames: readonly string[],
	onEvent: (event: Event) => void,
	onVerified: () => void,
	onFailure: () => void,
): () => void {
	let source: EventSource | undefined;
	let closed = false;
	let verified = false;
	let timeout: ReturnType<typeof setTimeout> | undefined;
	const close = () => {
		closed = true;
		if (timeout) clearTimeout(timeout);
		source?.close();
	};
	const fail = () => {
		if (closed) return;
		close();
		onFailure();
	};
	const verify = () => {
		if (closed || verified) return;
		verified = true;
		if (timeout) clearTimeout(timeout);
		onVerified();
	};
	try {
		if (typeof EventSource === "undefined") throw new Error("EventSource unavailable");
		source = new EventSource(url);
		const delivered = (event: Event) => {
			if (closed) return;
			verify();
			onEvent(event);
		};
		source.onmessage = delivered;
		for (const name of eventNames) source.addEventListener(name, delivered);
		for (const name of ["ready", "cursor", "heartbeat"]) source.addEventListener(name, verify);
		source.onerror = fail;
		timeout = setTimeout(fail, 15_000);
	} catch {
		fail();
	}
	return close;
}

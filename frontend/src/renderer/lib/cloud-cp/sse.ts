// Incremental server-sent-events frame parser for the control plane's
// `/sessions/{id}/events` stream. The stream is consumed with fetch +
// ReadableStream rather than EventSource because EventSource cannot attach an
// Authorization header. This module is a pure text parser: feed it decoded
// chunks in arrival order and it yields complete frames, buffering partial
// frames across chunk boundaries.
//
// Understands the subset of the SSE wire format the control plane emits
// (`event_handlers.go` writeSSEEvent): `id`, `event`, and `data` fields,
// `: keepalive` comment lines, and `retry:` hints (ignored). Multi-line
// `data:` fields are joined with newlines per the SSE specification.

export interface CloudCpSseFrame {
	/** Last-Event-ID value; the control plane sets it to the event sequence. */
	id?: string;
	/** Event name; the control plane sets it to the client-event type. */
	event?: string;
	/** Concatenated data payload (JSON-encoded clientEventResponse on this API). */
	data: string;
}

export interface CloudCpSseFrameParser {
	/** Consume one decoded chunk; returns every frame the chunk completed. */
	push(chunk: string): CloudCpSseFrame[];
	/** Drain any final unterminated frame once the stream has ended. */
	flush(): CloudCpSseFrame[];
}

function parseBlock(block: string): CloudCpSseFrame | null {
	let id: string | undefined;
	let event: string | undefined;
	const data: string[] = [];
	for (const line of block.split("\n")) {
		// Comment lines (the server's keepalives) and blank lines carry nothing.
		if (line === "" || line.startsWith(":")) continue;
		const colon = line.indexOf(":");
		const field = colon === -1 ? line : line.slice(0, colon);
		let value = colon === -1 ? "" : line.slice(colon + 1);
		if (value.startsWith(" ")) value = value.slice(1);
		if (field === "id") {
			id = value;
		} else if (field === "event") {
			event = value;
		} else if (field === "data") {
			data.push(value);
		}
		// "retry" and unknown fields are intentionally ignored in v0.
	}
	// A frame with no data (e.g. a lone "retry: 2000" block) dispatches nothing.
	if (data.length === 0) return null;
	return { id, event, data: data.join("\n") };
}

export function createSseFrameParser(): CloudCpSseFrameParser {
	let buffer = "";

	return {
		push(chunk: string): CloudCpSseFrame[] {
			buffer += chunk;
			// Hold back a trailing CR so a CRLF pair split across chunks still
			// normalizes as one line break instead of two.
			let normalized = buffer;
			let heldCr = "";
			if (normalized.endsWith("\r")) {
				heldCr = "\r";
				normalized = normalized.slice(0, -1);
			}
			normalized = normalized.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
			const blocks = normalized.split("\n\n");
			// The final piece is an incomplete frame (often ""): keep buffering it.
			buffer = blocks.pop()! + heldCr;
			const frames: CloudCpSseFrame[] = [];
			for (const block of blocks) {
				const frame = parseBlock(block);
				if (frame !== null) frames.push(frame);
			}
			return frames;
		},

		flush(): CloudCpSseFrame[] {
			const rest = buffer.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
			buffer = "";
			if (rest === "") return [];
			const frame = parseBlock(rest);
			return frame === null ? [] : [frame];
		},
	};
}

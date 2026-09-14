import { forwardRef, useEffect, useImperativeHandle, useRef, useState, type PointerEvent, type RefObject } from "react";
import type { LocalDevicePlatform } from "../../shared/local-device";

export type DeviceStreamHandle = {
	press: (button: "back" | "home" | "enter") => void;
	sendText: (text: string) => void;
};

type Props = {
	baseUrl: string;
	screenLabel: string;
	platform: LocalDevicePlatform;
	onError: (message: string) => void;
	onReady: () => void;
};

type Gesture = { pointerId: number; x: number; y: number };

export const DeviceStream = forwardRef<DeviceStreamHandle, Props>(function DeviceStream(
	{ baseUrl, screenLabel, platform, onError, onReady },
	ref,
) {
	const socketRef = useRef<WebSocket | undefined>(undefined);
	const canvasRef = useRef<HTMLCanvasElement>(null);
	const gestureRef = useRef<Gesture | undefined>(undefined);
	const [iosMJPEG, setIOSMJPEG] = useState(false);

	useEffect(() => {
		let stopped = false;
		let decoder: VideoDecoder | undefined;
		let awaitingAndroidKeyframe = platform === "android";
		let androidDecoderFailures = 0;
		let lastAndroidKeyframeRequest = Number.NEGATIVE_INFINITY;
		let decodeChain = Promise.resolve();
		const streamAbort = new AbortController();
		const socket = new WebSocket(`${baseUrl.replace(/^http/, "ws")}/input`);
		socket.binaryType = "arraybuffer";
		socketRef.current = socket;
		const requestAndroidKeyframe = () => {
			const now = performance.now();
			if (now - lastAndroidKeyframeRequest < 400) return;
			lastAndroidKeyframeRequest = now;
			if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: "reset-video", ack: false }));
		};
		const resetAndroidDecoder = () => {
			const current = decoder;
			decoder = undefined;
			awaitingAndroidKeyframe = true;
			if (current?.state !== "closed") current?.close();
		};

		if (platform === "ios") {
			setIOSMJPEG(false);
			if (globalThis.VideoDecoder) {
				void streamIOSAVCC(`${baseUrl}/avcc`, canvasRef, streamAbort.signal, (value) => { decoder = value; }, () => setIOSMJPEG(true))
					.catch(() => { if (!streamAbort.signal.aborted) setIOSMJPEG(true); });
			} else {
				setIOSMJPEG(true);
			}
			socket.addEventListener("open", () => {
				sendTagged(socket, 0x0d, { enabled: false });
				sendTagged(socket, 0x04, { button: "home" });
				onReady();
			});
		} else {
			socket.addEventListener("open", () => {
				requestAndroidKeyframe();
				onReady();
			});
			socket.addEventListener("message", (event) => {
				if (typeof event.data === "string") {
					try {
						const message = JSON.parse(event.data) as { type?: string; ok?: boolean; error?: string };
						if (message.type === "video-session") {
							resetAndroidDecoder();
							requestAndroidKeyframe();
						}
						if (message.ok === false && message.error) onError(`Android device control failed: ${message.error}`);
					} catch { /* Ignore non-protocol diagnostics. */ }
					return;
				}
				decodeChain = decodeChain.then(async () => {
					if (stopped) return;
					const packet = event.data instanceof Blob ? await event.data.arrayBuffer() : event.data;
					if (!(packet instanceof ArrayBuffer)) return;
					const parsed = parseAndroidFrame(packet);
					if (!parsed) return;
					const Decoder = globalThis.VideoDecoder;
					if (!Decoder) throw new Error("This AO build does not support Android hardware video decoding.");
					if (!decoder) {
						const codec = avcCodec(parsed.data);
						if (!codec) {
							requestAndroidKeyframe();
							return;
						}
						const nextDecoder = new Decoder({
							output: (frame) => {
								if (decoder !== nextDecoder) {
									frame.close();
									return;
								}
								const canvas = canvasRef.current;
								if (canvas) {
									canvas.width = frame.displayWidth;
									canvas.height = frame.displayHeight;
									canvas.getContext("2d")?.drawImage(frame, 0, 0);
								}
								androidDecoderFailures = 0;
								frame.close();
							},
							error: (cause) => {
								if (decoder !== nextDecoder) return;
								decoder = undefined;
								awaitingAndroidKeyframe = true;
								androidDecoderFailures++;
								requestAndroidKeyframe();
								if (androidDecoderFailures >= 3) onError(`Android video decoder failed: ${cause.message}`);
							},
						});
						// Some Android emulator profiles are software-decodable but rejected
						// when Chromium is forced onto VideoToolbox hardware decoding.
						nextDecoder.configure({ codec, optimizeForLatency: true, hardwareAcceleration: "no-preference" });
						decoder = nextDecoder;
					}
					if (decoder.state !== "configured") return;
					if (awaitingAndroidKeyframe) {
						if (!parsed.key) return;
						awaitingAndroidKeyframe = false;
					}
					// Keep latency bounded under renderer load. A later keyframe catches
					// the canvas up without tearing down the codec configuration.
					if (decoder.decodeQueueSize > 8) {
						awaitingAndroidKeyframe = true;
						requestAndroidKeyframe();
						return;
					}
					try {
						decoder.decode(new EncodedVideoChunk({
							type: parsed.key ? "key" : "delta",
							timestamp: parsed.timestamp,
							data: parsed.data,
						}));
					} catch (cause) {
						resetAndroidDecoder();
						androidDecoderFailures++;
						requestAndroidKeyframe();
						if (androidDecoderFailures >= 3) onError(`Android video decoder failed: ${errorMessage(cause)}`);
					}
				}).catch((cause) => onError(errorMessage(cause)));
			});
		}
		socket.addEventListener("error", () => onError(`${platform === "ios" ? "iOS" : "Android"} device stream disconnected.`));
		return () => {
			stopped = true;
			streamAbort.abort();
			socket.close();
			if (decoder?.state !== "closed") decoder?.close();
			if (socketRef.current === socket) socketRef.current = undefined;
		};
	}, [baseUrl, onError, onReady, platform]);

	useImperativeHandle(ref, () => ({
		press(button) {
			const socket = socketRef.current;
			if (!socket || socket.readyState !== WebSocket.OPEN) return;
			if (platform === "ios") {
				if (button === "home") sendTagged(socket, 0x04, { button: "home" });
				if (button === "enter") sendIOSKey(socket, 40);
				return;
			}
			socket.send(JSON.stringify(button === "enter" ? { type: "key", keycode: 66 } : { type: button }));
		},
		sendText(text) {
			const socket = socketRef.current;
			if (!socket || socket.readyState !== WebSocket.OPEN) return;
			if (platform === "android") {
				socket.send(JSON.stringify({ type: "text", text }));
				return;
			}
			for (const character of text) sendIOSCharacter(socket, character);
		},
	}), [platform]);

	const point = (element: HTMLElement, event: PointerEvent<HTMLElement>) => {
		const rect = element.getBoundingClientRect();
		return {
			x: Math.max(0, Math.min(1, (event.clientX - rect.left) / rect.width)),
			y: Math.max(0, Math.min(1, (event.clientY - rect.top) / rect.height)),
		};
	};
	const touch = (phase: "begin" | "move" | "end", event: PointerEvent<HTMLElement>) => {
		const socket = socketRef.current;
		if (!socket || socket.readyState !== WebSocket.OPEN) return;
		const position = point(event.currentTarget, event);
		if (platform === "ios") sendTagged(socket, 0x03, { type: phase, ...position });
		else socket.send(JSON.stringify({ type: "touch", action: phase === "begin" ? "down" : phase === "end" ? "up" : "move", ...position }));
	};
	const pointerDown = (event: PointerEvent<HTMLElement>) => {
		event.currentTarget.setPointerCapture(event.pointerId);
		const position = point(event.currentTarget, event);
		gestureRef.current = { pointerId: event.pointerId, ...position };
		touch("begin", event);
	};
	const pointerMove = (event: PointerEvent<HTMLElement>) => {
		if (gestureRef.current?.pointerId === event.pointerId) touch("move", event);
	};
	const pointerEnd = (event: PointerEvent<HTMLElement>) => {
		if (gestureRef.current?.pointerId !== event.pointerId) return;
		touch("end", event);
		gestureRef.current = undefined;
	};
	const pointerCancel = (event: PointerEvent<HTMLElement>) => {
		if (gestureRef.current?.pointerId === event.pointerId) {
			touch("end", event);
			gestureRef.current = undefined;
		}
	};
	const pointerProps = {
		onPointerCancel: pointerCancel,
		onPointerDown: pointerDown,
		onPointerMove: pointerMove,
		onPointerUp: pointerEnd,
	};
	const className = "mx-auto block max-h-full max-w-full cursor-crosshair touch-none select-none object-contain";
	return platform === "ios" && iosMJPEG ? (
		<img {...pointerProps} alt={screenLabel} className={className} draggable={false} onError={() => onError("iOS device stream disconnected.")} src={`${baseUrl}/mjpeg`} />
	) : (
		<canvas {...pointerProps} aria-label={screenLabel} className={className} ref={canvasRef} role="img" />
	);
});

async function streamIOSAVCC(
	url: string,
	canvasRef: RefObject<HTMLCanvasElement | null>,
	signal: AbortSignal,
	onDecoder: (decoder: VideoDecoder) => void,
	onDecoderError: () => void,
) {
	const response = await fetch(url, { cache: "no-store", signal });
	if (!response.ok || !response.body) throw new Error(`iOS H.264 stream failed (${response.status})`);
	const reader = response.body.getReader();
	let buffer = new Uint8Array(0);
	let decoder: VideoDecoder | undefined;
	let timestamp = 0;
	for (;;) {
		const { done, value } = await reader.read();
		if (done) throw new Error("iOS H.264 stream ended");
		if (value?.length) {
			const next = new Uint8Array(buffer.length + value.length);
			next.set(buffer);
			next.set(value, buffer.length);
			buffer = next;
		}
		let offset = 0;
		while (buffer.length - offset >= 4) {
			const length = new DataView(buffer.buffer, buffer.byteOffset + offset, 4).getUint32(0);
			if (length < 1) { offset += 4; continue; }
			if (buffer.length - offset - 4 < length) break;
			const tag = buffer[offset + 4];
			const payload = buffer.slice(offset + 5, offset + 4 + length);
			offset += 4 + length;
			if (tag === 1) {
				if (payload.length < 4) continue;
				decoder = new VideoDecoder({
					output: (frame) => {
						const canvas = canvasRef.current;
						if (canvas) {
							canvas.width = frame.displayWidth;
							canvas.height = frame.displayHeight;
							canvas.getContext("2d")?.drawImage(frame, 0, 0);
						}
						frame.close();
					},
					error: onDecoderError,
				});
				decoder.configure({ codec: avcCodecDescription(payload), description: payload, optimizeForLatency: true, hardwareAcceleration: "prefer-hardware" });
				onDecoder(decoder);
			} else if ((tag === 2 || tag === 3) && decoder?.state === "configured") {
				if (decoder.decodeQueueSize <= 8 || tag === 2) {
					decoder.decode(new EncodedVideoChunk({ type: tag === 2 ? "key" : "delta", timestamp, data: payload }));
				}
				timestamp += 16_667;
			} else if (tag === 4 && typeof createImageBitmap === "function") {
				const bitmap = await createImageBitmap(new Blob([payload], { type: "image/jpeg" }));
				const canvas = canvasRef.current;
				if (canvas) {
					canvas.width = bitmap.width;
					canvas.height = bitmap.height;
					canvas.getContext("2d")?.drawImage(bitmap, 0, 0);
				}
				bitmap.close();
			}
		}
		if (offset) buffer = buffer.slice(offset);
	}
}

function avcCodecDescription(description: Uint8Array) {
	return description.length < 4 ? "avc1.42e01e" : `avc1.${hex(description[1])}${hex(description[2])}${hex(description[3])}`;
}

function sendTagged(socket: WebSocket, tag: number, value: object) {
	const json = new TextEncoder().encode(JSON.stringify(value));
	const message = new Uint8Array(json.length + 1);
	message[0] = tag;
	message.set(json, 1);
	socket.send(message);
}

function sendIOSKey(socket: WebSocket, usage: number, shift = false) {
	if (shift) sendTagged(socket, 0x06, { type: "down", usage: 225 });
	sendTagged(socket, 0x06, { type: "down", usage });
	sendTagged(socket, 0x06, { type: "up", usage });
	if (shift) sendTagged(socket, 0x06, { type: "up", usage: 225 });
}

function sendIOSCharacter(socket: WebSocket, character: string) {
	const lower = character.toLowerCase();
	if (lower >= "a" && lower <= "z") return sendIOSKey(socket, lower.charCodeAt(0) - 93, character !== lower);
	if (character >= "1" && character <= "9") return sendIOSKey(socket, character.charCodeAt(0) - 19);
	if (character === "0") return sendIOSKey(socket, 39);
	const keys: Record<string, [number, boolean?]> = {
		" ": [44], "\n": [40], "-": [45], "_": [45, true], "=": [46], "+": [46, true],
		"[": [47], "{": [47, true], "]": [48], "}": [48, true], "\\": [49],
		";": [51], ":": [51, true], "'": [52], "\"": [52, true], "`": [53],
		",": [54], "<": [54, true], ".": [55], ">": [55, true], "/": [56], "?": [56, true],
		"!": [30, true], "@": [31, true], "#": [32, true], "$": [33, true], "%": [34, true],
		"^": [35, true], "&": [36, true], "*": [37, true], "(": [38, true], ")": [39, true],
	};
	const key = keys[character];
	if (key) sendIOSKey(socket, key[0], key[1]);
}

function parseAndroidFrame(buffer: ArrayBuffer): { data: Uint8Array; key: boolean; timestamp: number } | undefined {
	let data = new Uint8Array(buffer);
	let timestamp = Math.round(performance.now() * 1_000);
	let key = false;
	if (data.byteLength >= 16 && new DataView(buffer).getUint32(0) === 0x53454d55) {
		const view = new DataView(buffer);
		const version = view.getUint8(4);
		key = (view.getUint8(5) & 1) !== 0;
		const raw = view.getBigUint64(8);
		timestamp = Number(raw <= BigInt(Number.MAX_SAFE_INTEGER) ? raw : BigInt(timestamp));
		if (version === 2 && data.byteLength > 24) data = data.subarray(24);
		else if (version === 1 && data.byteLength > 16) data = data.subarray(16);
		else return undefined;
	}
	if (!data.byteLength) return undefined;
	return { data, key: key || hasNALType(data, 5), timestamp };
}

function avcCodec(data: Uint8Array): string | undefined {
	const sps = findNAL(data, 7);
	if (!sps || sps.length < 4) return undefined;
	return `avc1.${hex(sps[1])}${hex(sps[2])}${hex(sps[3])}`;
}

function hasNALType(data: Uint8Array, type: number) { return findNAL(data, type) !== undefined; }

function findNAL(data: Uint8Array, type: number): Uint8Array | undefined {
	for (let index = 0; index + 4 < data.length; index++) {
		const start = data[index] === 0 && data[index + 1] === 0 && (data[index + 2] === 1 || (data[index + 2] === 0 && data[index + 3] === 1));
		if (!start) continue;
		const offset = data[index + 2] === 1 ? index + 3 : index + 4;
		if ((data[offset] & 0x1f) === type) return data.subarray(offset);
	}
	return undefined;
}

function hex(value: number) { return value.toString(16).padStart(2, "0"); }
function errorMessage(cause: unknown) { return cause instanceof Error ? cause.message : String(cause); }

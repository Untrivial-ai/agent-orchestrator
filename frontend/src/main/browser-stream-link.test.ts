import { EventEmitter } from "node:events";
import type net from "node:net";
import { describe, expect, it, vi } from "vitest";
import { connectBrowserStream, type BrowserEncodedFrame } from "./browser-stream-link";

class FakeSocket extends EventEmitter {
	destroyed = false;
	readonly writes: Buffer[] = [];
	readonly writeResults: boolean[] = [];

	write(packet: Uint8Array): boolean {
		this.writes.push(Buffer.from(packet));
		return this.writeResults.shift() ?? true;
	}

	destroy(): this {
		this.destroyed = true;
		return this;
	}
}

function frame(streamId: number, sequence: number): BrowserEncodedFrame {
	return {
		streamId,
		sequence: BigInt(sequence),
		capturedAtMs: 1n,
		width: 100,
		height: 100,
		jpeg: Buffer.from([sequence]),
	};
}

function decodeFrame(packet: Buffer): { streamId: number; sequence: bigint } | null {
	if (packet[4] !== 2) return null;
	return {
		streamId: packet.readUInt32BE(5),
		sequence: packet.readBigUInt64BE(9),
	};
}

describe("browser stream link", () => {
	it("keeps only the newest frame per stream while socket writes are blocked", () => {
		const socket = new FakeSocket();
		// The hello and first frame are accepted; the first frame also signals
		// backpressure and must be the final write until drain.
		socket.writeResults.push(true, false, true, true);
		const handle = connectBrowserStream("ignored", {
			token: "secret",
			onControl: vi.fn(),
			createConnection: () => socket as unknown as net.Socket,
		});
		socket.emit("connect");

		handle.sendFrame(frame(1, 1));
		handle.sendFrame(frame(1, 2));
		handle.sendFrame(frame(2, 1));
		handle.sendFrame(frame(1, 3));

		expect(socket.writes.map(decodeFrame).filter((value) => value !== null)).toEqual([
			{ streamId: 1, sequence: 1n },
		]);

		socket.emit("drain");

		expect(socket.writes.map(decodeFrame).filter((value) => value !== null)).toEqual([
			{ streamId: 1, sequence: 1n },
			{ streamId: 1, sequence: 3n },
			{ streamId: 2, sequence: 1n },
		]);
		handle.dispose();
	});
});

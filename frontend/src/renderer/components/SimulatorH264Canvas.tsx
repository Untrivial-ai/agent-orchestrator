import { useEffect, useRef, type CanvasHTMLAttributes } from "react";

type Props = { encoded?: ArrayBuffer; width: number; height: number; label: string } & CanvasHTMLAttributes<HTMLCanvasElement>;

/** Decode the daemon's Annex-B VideoToolbox stream without Simulator.app. */
export function SimulatorH264Canvas({ encoded, width, height, label, ...props }: Props) {
	const canvasRef = useRef<HTMLCanvasElement>(null);
	const decoderRef = useRef<any>(null);
	const timestampRef = useRef(0);
	const firstChunkRef = useRef(true);

	useEffect(() => {
		const VideoDecoderAPI = (globalThis as any).VideoDecoder;
		const EncodedVideoChunkAPI = (globalThis as any).EncodedVideoChunk;
		if (!VideoDecoderAPI || !EncodedVideoChunkAPI) return;
		const canvas = canvasRef.current;
		if (!canvas) return;
		canvas.width = width;
		canvas.height = height;
		const context = canvas.getContext("2d", { alpha: false });
		if (!context) return;
		const decoder = new VideoDecoderAPI({
			output: (frame: any) => {
				context.drawImage(frame, 0, 0, width, height);
				frame.close();
			},
			error: () => { /* reconnect logic owns recovery; a bad GOP is recoverable */ },
		});
		decoder.configure({ codec: "avc1.42E01E", optimizeForLatency: true, avc: { format: "annexb" } });
		decoderRef.current = decoder;
		firstChunkRef.current = true;
		return () => {
			decoderRef.current = null;
			if (decoder.state !== "closed") void decoder.close();
		};
	}, [width, height]);

	useEffect(() => {
		if (!encoded || !decoderRef.current || decoderRef.current.state !== "configured") return;
		const EncodedVideoChunkAPI = (globalThis as any).EncodedVideoChunk;
		if (!EncodedVideoChunkAPI) return;
		const chunk = new EncodedVideoChunkAPI({
			type: firstChunkRef.current ? "key" : "delta",
			timestamp: timestampRef.current++,
			data: encoded,
		});
		firstChunkRef.current = false;
		try { decoderRef.current.decode(chunk); } catch { /* wait for the next reconnect/keyframe */ }
	}, [encoded]);

	return <canvas ref={canvasRef} aria-label={label} data-testid="emulator-canvas" {...props} className="max-h-full max-w-full rounded-sm object-contain" style={{ aspectRatio: `${width} / ${height}` }} />;
}

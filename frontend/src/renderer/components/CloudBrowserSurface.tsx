import {
	useCallback,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
	type ClipboardEvent,
	type CompositionEvent,
	type KeyboardEvent,
	type MouseEvent,
	type PointerEvent,
	type WheelEvent,
} from "react";
import { useTranslation } from "react-i18next";
import type { CloudBrowserSurfaceModel } from "../hooks/useBrowserView";
import { cn } from "../lib/utils";

type Size = { width: number; height: number };
type PendingFramePaint = {
	frameUrl: string;
	height: number;
	sequence: number;
	width: number;
	generation: number;
};

export async function paintCloudBrowserFrame(
	canvas: HTMLCanvasElement,
	frameUrl: string,
	width: number,
	height: number,
	isCanceled: () => boolean = () => false,
	now: () => number = () => performance.now(),
): Promise<{ decodeMs: number; paintMs: number } | null> {
	const decodeStartedAt = now();
	const response = await fetch(frameUrl);
	const blob = await response.blob();
	const bitmap = await createImageBitmap(blob);
	if (isCanceled()) {
		bitmap.close();
		return null;
	}
	canvas.width = width;
	canvas.height = height;
	const paintStartedAt = now();
	canvas.getContext("2d")?.drawImage(bitmap, 0, 0, width, height);
	bitmap.close();
	return { decodeMs: paintStartedAt - decodeStartedAt, paintMs: now() - paintStartedAt };
}

export function mapCloudBrowserPoint(
	clientX: number,
	clientY: number,
	rect: Pick<DOMRect, "left" | "top" | "width" | "height">,
	frameWidth: number,
	frameHeight: number,
): { x: number; y: number } {
	return {
		x: Math.max(0, Math.min(frameWidth, (clientX - rect.left) * frameWidth / rect.width)),
		y: Math.max(0, Math.min(frameHeight, (clientY - rect.top) * frameHeight / rect.height)),
	};
}

function modifiers(event: Pick<KeyboardEvent | PointerEvent | WheelEvent, "altKey" | "ctrlKey" | "metaKey" | "shiftKey">): number {
	return (event.altKey ? 1 : 0) | (event.ctrlKey ? 2 : 0) | (event.metaKey ? 4 : 0) | (event.shiftKey ? 8 : 0);
}

function mouseButton(button: number): string {
	switch (button) {
		case 0: return "left";
		case 1: return "middle";
		case 2: return "right";
		case 3: return "back";
		case 4: return "forward";
		default: return "none";
	}
}

export function CloudBrowserSurface({ model }: { model: CloudBrowserSurfaceModel }) {
	const { t } = useTranslation();
	const { snapshot, send, setViewport, reportPaint, retry } = model;
	const hostRef = useRef<HTMLDivElement>(null);
	const canvasRef = useRef<HTMLCanvasElement>(null);
	const textInputRef = useRef<HTMLTextAreaElement>(null);
	const [hostSize, setHostSize] = useState<Size>({ width: 0, height: 0 });
	const [inputError, setInputError] = useState("");
	const [dialogPrompt, setDialogPrompt] = useState("");
	const moveTimerRef = useRef<number | undefined>(undefined);
	const pendingMoveRef = useRef<{ x: number; y: number; buttons: number; modifiers: number } | undefined>(undefined);
	const wheelTimerRef = useRef<number | undefined>(undefined);
	const pendingWheelRef = useRef<{
		x: number;
		y: number;
		deltaX: number;
		deltaY: number;
		modifiers: number;
	} | undefined>(undefined);
	const pendingFrameRef = useRef<PendingFramePaint | null>(null);
	const paintGenerationRef = useRef(0);
	const runningPaintGenerationRef = useRef<number | null>(null);
	const reportPaintRef = useRef(reportPaint);
	reportPaintRef.current = reportPaint;

	useLayoutEffect(() => {
		const host = hostRef.current;
		if (!host) return;
		let resizeTimer: number | undefined;
		const update = () => {
			const rect = host.getBoundingClientRect();
			const size = { width: Math.max(0, rect.width), height: Math.max(0, rect.height) };
			setHostSize(size);
			window.clearTimeout(resizeTimer);
			resizeTimer = window.setTimeout(() => setViewport(size.width, size.height), 120);
		};
		const observer = new ResizeObserver(update);
		observer.observe(host);
		update();
		return () => {
			observer.disconnect();
			window.clearTimeout(resizeTimer);
		};
	}, [setViewport]);

	useEffect(() => {
		const generation = paintGenerationRef.current + 1;
		paintGenerationRef.current = generation;
		return () => {
			if (paintGenerationRef.current === generation) paintGenerationRef.current += 1;
			if (pendingFrameRef.current?.generation === generation) pendingFrameRef.current = null;
		};
	}, []);

	useEffect(() => {
		const canvas = canvasRef.current;
		if (!canvas || !snapshot.frameUrl || snapshot.frameWidth <= 0 || snapshot.frameHeight <= 0) return;
		const generation = paintGenerationRef.current;
		pendingFrameRef.current = {
			frameUrl: snapshot.frameUrl,
			height: snapshot.frameHeight,
			sequence: snapshot.frameSequence,
			width: snapshot.frameWidth,
			generation,
		};
		if (runningPaintGenerationRef.current === generation) return;
		runningPaintGenerationRef.current = generation;
		void (async () => {
			try {
				while (paintGenerationRef.current === generation) {
					const frame = pendingFrameRef.current;
					if (!frame || frame.generation !== generation) break;
					pendingFrameRef.current = null;
					try {
						const timing = await paintCloudBrowserFrame(
							canvas,
							frame.frameUrl,
							frame.width,
							frame.height,
							() => paintGenerationRef.current !== generation,
						);
						if (timing && paintGenerationRef.current === generation) {
							reportPaintRef.current(frame.sequence, timing.decodeMs, timing.paintMs);
						}
					} catch {
						// A newer queued frame can still decode after one malformed frame.
					}
				}
			} finally {
				if (runningPaintGenerationRef.current === generation) {
					runningPaintGenerationRef.current = null;
				}
			}
		})();
	}, [snapshot.frameHeight, snapshot.frameSequence, snapshot.frameUrl, snapshot.frameWidth]);

	useEffect(() => () => {
		window.clearTimeout(moveTimerRef.current);
		window.clearTimeout(wheelTimerRef.current);
	}, []);

	useEffect(() => {
		if (snapshot.dialogOpen) setDialogPrompt(snapshot.dialogPrompt);
	}, [snapshot.dialogOpen, snapshot.dialogPrompt]);

	useEffect(() => {
		if ((snapshot.status === "waiting" || snapshot.status === "ready") && hostSize.width > 0 && hostSize.height > 0) {
			setViewport(hostSize.width, hostSize.height);
		}
	}, [hostSize.height, hostSize.width, setViewport, snapshot.status, snapshot.streamEpoch, snapshot.owner]);

	const frameAspect = snapshot.frameWidth > 0 && snapshot.frameHeight > 0
		? snapshot.frameWidth / snapshot.frameHeight
		: 16 / 9;
	const hostAspect = hostSize.height > 0 ? hostSize.width / hostSize.height : frameAspect;
	const displaySize = hostAspect > frameAspect
		? { width: hostSize.height * frameAspect, height: hostSize.height }
		: { width: hostSize.width, height: hostSize.width / frameAspect };
	const inputEnabled = snapshot.status === "ready" && snapshot.canOperate && !snapshot.viewportPending && snapshot.owner !== "agent";

	const point = useCallback((event: PointerEvent<HTMLCanvasElement> | MouseEvent<HTMLCanvasElement> | WheelEvent<HTMLCanvasElement>) => {
		const rect = event.currentTarget.getBoundingClientRect();
		return mapCloudBrowserPoint(event.clientX, event.clientY, rect, snapshot.frameWidth, snapshot.frameHeight);
	}, [snapshot.frameHeight, snapshot.frameWidth]);

	const pointer = useCallback((kind: string, event: PointerEvent<HTMLCanvasElement> | MouseEvent<HTMLCanvasElement>, clickCount?: number) => {
		if (!inputEnabled) return;
		const location = point(event);
		send({
			type: "input", kind, ...location,
			button: mouseButton(event.button), buttons: event.buttons,
			clickCount, modifiers: modifiers(event),
		});
	}, [inputEnabled, point, send]);

	const pointerMove = useCallback((event: PointerEvent<HTMLCanvasElement>) => {
		if (!inputEnabled) return;
		pendingMoveRef.current = { ...point(event), buttons: event.buttons, modifiers: modifiers(event) };
		if (moveTimerRef.current !== undefined) return;
		moveTimerRef.current = window.setTimeout(() => {
			moveTimerRef.current = undefined;
			const pending = pendingMoveRef.current;
			if (pending) send({ type: "input", kind: "pointerMove", ...pending });
		}, 33);
	}, [inputEnabled, point, send]);

	const wheel = useCallback((event: WheelEvent<HTMLCanvasElement>) => {
		if (!inputEnabled) return;
		event.preventDefault();
		const location = point(event);
		const pending = pendingWheelRef.current;
		pendingWheelRef.current = {
			...location,
			deltaX: (pending?.deltaX ?? 0) + event.deltaX,
			deltaY: (pending?.deltaY ?? 0) + event.deltaY,
			modifiers: modifiers(event),
		};
		if (wheelTimerRef.current !== undefined) return;
		wheelTimerRef.current = window.setTimeout(() => {
			wheelTimerRef.current = undefined;
			const next = pendingWheelRef.current;
			pendingWheelRef.current = undefined;
			if (next) send({ type: "input", kind: "wheel", ...next });
		}, 16);
	}, [inputEnabled, point, send]);

	const key = useCallback((kind: "keyDown" | "keyUp", event: KeyboardEvent<HTMLElement>) => {
		if (!inputEnabled || event.nativeEvent.isComposing) return;
		const shortcut = event.metaKey || event.ctrlKey;
		if (shortcut && !["a", "z", "y", "ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Backspace", "Delete", "Home", "End"].includes(event.key.length === 1 ? event.key.toLowerCase() : event.key)) return;
		event.preventDefault();
		event.stopPropagation();
		send({
			type: "input", kind, key: event.key, codeValue: event.code,
			text: kind === "keyDown" && !shortcut && event.key.length === 1 ? event.key : undefined,
			modifiers: shortcut ? (modifiers(event) & ~4) | 2 : modifiers(event),
		});
	}, [inputEnabled, send]);

	const paste = useCallback((event: ClipboardEvent<HTMLElement>) => {
		event.preventDefault();
		event.stopPropagation();
		if (!inputEnabled) return;
		const text = event.clipboardData.getData("text/plain");
		if (new TextEncoder().encode(text).byteLength > 8 * 1024) {
			setInputError(t("browser.cloud.pasteTooLarge"));
			return;
		}
		setInputError("");
		if (text) send({ type: "input", kind: "text", text });
		if (textInputRef.current) textInputRef.current.value = "";
	}, [inputEnabled, send, t]);

	const composition = useCallback((kind: string, event: CompositionEvent<HTMLElement>) => {
		if (!inputEnabled) return;
		send({ type: "input", kind, text: event.data });
		if (kind === "compositionCommit" && textInputRef.current) textInputRef.current.value = "";
	}, [inputEnabled, send]);

	const blankPage = !snapshot.frameUrl && (!snapshot.url || snapshot.url === "about:blank");
	const statusLabel = (snapshot.status === "connecting" || snapshot.status === "waiting") && !blankPage
		? t("browser.cloud.starting")
		: snapshot.status === "reconnecting"
			? t("browser.cloud.reconnecting")
			: snapshot.status === "fatal"
				? (snapshot.error || t("browser.cloud.unavailable"))
				: "";
	const ownerLabel = snapshot.owner === "agent"
		? t("browser.cloud.agentControlling")
		: snapshot.owner === "user"
			? t("browser.cloud.userControlling")
			: "";

	return (
		<div className="absolute inset-0 grid place-items-center overflow-hidden bg-background focus-within:ring-2 focus-within:ring-primary" ref={hostRef}>
			<canvas
				aria-label={t("browser.cloud.canvas")}
				className={cn(
					"block bg-black outline-none focus-visible:ring-2 focus-visible:ring-primary",
					!inputEnabled && "cursor-default",
					snapshot.status === "reconnecting" && "opacity-60",
				)}
				onContextMenu={(event) => event.preventDefault()}
				onPaste={paste}
				onKeyDown={(event) => key("keyDown", event)}
				onKeyUp={(event) => key("keyUp", event)}
				onPointerDown={(event) => {
					event.currentTarget.setPointerCapture(event.pointerId);
					textInputRef.current?.focus();
					pointer("pointerDown", event, Math.max(1, event.detail));
				}}
				onPointerMove={pointerMove}
				onPointerUp={(event) => pointer("pointerUp", event, Math.max(1, event.detail))}
				onWheel={wheel}
				style={{ height: displaySize.height, width: displaySize.width }}
				tabIndex={inputEnabled ? 0 : -1}
				ref={canvasRef}
			/>
			<textarea
				aria-label={t("browser.cloud.textInput")}
				className="pointer-events-none absolute size-px opacity-0"
				defaultValue=""
				onCompositionEnd={(event) => composition("compositionCommit", event)}
				onCompositionStart={(event) => composition("compositionStart", event)}
				onCompositionUpdate={(event) => composition("compositionUpdate", event)}
				onPaste={paste}
				onKeyDown={(event) => key("keyDown", event)}
				onKeyUp={(event) => key("keyUp", event)}
				ref={textInputRef}
			/>
			{inputError ? <p role="alert" className="absolute bottom-2 rounded bg-background p-2 text-xs text-destructive">{inputError}</p> : null}
			{statusLabel ? (
				<div className={cn(
					"absolute inset-0 grid place-items-center bg-background/55 p-5 text-center font-mono text-xs text-passive",
					snapshot.status !== "fatal" && "pointer-events-none",
				)}>
					<div className="grid justify-items-center gap-2">
						<p>{statusLabel}</p>
						{snapshot.status === "fatal" && snapshot.errorRequestId ? (
							<p>{t("browser.cloud.requestId", { id: snapshot.errorRequestId })}</p>
						) : null}
						{snapshot.status === "fatal" ? (
							<button className="rounded-md border border-border bg-background px-3 py-1.5 text-xs text-foreground" onClick={retry} type="button">
								{t("browser.cloud.retry")}
							</button>
						) : null}
					</div>
				</div>
			) : null}
			{blankPage && snapshot.status !== "fatal" && snapshot.status !== "reconnecting" ? (
				<div className="pointer-events-none absolute inset-0 grid place-items-center p-5 text-center font-mono text-xs text-passive">
					<p>{t("browser.emptyUrl")}</p>
				</div>
			) : null}
			{ownerLabel ? (
				<div className="pointer-events-none absolute right-2 top-2 rounded-md border border-border bg-background/90 px-2 py-1 text-caption text-foreground shadow-sm">
					{ownerLabel}
				</div>
			) : null}
			{snapshot.dialogOpen ? (
				<div className="absolute inset-0 grid place-items-center bg-background/60 p-5">
					<div className="w-full max-w-sm rounded-lg border border-border bg-background p-4 shadow-lg">
						<p className="mb-3 whitespace-pre-wrap text-sm text-foreground">{snapshot.dialogText}</p>
						{snapshot.dialogType === "prompt" ? (
							<input
								autoFocus
								className="mb-3 w-full rounded-md border border-border bg-surface px-2 py-1.5 text-sm outline-none focus:ring-2 focus:ring-primary"
								onChange={(event) => setDialogPrompt(event.target.value)}
								value={dialogPrompt}
							/>
						) : null}
						<div className="flex justify-end gap-2">
							<button
								className="rounded-md border border-border px-3 py-1.5 text-xs"
								disabled={!snapshot.canOperate}
								onClick={() => send({ type: "dialog", operation: "dismiss" })}
								type="button"
							>
								{t("browser.cloud.dismissDialog")}
							</button>
							<button
								className="rounded-md bg-primary px-3 py-1.5 text-xs text-primary-foreground"
								disabled={!snapshot.canOperate}
								onClick={() => send({ type: "dialog", operation: "accept", text: dialogPrompt })}
								type="button"
							>
								{t("browser.cloud.acceptDialog")}
							</button>
						</div>
					</div>
				</div>
			) : null}
		</div>
	);
}

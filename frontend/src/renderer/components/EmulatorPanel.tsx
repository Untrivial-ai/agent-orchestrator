import { Home, Loader2, Lock, Play, RotateCcw, RotateCw, Smartphone, Square } from "lucide-react";
import type { MouseEvent } from "react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useFrameFps } from "../hooks/useFrameFps";
import { useIOSSimulator } from "../hooks/useIOSSimulator";
import { useSimulatorKeyboard } from "../hooks/useSimulatorKeyboard";
import { isNearFrameEdge, pointerToFrame, type FramePoint } from "../lib/device-viewport";
import { isMacPlatform } from "../lib/platform";
import { Button } from "./ui/button";
import { SimulatorH264Canvas } from "./SimulatorH264Canvas";

/**
 * The emulator is docked inside the right-side session inspector: it lives in
 * the inspector's tab body, is operated from there, and has no detach/pop-out
 * affordance. It only appears once "Mobile Emulator" is switched on in
 * Settings — the tab is absent otherwise.
 *
 * The device frame is rendered contain-fit inside a stage that fills the tab
 * body; pointer events are mapped through the rendered frame's rect into
 * device framebuffer pixels (see lib/device-viewport), so taps stay accurate
 * at any panel size, letterboxing, and orientation. Input coordinates on the
 * wire are device framebuffer pixels; the backend owns the Simulator window
 * mapping.
 */
export function EmulatorPanel({ active, sessionId }: { active: boolean; sessionId?: string }) {
	const { t } = useTranslation();
	const activeMac = active && isMacPlatform();
	const [text, setText] = useState("");
	const [selectedDevice, setSelectedDevice] = useState("");
	const [edgeHint, setEdgeHint] = useState(false);
	const edgeHintTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
	const pointerStart = useRef<FramePoint | null>(null);
	const ios = useIOSSimulator(activeMac, sessionId);
	const status = ios.status.data;
	const booted = status?.state === "Booted";
	const screenshot = ios.screenshot.data;
	const frame = useMemo(
		() => ios.streamFrame ?? (screenshot ? { data: screenshot.data, mimeType: screenshot.mimeType, codec: "png" as const, width: screenshot.width, height: screenshot.height } : null),
		[ios.streamFrame, screenshot],
	);
	const frameWidth = frame?.width ?? status?.screenWidth ?? 0;
	const frameHeight = frame?.height ?? status?.screenHeight ?? 0;
	const fpsTarget = useMemo(
		() => (frame?.codec !== "h264" && frame?.data && frame.mimeType ? { data: frame.data, mimeType: frame.mimeType } : null),
		[frame?.codec, frame?.data, frame?.mimeType],
	);
	const fps = useFrameFps(fpsTarget);
	const keyboard = useSimulatorKeyboard({
		active: activeMac,
		booted,
		onInput: (input) => ios.input.mutate(input),
	});
	const permissions = ios.permissions.data;
	const toolchain = ios.toolchain.data;

	// A stale edge-hint timer must not fire after the panel unmounts.
	useEffect(() => () => clearTimeout(edgeHintTimer.current), []);

	if (!activeMac) return null;

	const pointerPoint = (event: MouseEvent<HTMLElement>): FramePoint | null => {
		const bounds = event.currentTarget.getBoundingClientRect();
		return pointerToFrame(event.clientX, event.clientY, bounds, frameWidth, frameHeight);
	};
	const startPointer = (event: MouseEvent<HTMLElement>) => {
		pointerStart.current = pointerPoint(event);
	};
	const sendPointer = (event: MouseEvent<HTMLElement>) => {
		const start = pointerStart.current;
		pointerStart.current = null;
		if (!start) return; // the gesture began outside the device frame
		const end = pointerPoint(event);
		if (!end) return; // released outside the device frame — drop the gesture
		if (Math.hypot(end.x - start.x, end.y - start.y) < 8) {
			ios.input.mutate({ action: "tap", x: end.x, y: end.y });
		} else {
			if (isNearFrameEdge(start, frameWidth, frameHeight)) {
				setEdgeHint(true);
				clearTimeout(edgeHintTimer.current);
				edgeHintTimer.current = setTimeout(() => setEdgeHint(false), 1800);
			}
			ios.input.mutate({ action: "swipe", x: start.x, y: start.y, x2: end.x, y2: end.y });
		}
	};

	return (
		<div className="flex h-full min-h-0 flex-col gap-2 p-3 @max-[300px]/inspector:px-2.5" ref={keyboard.containerRef} role="tabpanel" aria-label={t("emulator.title")}>
			<div className="flex items-center justify-between gap-2">
				<div className="flex min-w-0 flex-col">
					<strong className="text-sm-md">{t("emulator.title")}</strong>
					<span className="truncate text-2xs text-passive">
						<Smartphone aria-hidden="true" className="mr-1 inline size-3 align-[-1px]" />
						{status?.name ?? (booted ? t("emulator.deviceLabel") : t("emulator.noDevice"))}
					</span>
				</div>
				<div className="flex shrink-0 gap-1.5">
					<select aria-label={t("emulator.deviceLabel")} className="max-w-28 rounded border border-border bg-background px-1 text-2xs" value={selectedDevice || status?.deviceId || ""} onChange={(event) => setSelectedDevice(event.target.value)} disabled={booted}>
						<option value="">{t("emulator.deviceLabel")}</option>
						{ios.devices.data?.map((device) => <option key={device.deviceId} value={device.deviceId}>{device.name}</option>)}
					</select>
					<Button size="sm" type="button" onClick={() => ios.start.mutate(selectedDevice || undefined)} disabled={ios.start.isPending || booted} aria-label={t("emulator.start")} title={t("emulator.start")}>
						<Play aria-hidden="true" />{t("emulator.start")}
					</Button>
					<Button size="sm" type="button" variant="outline" onClick={() => ios.stop.mutate()} disabled={ios.stop.isPending || !booted} aria-label={t("emulator.stop")} title={t("emulator.stop")}>
						<Square aria-hidden="true" />{t("emulator.stop")}
					</Button>
				</div>
			</div>
			{/* Dependencies: Xcode must be present before the simulator can boot. */}
			{toolchain && !toolchain.xcodeDetected ? (
				<div className="rounded-md border border-warning/40 bg-warning/10 p-2 text-caption text-passive">
					<p>{t("emulator.toolchainMissing")}</p>
					<p className="mt-1">{toolchain.guidanceWhyMissing ?? ""}</p>
					<div className="mt-1.5 flex gap-1.5">
						<Button size="sm" type="button" variant="outline" onClick={() => window.open(toolchain.guidanceAppStoreURL || "https://apps.apple.com/app/xcode/id497799835", "_blank")}>{t("emulator.installXcode")}</Button>
						<Button size="sm" type="button" variant="outline" onClick={() => ios.recheck.mutate()} disabled={ios.recheck.isPending}>{t("emulator.recheck")}</Button>
					</div>
				</div>
			) : null}
			{permissions && (!permissions.screenRecording || !permissions.accessibility) ? <div className="rounded-md border border-warning/40 bg-warning/10 p-2 text-caption text-passive">
				<p>{t("emulator.permissionsDescription")}</p>
				<div className="mt-1.5 flex gap-1.5">
					{!permissions.screenRecording ? <Button size="sm" type="button" variant="outline" onClick={() => window.open("x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture", "_blank")}>{t("emulator.screenRecording")}</Button> : null}
					{!permissions.accessibility ? <Button size="sm" type="button" variant="outline" onClick={() => window.open("x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility", "_blank")}>{t("emulator.accessibility")}</Button> : null}
				</div>
			</div> : null}
			{/* Device stage: the frame fills the flexible area at its native
			    aspect ratio, letterboxed by the stage. Clicks in the letterbox
			    never reach the simulator (pointerToFrame returns null). */}
			<div className="relative flex min-h-0 flex-1 items-center justify-center overflow-hidden rounded-md border border-border bg-background/40" data-testid="emulator-stage">
				{booted && frame ? (
					frame.codec === "h264" ? <SimulatorH264Canvas
						encoded={frame.encoded}
						width={frameWidth}
						height={frameHeight}
						label={t("emulator.title")}
						onMouseDown={startPointer}
						onMouseUp={sendPointer}
						onMouseLeave={() => { pointerStart.current = null; }}
					/> : <img
						alt={t("emulator.title")}
						data-testid="emulator-frame"
						draggable={false}
						className="max-h-full max-w-full touch-none rounded-sm select-none object-contain"
						style={frameWidth > 0 && frameHeight > 0 ? { aspectRatio: `${frameWidth} / ${frameHeight}` } : undefined}
						src={frame.data ? `data:${frame.mimeType};base64,${frame.data}` : undefined}
						onMouseDown={startPointer}
						onMouseUp={sendPointer}
						onMouseLeave={() => { pointerStart.current = null; }}
					/>
				) : booted ? (
					<p className="flex items-center gap-1.5 text-caption text-passive">
						{ios.streamState === "connecting" || ios.streamState === "idle" ? (
							<><Loader2 className="size-3.5 animate-spin" aria-hidden="true" />{t("emulator.connecting")}</>
						) : (
							<span className="text-error">{ios.streamError ?? t("emulator.disconnected")}</span>
						)}
					</p>
				) : (
					<p className="text-caption text-passive">{ios.start.isPending ? t("emulator.booting") : (status?.error ?? t("emulator.startPrompt"))}</p>
				)}
				{/* Laid-over device telemetry and input affordances. All overlays are
				    pointer-events-none so they never swallow frame interactions. */}
				{fps > 0 ? (
					<span data-testid="emulator-fps" className="pointer-events-none absolute right-1.5 top-1.5 rounded bg-foreground/70 px-1.5 py-0.5 font-mono text-2xs text-background">
						{fps} <span className="lowercase">{t("emulator.fps")}</span>
					</span>
				) : null}
				{keyboard.armed ? (
					<span data-testid="emulator-keyboard-armed" className="pointer-events-none absolute bottom-1.5 left-1.5 rounded bg-foreground/70 px-1.5 py-0.5 text-2xs text-background">
						{t("emulator.keyboardArmed")}
					</span>
				) : null}
				{edgeHint ? (
					<div data-testid="emulator-edge-hint" className="pointer-events-none absolute inset-x-0 bottom-4 flex justify-center px-2">
						<span className="rounded bg-foreground/70 px-2 py-0.5 text-center text-2xs text-background">{t("emulator.edgeGestureHint")}</span>
					</div>
				) : null}
			</div>
			{booted ? (
				<div className="flex flex-wrap items-center gap-1.5">
					<Button size="sm" type="button" variant="outline" onClick={() => ios.input.mutate({ action: "home" })} aria-label={t("emulator.home")} title={t("emulator.home")}>
						<Home aria-hidden="true" />
					</Button>
					<Button size="sm" type="button" variant="outline" onClick={() => ios.input.mutate({ action: "lock" })} aria-label={t("emulator.lock")} title={t("emulator.lock")}>
						<Lock aria-hidden="true" />
					</Button>
					<Button size="sm" type="button" variant="outline" onClick={() => ios.input.mutate({ action: "rotateLeft" })} aria-label={t("emulator.rotateLeft")} title={t("emulator.rotateLeft")}>
						<RotateCcw aria-hidden="true" />
					</Button>
					<Button size="sm" type="button" variant="outline" onClick={() => ios.input.mutate({ action: "rotateRight" })} aria-label={t("emulator.rotateRight")} title={t("emulator.rotateRight")}>
						<RotateCw aria-hidden="true" />
					</Button>
					<form className="flex min-w-0 flex-1 gap-1.5" onSubmit={(event) => { event.preventDefault(); if (text) { ios.input.mutate({ action: "text", text }); setText(""); } }}><input aria-label={t("emulator.textInput")} className="min-w-0 flex-1 rounded border border-border bg-background px-2 py-1 text-sm" value={text} onChange={(event) => setText(event.target.value)} placeholder={t("emulator.textPlaceholder")} /><Button size="sm" type="submit" disabled={!text || ios.input.isPending}>{t("emulator.send")}</Button></form>
					<Button size="sm" type="button" variant="outline" onClick={() => ios.input.mutate({ action: "key", keyCode: 36 })} aria-label={t("emulator.enter")}>{t("emulator.enter")}</Button>
					<Button size="sm" type="button" variant="outline" onClick={() => ios.input.mutate({ action: "key", keyCode: 51 })} aria-label={t("emulator.backspace")}>⌫</Button>
				</div>
			) : null}
		</div>
	);
}

import { useCallback, useEffect, useRef, useState, type MouseEvent, type ReactNode } from "react";
import { ArrowLeft, CornerDownLeft, ExternalLink, House, Loader2, Power, RefreshCw, Smartphone, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import type {
	LocalDevice,
	LocalDeviceAttachment,
	LocalDeviceCapability,
	LocalDeviceCommand,
	LocalDevicePlatform,
	LocalDeviceSetup,
} from "../../shared/local-device";
import { aoBridge } from "../lib/bridge";
import { Button } from "./ui/button";
import { cn } from "../lib/utils";

const SCREEN_REFRESH_MS = 1_500;

export function DevicePanel({ sessionId }: { sessionId: string }) {
	const { t } = useTranslation();
	const [devices, setDevices] = useState<LocalDevice[]>([]);
	const [capabilities, setCapabilities] = useState<LocalDeviceCapability[]>([]);
	const [setups, setSetups] = useState<LocalDeviceSetup[]>([]);
	const [licenses, setLicenses] = useState<Record<LocalDevicePlatform, boolean>>({ ios: false, android: false });
	const [attachment, setAttachment] = useState<LocalDeviceAttachment>();
	const [screen, setScreen] = useState<string>();
	const [selectedId, setSelectedId] = useState("");
	const [text, setText] = useState("");
	const [error, setError] = useState<string>();
	const [busy, setBusy] = useState(true);
	const captureInFlight = useRef(false);

	const load = useCallback(async () => {
		setBusy(true);
		setError(undefined);
		try {
			const [status, inventory, setupStatus] = await Promise.all([
				aoBridge.device.status(sessionId),
				aoBridge.device.list(sessionId),
				aoBridge.device.setupStatus(sessionId),
			]);
			// Older or stale dev daemons may encode an empty Go slice as null.
			// Treat the bridge as an untrusted runtime boundary even though the
			// generated TypeScript contract correctly declares an array.
			const availableDevices = Array.isArray(inventory.devices) ? inventory.devices : [];
			const listErrors = new Map((inventory.errors ?? []).map((item) => [item.platform, item]));
			setCapabilities(status.capabilities.map((item) => listErrors.get(item.platform) ?? item));
			setAttachment(status.attachment);
			setDevices(availableDevices);
			setSetups(Array.isArray(setupStatus.setups) ? setupStatus.setups : []);
			setSelectedId((current) => current || availableDevices.find((item) => !item.busy)?.id || "");
		} catch (cause) {
			setError(errorMessage(cause));
		} finally {
			setBusy(false);
		}
	}, [sessionId]);

	const command = useCallback(async (input: Omit<LocalDeviceCommand, "sessionId">) => {
		setError(undefined);
		const result = await aoBridge.device.command({ ...input, sessionId });
		setAttachment((current) => {
			if (!result.attachment) {
				return input.action === "close" || input.action === "shutdown" ? undefined : current;
			}
			if (
				current?.sessionId === result.attachment.sessionId &&
				current.deviceId === result.attachment.deviceId &&
				current.platform === result.attachment.platform &&
				current.name === result.attachment.name
			) return current;
			return result.attachment;
		});
		return result;
	}, [sessionId]);

	const refreshScreen = useCallback(async () => {
		if (!attachment || captureInFlight.current) return;
		captureInFlight.current = true;
		try {
			const result = await command({ action: "screenshot" });
			const png = result.result?.pngBase64;
			if (typeof png === "string" && png) setScreen(`data:image/png;base64,${png}`);
		} catch (cause) {
			setError(errorMessage(cause));
		} finally {
			captureInFlight.current = false;
		}
	}, [attachment, command]);

	useEffect(() => void load(), [load]);
	useEffect(() => {
		if (!setups.some((setup) => setup.cancelable)) return;
		const timer = window.setInterval(() => {
			void aoBridge.device.setupStatus(sessionId).then((result) => {
				const next = Array.isArray(result.setups) ? result.setups : [];
				setSetups(next);
				if (!next.some((setup) => setup.cancelable)) void load();
			}).catch((cause) => setError(errorMessage(cause)));
		}, 1_000);
		return () => window.clearInterval(timer);
	}, [load, sessionId, setups]);
	useEffect(() => {
		if (!attachment) return;
		void refreshScreen();
		const timer = window.setInterval(() => void refreshScreen(), SCREEN_REFRESH_MS);
		return () => window.clearInterval(timer);
	}, [attachment, refreshScreen]);

	const run = useCallback(async (input: Omit<LocalDeviceCommand, "sessionId">, refresh = true) => {
		setBusy(true);
		try {
			await command(input);
			if (refresh) window.setTimeout(() => void refreshScreen(), 250);
		} catch (cause) {
			setError(errorMessage(cause));
		} finally {
			setBusy(false);
		}
	}, [command, refreshScreen]);

	const openSelected = async () => {
		const selected = devices.find((item) => item.id === selectedId);
		if (!selected) return;
		setScreen(undefined);
		await run({ action: "open", deviceId: selected.id, platform: selected.platform });
	};

	const manageSetup = async (platform: LocalDevicePlatform, action: "start" | "retry" | "cancel") => {
		setError(undefined);
		try {
			const result = await aoBridge.device.setup({ sessionId, platform, action, licenseAccepted: licenses[platform] });
			setSetups((current) => [...current.filter((item) => item.platform !== platform), result.setup]);
			if (result.setup.actionUrl) await aoBridge.app.openExternal(result.setup.actionUrl);
			if (result.setup.cancelable) window.setTimeout(() => void load(), 300);
		} catch (cause) { setError(errorMessage(cause)); }
	};

	const tapScreen = (event: MouseEvent<HTMLImageElement>) => {
		const image = event.currentTarget;
		const rect = image.getBoundingClientRect();
		const x = Math.round((event.clientX - rect.left) * image.naturalWidth / rect.width);
		const y = Math.round((event.clientY - rect.top) * image.naturalHeight / rect.height);
		void run({ action: "tap", x, y });
	};

	if (!attachment) {
		return (
			<div className="device-panel board-scrollbar h-full overflow-y-auto p-3" role="tabpanel" aria-label={t("device.title")}>
				<div className="mb-3 flex items-center gap-2">
					<Smartphone className="size-icon-md" aria-hidden="true" />
					<strong className="text-sm">{t("device.title")}</strong>
					<Button className="ml-auto" disabled={busy} onClick={() => void load()} size="icon" variant="ghost" aria-label={t("device.refresh")}>
						<RefreshCw className={cn("size-icon-sm", busy && "animate-spin")} />
					</Button>
				</div>
				<p className="mb-3 text-xs text-settings-muted">{t("device.description")}</p>
				{error ? <DeviceError message={error} /> : null}
				<div className="space-y-2">
					{setups.filter((item) => item.state !== "succeeded").map((setup) => (
						<SetupCard capability={capabilities.find((item) => item.platform === setup.platform)} key={setup.platform} licenseAccepted={licenses[setup.platform]} onLicense={(value) => setLicenses((current) => ({ ...current, [setup.platform]: value }))} onSetup={(action) => void manageSetup(setup.platform, action)} setup={setup} />
					))}
					{devices.map((device) => (
						<label className={cn("flex cursor-pointer items-center gap-2 rounded-md border border-border p-2", selectedId === device.id && "border-primary")} key={device.id}>
							<input checked={selectedId === device.id} disabled={device.busy} name="device" onChange={() => setSelectedId(device.id)} type="radio" />
							<span className="min-w-0 flex-1">
								<span className="block truncate text-xs font-medium">{device.name}</span>
								<span className="block text-[11px] text-settings-muted">{t(device.platform === "ios" ? "device.iosSimulator" : "device.androidEmulator")}{device.booted ? ` · ${t("device.booted")}` : ""}</span>
							</span>
							{device.busy ? <span className="text-[11px] text-settings-muted">{t("device.busy")}</span> : null}
						</label>
					))}
				</div>
				{!busy && devices.length === 0 ? <p className="mt-4 text-xs text-settings-muted">{t("device.none")}</p> : null}
				<Button className="mt-3 w-full" disabled={busy || !selectedId} onClick={() => void openSelected()}>
					{busy ? <Loader2 className="animate-spin" /> : <Power />}{t("device.open")}
				</Button>
			</div>
		);
	}

	return (
		<div className="device-panel flex h-full min-h-0 flex-col" role="tabpanel" aria-label={t("device.title")}>
			<div className="flex h-control-lg shrink-0 items-center gap-1 border-b border-border px-2">
				<span className="min-w-0 flex-1 truncate text-xs font-medium">{attachment.name}</span>
				<DeviceControl label={t("device.back")} disabled={busy} onClick={() => void run({ action: "back" })}><ArrowLeft /></DeviceControl>
				<DeviceControl label={t("device.home")} disabled={busy} onClick={() => void run({ action: "home" })}><House /></DeviceControl>
				<DeviceControl label={t("device.refresh")} disabled={busy} onClick={() => void refreshScreen()}><RefreshCw /></DeviceControl>
				<DeviceControl label={t("device.close")} disabled={busy} onClick={() => { setScreen(undefined); void run({ action: "close" }, false); }}><X /></DeviceControl>
			</div>
			<div className="min-h-0 flex-1 overflow-auto bg-black/90 p-2">
				{screen ? (
					<img alt={t("device.screenAlt", { name: attachment.name })} className="mx-auto block max-h-full max-w-full cursor-crosshair object-contain" draggable={false} onClick={tapScreen} src={screen} />
				) : (
					<div className="flex h-full items-center justify-center text-xs text-white/60"><Loader2 className="mr-2 animate-spin" />{t("device.loadingScreen")}</div>
				)}
			</div>
			<form className="flex shrink-0 gap-1 border-t border-border p-2" onSubmit={(event) => { event.preventDefault(); if (!text) return; void run({ action: "type", text }).then(() => setText("")); }}>
				<input aria-label={t("device.textInput")} className="min-w-0 flex-1 rounded-md border border-border bg-background px-2 text-xs" disabled={busy} onChange={(event) => setText(event.target.value)} placeholder={t("device.textPlaceholder")} value={text} />
				<DeviceControl label={t("device.typeText")} disabled={busy || !text} type="submit"><CornerDownLeft /></DeviceControl>
				<DeviceControl label={t("device.enter")} disabled={busy} onClick={() => void run({ action: "key", key: "enter" })}><CornerDownLeft /></DeviceControl>
			</form>
			{error ? <div className="shrink-0 p-2"><DeviceError message={error} /></div> : null}
		</div>
	);
}

function DeviceControl({ children, disabled, label, onClick, type = "button" }: { children: ReactNode; disabled?: boolean; label: string; onClick?: () => void; type?: "button" | "submit" }) {
	return <Button aria-label={label} disabled={disabled} onClick={onClick} size="icon" title={label} type={type} variant="ghost">{children}</Button>;
}

function DeviceError({ message }: { message: string }) {
	return <p className="rounded-md border border-destructive/30 bg-destructive/10 p-2 text-xs text-destructive" role="alert">{message}</p>;
}

function SetupCard({ capability, licenseAccepted, onLicense, onSetup, setup }: { capability?: LocalDeviceCapability; licenseAccepted: boolean; onLicense: (value: boolean) => void; onSetup: (action: "start" | "retry" | "cancel") => void; setup: LocalDeviceSetup }) {
	const { t } = useTranslation();
	const active = setup.cancelable;
	const failed = setup.state === "failed" || setup.state === "canceled" || setup.state === "interrupted";
	const platformName = setup.platform === "ios" ? "iOS" : "Android";
	return (
		<div className="rounded-md border border-border p-3 text-xs">
			<div className="flex items-center justify-between gap-2"><strong>{t("device.setup", { platform: platformName })}</strong><span className="text-[11px] text-settings-muted">{setup.progress}%</span></div>
			<p className="mt-1 text-settings-muted">{setup.error || setup.message || capability?.message}</p>
			{setup.requiredBytes ? <p className="mt-1 text-[11px] text-settings-muted">{t("device.downloadSize", { size: formatBytes(setup.requiredBytes) })}</p> : null}
			{active || setup.progress > 0 ? <div aria-label={t("device.setupProgress", { platform: platformName })} aria-valuemax={100} aria-valuemin={0} aria-valuenow={setup.progress} className="mt-2 h-1.5 overflow-hidden rounded-full bg-muted" role="progressbar"><div className="h-full bg-primary transition-[width]" style={{ width: `${setup.progress}%` }} /></div> : null}
			{!active ? <label className="mt-2 flex items-start gap-2"><input checked={licenseAccepted} className="mt-0.5" onChange={(event) => onLicense(event.target.checked)} type="checkbox" /><span>{t("device.acceptLicense")} {setup.licenseUrl ? <button className="text-primary hover:underline" onClick={(event) => { event.preventDefault(); void aoBridge.app.openExternal(setup.licenseUrl!); }} type="button">{t("device.viewTerms")} <ExternalLink className="inline size-3" /></button> : null}</span></label> : null}
			<Button className="mt-2 w-full" disabled={!active && !licenseAccepted} onClick={() => onSetup(active ? "cancel" : failed || setup.state === "awaiting_action" ? "retry" : "start")} size="sm" variant={active ? "outline" : "primary"}>
				{active ? t("device.cancelSetup") : failed || setup.state === "awaiting_action" ? t("device.retrySetup") : t("device.setup", { platform: platformName })}
			</Button>
		</div>
	);
}

function formatBytes(bytes: number): string { return `${(bytes / (1 << 30)).toFixed(1)} GB`; }

function errorMessage(cause: unknown): string {
	return cause instanceof Error ? cause.message : String(cause);
}

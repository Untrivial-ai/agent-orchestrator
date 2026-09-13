import { useEffect, useRef, useState } from "react";
import { Bell, Camera, ChevronDown, Globe2, Info, MapPin, Mic, RotateCcw, Settings2, Trash2, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import {
	browserSiteOrigin,
	type BrowserSiteSettings,
	type BrowserSitePermissionSetting,
	type BrowserSitePermissionRequest,
	type BrowserSitePermissionDecisionValue,
} from "../../shared/browser-site-settings";
import { Button } from "./ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "./ui/popover";

const permissionRows = [
	{ permission: "camera", label: "browser.siteCamera", Icon: Camera },
	{ permission: "microphone", label: "browser.siteMicrophone", Icon: Mic },
	{ permission: "location", label: "browser.siteLocation", Icon: MapPin },
	{ permission: "notifications", label: "browser.siteNotifications", Icon: Bell },
] as const;

const permissionLabels = Object.fromEntries(permissionRows.map(({ permission, label }) => [permission, label])) as
	Record<BrowserSitePermissionRequest["permissions"][number], typeof permissionRows[number]["label"]>;

export function BrowserPermissionPrompt({ viewId, tabId }: { viewId: string; tabId: string }) {
	const { t } = useTranslation();
	const [request, setRequest] = useState<BrowserSitePermissionRequest | null>(null);
	const requestRef = useRef<BrowserSitePermissionRequest | null>(null);

	const respond = (decision: BrowserSitePermissionDecisionValue) => {
		const current = requestRef.current;
		if (!current) return;
		requestRef.current = null;
		setRequest(null);
		window.ao?.browser.respondToPermissionRequest({ requestId: current.requestId, viewId: current.viewId, decision });
	};

	useEffect(() => window.ao?.browser.onPermissionRequest((next) => {
		if (next.viewId !== viewId) return;
		if (next.tabId !== tabId) {
			window.ao?.browser.respondToPermissionRequest({ requestId: next.requestId, viewId: next.viewId, decision: "dismiss" });
			return;
		}
		const previous = requestRef.current;
		if (previous) window.ao?.browser.respondToPermissionRequest({ requestId: previous.requestId, viewId: previous.viewId, decision: "dismiss" });
		requestRef.current = next;
		setRequest(next);
	}) ?? (() => undefined), [tabId, viewId]);

	useEffect(() => {
		if (!request || (request.viewId === viewId && request.tabId === tabId)) return;
		respond("dismiss");
	}, [request, tabId, viewId]);

	useEffect(() => {
		if (!request) return;
		const onKeyDown = (event: KeyboardEvent) => {
			if (event.key !== "Escape") return;
			event.preventDefault();
			respond("dismiss");
		};
		window.addEventListener("keydown", onKeyDown);
		return () => window.removeEventListener("keydown", onKeyDown);
	}, [request]);

	useEffect(() => () => {
		const current = requestRef.current;
		if (current) window.ao?.browser.respondToPermissionRequest({ requestId: current.requestId, viewId: current.viewId, decision: "dismiss" });
	}, []);

	if (!request) return null;
	const host = new URL(request.origin).host;
	const labels = request.permissions.map((permission) => t(permissionLabels[permission])).join(", ");
	const Icon = permissionRows.find(({ permission }) => permission === request.permissions[0])?.Icon ?? Info;
	return (
		<div aria-label={t("browser.sitePermissions")} className="browser-panel__permission-prompt animate-popover-in"
			data-browser-native-overlay="true" data-state="open" role="alertdialog">
			<div className="browser-permission__message">
				<span className="browser-permission__icon"><Icon aria-hidden="true" className="size-icon-lg" /></span>
				<p>{t("browser.sitePermissionRequest", { origin: host, permissions: labels })}</p>
			</div>
			<div className="browser-permission__actions">
				<Button onClick={() => respond("block")} size="sm" type="button" variant="ghost">{t("browser.siteBlock")}</Button>
				<Button onClick={() => respond("allow-once")} size="sm" type="button" variant="outline">{t("browser.siteAllowOnce")}</Button>
				<Button onClick={() => respond("allow-always")} size="sm" type="button">{t("browser.siteAlwaysAllow")}</Button>
			</div>
		</div>
	);
}

export function BrowserSiteInfo({ url, native, viewId, tabId }: { url: string; native: boolean; viewId: string; tabId: string }) {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const [settings, setSettings] = useState<BrowserSiteSettings | null>(null);
	const [loading, setLoading] = useState(false);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState(false);
	const [needsReload, setNeedsReload] = useState(false);
	const [confirmClear, setConfirmClear] = useState(false);
	const origin = browserSiteOrigin(url);
	useEffect(() => {
		if (!native || !origin) {
			setSettings(null);
			setLoading(false);
			return;
		}
		let current = true;
		setLoading(true);
		setError(false);
		setNeedsReload(false);
		void window.ao!.browser.getSiteSettings({ viewId }).then((value) => {
			if (!current) return;
			if (value.origin !== origin || value.tabId !== tabId) setError(true);
			else setSettings(value);
		}, () => current && setError(true)).finally(() => current && setLoading(false));
		return () => { current = false; };
	}, [native, origin, viewId, tabId, open]);

	if (!origin) return null;
	const address = new URL(origin);
	const change = async (operation: () => Promise<BrowserSiteSettings | void>) => {
		if (busy) return;
		setBusy(true);
		setError(false);
		try {
			const next = await operation();
			if (next) setSettings(next);
			setNeedsReload(true);
			setConfirmClear(false);
		} catch {
			setError(true);
		} finally {
			setBusy(false);
		}
	};
	return (
		<Popover open={open} onOpenChange={(value) => { setOpen(value); setConfirmClear(false); }}>
			<PopoverTrigger asChild>
				<Button aria-label={t("browser.siteInfo")} className="browser-panel__site-info"
					onMouseDown={(event) => event.preventDefault()} size="icon-sm" type="button" variant="ghost">
					<Settings2 aria-hidden="true" className="size-icon-sm" />
				</Button>
			</PopoverTrigger>
			<PopoverContent align="start" collisionPadding={8} aria-label={t("browser.siteInfo")} role="dialog"
				className="browser-panel__site-info-content" data-browser-native-overlay="true">
				<header className="browser-site__header">
					<Globe2 aria-hidden="true" className="browser-site__header-icon" />
					<div className="min-w-0 flex-1">
						<p className="browser-site__host" title={address.host}>{address.host}</p>
					</div>
					<Button aria-label={t("browser.siteClose")} size="icon-sm" type="button" variant="ghost" onClick={() => setOpen(false)}>
						<X aria-hidden="true" size={14} />
					</Button>
				</header>
				<div className="browser-site__connection">
					<Info aria-hidden="true" className="size-icon-base" />
					<div><p>{t("browser.siteProtocol")}: {address.protocol === "https:" ? "HTTPS" : "HTTP"}</p>
						{address.protocol === "http:" && <p className="browser-site__subtitle">{t("browser.siteHttpNotice")}</p>}
					</div>
				</div>
				{native && <>
					<section className="browser-site__permissions" aria-busy={loading} aria-label={t("browser.sitePermissions")}>
						<p className="browser-site__section-label">{t("browser.sitePermissions")}</p>
						{permissionRows.map(({ permission, label, Icon }) => (
							<label key={permission} className="browser-site__permission">
								<Icon aria-hidden="true" className="size-icon-base" />
								<span className="flex-1">{t(label)}</span>
								{settings ? <span className="browser-site__select-wrap">
									<select aria-label={t(label)} disabled={busy} value={settings.permissions[permission]}
										onChange={(event) => void change(() => window.ao!.browser.setSitePermission({
											...settings, permission, setting: event.target.value as BrowserSitePermissionSetting,
										}))}>
										<option value="block">{t("browser.siteBlock")}</option>
										<option value="ask">{t("browser.siteAsk")}</option>
										<option value="allow">{t("browser.siteAllow")}</option>
									</select>
									<ChevronDown aria-hidden="true" size={12} />
								</span> : <span aria-hidden="true" className="browser-site__permission-placeholder" />}
							</label>
						))}
					</section>
					{settings && <footer className="browser-site__actions">
						{confirmClear ? <>
							<p className="browser-site__subtitle">{t("browser.siteClearNotice")}</p>
							<div className="flex justify-end gap-2 mt-3">
								<Button type="button" variant="ghost" size="sm" disabled={busy} onClick={() => setConfirmClear(false)}>{t("browser.siteCancel")}</Button>
								<Button type="button" variant="outline" size="sm" disabled={busy} onClick={() => void change(() => window.ao!.browser.clearSiteData(settings))}>{t("browser.siteClearConfirm")}</Button>
							</div>
						</> : <>
							<button type="button" disabled={busy} className="browser-site__action" onClick={() => setConfirmClear(true)}>
								<Trash2 aria-hidden="true" size={16} />{t("browser.siteClearData")}
							</button>
							<button type="button" disabled={busy} className="browser-site__action" onClick={() => void change(() => window.ao!.browser.resetSitePermissions(settings))}>
								<RotateCcw aria-hidden="true" size={16} />{t("browser.siteResetPermissions")}
							</button>
						</>}
					</footer>}
					{error && <p role="alert" className="browser-site__feedback">{t("browser.siteError")}</p>}
					{needsReload && <div role="status" className="browser-site__feedback flex items-center justify-between gap-2">
						<span>{t("browser.siteReloadNotice")}</span>
						<Button type="button" size="sm" variant="outline" disabled={busy} onClick={() => void change(async () => { await window.ao!.browser.reload(viewId); setOpen(false); })}>{t("browser.reload")}</Button>
					</div>}
				</>}
			</PopoverContent>
		</Popover>
	);
}

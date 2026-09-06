import { useEffect, useState } from "react";
import { Bell, Camera, ChevronDown, Globe2, Info, MapPin, Mic, RotateCcw, Settings2, Trash2, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { browserSiteOrigin, type BrowserSiteSettings, type BrowserSitePermissionSetting } from "../../shared/browser-site-settings";
import { Button } from "./ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "./ui/popover";

const permissionRows = [
	{ permission: "camera", label: "browser.siteCamera", Icon: Camera },
	{ permission: "microphone", label: "browser.siteMicrophone", Icon: Mic },
	{ permission: "location", label: "browser.siteLocation", Icon: MapPin },
	{ permission: "notifications", label: "browser.siteNotifications", Icon: Bell },
] as const;

export function BrowserSiteInfo({ url, native, viewId, tabId }: { url: string; native: boolean; viewId: string; tabId: string }) {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const [settings, setSettings] = useState<BrowserSiteSettings | null>(null);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState(false);
	const [needsReload, setNeedsReload] = useState(false);
	const [confirmClear, setConfirmClear] = useState(false);
	const origin = browserSiteOrigin(url);
	useEffect(() => {
		if (!open || !native || !origin) return;
		let current = true;
		setSettings(null);
		setError(false);
		void window.ao!.browser.getSiteSettings({ viewId }).then((value) => {
			if (!current) return;
			if (value.origin !== origin || value.tabId !== tabId) setError(true);
			else setSettings(value);
		}, () => current && setError(true));
		return () => { current = false; };
	}, [open, native, origin, viewId, tabId]);

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
			<PopoverContent align="start" collisionPadding={8} aria-label={t("browser.siteInfo")}
				className="browser-panel__site-info-content" data-browser-native-overlay="true">
				<header className="browser-site__header">
					<span className="browser-site__site-icon"><Globe2 aria-hidden="true" size={18} /></span>
					<div className="min-w-0 flex-1">
						<p className="browser-site__host" title={address.host}>{address.host}</p>
						<p className="browser-site__subtitle">{t("browser.siteSettings")}</p>
					</div>
					<Button aria-label={t("browser.siteClose")} size="icon-sm" type="button" variant="ghost" onClick={() => setOpen(false)}>
						<X aria-hidden="true" size={14} />
					</Button>
				</header>
				<div className="browser-site__connection">
					<Info aria-hidden="true" size={16} />
					<div><p>{t("browser.siteProtocol")}: {address.protocol === "https:" ? "HTTPS" : "HTTP"}</p>
						{address.protocol === "http:" && <p className="browser-site__subtitle">{t("browser.siteHttpNotice")}</p>}
					</div>
				</div>
				{native && <>
					<section className="browser-site__permissions" aria-label={t("browser.sitePermissions")}>
						<p className="browser-site__section-label">{t("browser.sitePermissions")}</p>
						{!settings && !error && <p role="status" className="browser-site__subtitle">{t("browser.siteLoading")}</p>}
						{settings && permissionRows.map(({ permission, label, Icon }) => (
							<label key={permission} className="browser-site__permission">
								<Icon aria-hidden="true" size={16} />
								<span className="flex-1">{t(label)}</span>
								<span className="browser-site__select-wrap">
									<select aria-label={t(label)} disabled={busy} value={settings.permissions[permission]}
										onChange={(event) => void change(() => window.ao!.browser.setSitePermission({
											...settings, permission, setting: event.target.value as BrowserSitePermissionSetting,
										}))}>
										<option value="block">{t("browser.siteBlock")}</option>
										<option value="ask">{t("browser.siteAsk")}</option>
										<option value="allow">{t("browser.siteAllow")}</option>
									</select>
									<ChevronDown aria-hidden="true" size={12} />
								</span>
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

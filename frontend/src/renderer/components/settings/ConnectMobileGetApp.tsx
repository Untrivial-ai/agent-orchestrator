import { QrCode } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { QRCodeSVG } from "qrcode.react";
import { aoBridge } from "../../lib/bridge";
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";

// Public App Store listing for the Agent Orchestrator iOS app. No storefront
// segment ("/us/") on purpose: Apple redirects a bare /app/ link to the
// visitor's own storefront, while a pinned one sends everyone outside that
// country to a "not available" page. The landing site holds the same two URLs
// (frontend/src/landing/packages/shared/src/constants.ts) — change both.
export const IOS_APP_STORE_URL = "https://apps.apple.com/app/ao-mobile/id6792552173";

/** Public Google Play listing for the Agent Orchestrator Android app. */
export const ANDROID_PLAY_STORE_URL = "https://play.google.com/store/apps/details?id=aoagents.dev&pcampaignid=web_share";

/** Deliberately smaller than the pairing QR so it never competes with it. */
const STORE_QR_SIZE = 140;

// ConnectMobileGetApp is step zero of pairing: the pairing QR below it is
// meaningless until the app is installed. It sits outside the modal's
// enable-collapse because installing the app has nothing to do with whether
// the LAN bridge is running. The QR (of the store listing itself) hides
// behind a disclosure so the widened modal keeps its height.
export function ConnectMobileGetApp() {
	const { t } = useTranslation();
	const [showQR, setShowQR] = useState(false);

	return (
		<div className="flex flex-col">
			<span className="px-3 py-3 text-subtitle leading-(--leading-settings-mobile-title) text-settings-label">{t("mobile.getApp")}</span>

			{/* iOS — items-center so the action cluster sits on the row's optical centre. */}
			<div className="flex items-center justify-between gap-3 px-3 py-3">
				<div className="flex min-w-0 flex-col">
					<span className="text-sm leading-5 text-settings-label">{t("mobile.ios")}</span>
					<span className="text-caption leading-(--leading-settings-mobile-hint) text-settings-muted">
						{t("mobile.iosHint")}
					</span>
				</div>
				<div className="flex shrink-0 items-center gap-1.5">
					<Button
						type="button"
						variant="footer"
						className="rounded-md"
						aria-label={t("mobile.iosStoreAria")}
						onClick={() => void aoBridge.app.openExternal(IOS_APP_STORE_URL)}
					>
						{t("mobile.getApp")}
					</Button>
					<button
						type="button"
						aria-label={showQR ? t("mobile.hideQR") : t("mobile.showQR")}
						aria-expanded={showQR}
						onClick={() => setShowQR((v) => !v)}
						className={cn(
							"inline-flex size-(--size-settings-action-height) items-center justify-center rounded-md border border-transparent transition-colors hover:border-(--color-border-settings-input) hover:bg-[var(--color-bg-settings-input)]",
							showQR ? "bg-[var(--color-bg-settings-input)] text-settings-title" : "text-settings-muted",
						)}
					>
						<QrCode className="size-4" aria-hidden="true" />
					</button>
				</div>
			</div>

			<div
				data-testid="ios-store-qr"
				className={cn(
					"grid transition-[grid-template-rows] duration-300 ease-out",
					showQR ? "grid-rows-[1fr]" : "grid-rows-[0fr]",
				)}
				aria-hidden={!showQR}
			>
				<div className="overflow-hidden px-4">
					<div
						className={cn(
							"flex flex-col items-center pt-3 transition-opacity duration-300 ease-out",
							showQR ? "opacity-100" : "opacity-0",
						)}
					>
						<div className="rounded-md border border-(--color-border-settings-input) bg-white p-2">
							<QRCodeSVG value={IOS_APP_STORE_URL} size={STORE_QR_SIZE} className="block" />
						</div>
						<p className="mt-2 text-caption text-settings-muted">{t("mobile.qrHint")}</p>
					</div>
				</div>
			</div>

			{/* Android — available directly from Google Play. */}
			<div className="flex items-center justify-between gap-3 px-3 py-3">
				<div className="flex min-w-0 flex-col">
					<span className="text-sm leading-5 text-settings-label">{t("mobile.android")}</span>
					<span className="text-caption leading-(--leading-settings-mobile-hint) text-settings-muted">
						{t("mobile.androidHint")}
					</span>
				</div>
				<Button
					type="button"
					variant="footer"
					className="rounded-md"
					aria-label={t("mobile.androidSignupAria")}
					onClick={() => void aoBridge.app.openExternal(ANDROID_PLAY_STORE_URL)}
				>
					{t("mobile.getApp")}
				</Button>
			</div>
		</div>
	);
}

import { useQuery } from "@tanstack/react-query";
import { ArrowUpRight } from "lucide-react";
import { useTranslation } from "react-i18next";

import { isMacPlatform } from "../lib/platform";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { Button } from "./ui/button";

type EmulatorPanelProps = {
	active: boolean;
	poppedOut?: boolean;
	onTogglePopOut?: () => void;
};

/**
 * Emulator panel renders the mobile emulator surface inside the session
 * inspector. For Track B (iOS Simulator), on macOS without Xcode an onboarding
 * banner offers a single-click path to install Xcode; users who already have
 * Xcode never see it. On Windows/Linux the banner never renders (gated by
 * isMacPlatform()).
 *
 * Track A (Android emulator) integration is a separate change (PR #3882);
 * this component currently renders the iOS Xcode onboarding surface only.
 */
export function EmulatorPanel({ active }: EmulatorPanelProps) {
	const { t } = useTranslation();

	// Query the iOS toolchain status only on macOS.
	const iosStatus = useQuery({
		queryKey: ["ios-device", "toolchain", "status"],
		queryFn: async () => {
			const resp = await apiClient.GET("/api/v1/ios-device/toolchain/status");
			if (resp.error) throw new Error(apiErrorMessage(resp.error, "Failed to fetch iOS toolchain status"));
			return resp.data;
		},
		enabled: isMacPlatform(),
		refetchInterval: false,
	});

	if (!active) return null;

	const xcodeDetected = iosStatus.data?.xcodeDetected ?? false;
	const cltOnly = iosStatus.data?.cltOnly ?? false;
	const guidanceAppStoreURL = iosStatus.data?.guidanceAppStoreURL ?? "";
	const guidanceDeveloperURL = iosStatus.data?.guidanceDeveloperURL ?? "";
	const guidanceWhyMissing = iosStatus.data?.guidanceWhyMissing ?? "";

	// On macOS without Xcode, show the onboarding banner.
	const showXcodeOnboarding = isMacPlatform() && !xcodeDetected && !cltOnly;

	return (
		<div className="flex flex-col gap-3 p-3" role="tabpanel">
			{showXcodeOnboarding ? (
				<div className="rounded-md border border-border/60 bg-settings-input px-4 py-3">
					<div className="flex items-start gap-3">
						<div className="min-w-0 flex-1">
							<p className="text-sm-md font-semibold text-foreground">{t("emulator.installXcode")}</p>
							<p className="mt-1 text-caption leading-normal text-passive">{guidanceWhyMissing}</p>
						</div>
					</div>
					<div className="mt-3 flex flex-wrap gap-2">
						<Button
							onClick={() => {
								if (guidanceAppStoreURL) window.open(guidanceAppStoreURL, "_blank");
							}}
							size="sm"
							type="button"
							variant="outline"
						>
							<span className="inline-flex items-center gap-1.5">
								{t("emulator.installXcode")}
								<ArrowUpRight className="size-icon-2xs" aria-hidden="true" />
							</span>
						</Button>
						{guidanceDeveloperURL ? (
							<Button
								onClick={() => window.open(guidanceDeveloperURL, "_blank")}
								size="sm"
								type="button"
								variant="ghost"
							>
								<span className="inline-flex items-center gap-1.5">
									{t("emulator.appleDeveloperDownloads")}
									<ArrowUpRight className="size-icon-2xs" aria-hidden="true" />
								</span>
							</Button>
						) : null}
					</div>
				</div>
			) : null}

			{/* iOS Simulator surface — macOS only, shown once Xcode is present. */}
			{isMacPlatform() && xcodeDetected ? (
				<div className="rounded-md border border-border/60 bg-settings-input px-4 py-3">
					<p className="text-sm-md font-semibold text-foreground">{t("emulator.iosSimulator")}</p>
					<p className="mt-1 text-caption text-passive">{t("emulator.xcodeDetectedDescription")}</p>
				</div>
			) : null}
		</div>
	);
}
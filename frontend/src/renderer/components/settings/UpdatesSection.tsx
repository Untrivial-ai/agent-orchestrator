import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { AlertTriangle, Clock3, Loader2, RefreshCw } from "lucide-react";
import { AnimatePresence, motion } from "motion/react";
import { useTranslation } from "react-i18next";
import { aoBridge } from "../../lib/bridge";
import { cn } from "../../lib/utils";
import { useUiStore } from "../../stores/ui-store";
import { useUpdateStatus } from "../../hooks/useUpdateStatus";
import type { UpdateChannel, UpdateSettings, UpdateState, UpdateStatus } from "../../../main/update-settings";
import { Badge } from "../ui/badge";
import { Button } from "../ui/button";
import { Switch } from "../ui/switch";
import { ConfirmDialog } from "../ConfirmDialog";
import { SettingsOptionMenu } from "./SettingsOptionMenu";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";
import { captureRendererEvent, releaseChannelFrom, setReleaseChannelContext } from "../../lib/telemetry";

export const updateSettingsQueryKey = ["update-settings"] as const;

type PrimaryValue = UpdateChannel | "feature";

const DEFAULT_SETTINGS: UpdateSettings = { enabled: false, channel: "latest", nightlyAck: false, feature: null };
const MIN_MANUAL_CHECK_VISIBLE_MS = 1_000;

let updateRequestSequence = 0;

function nextUpdateRequestId(prefix = "feature-update"): string {
	updateRequestSequence += 1;
	return `${prefix}-${updateRequestSequence}`;
}

export function UpdatesSection({ titleHidden }: { titleHidden?: boolean } = {}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const query = useQuery({
		queryKey: updateSettingsQueryKey,
		queryFn: () => aoBridge.updateSettings.get(),
	});

	const [form, setForm] = useState<UpdateSettings>(DEFAULT_SETTINGS);
	const formRef = useRef(form);
	formRef.current = form;
	const [showFeature, setShowFeature] = useState(false);
	const [savingField, setSavingField] = useState<"automatic" | "channel" | null>(null);
	const [pendingPin, setPendingPin] = useState<{ pr: number; title: string } | null>(null);
	const [manualCheckRequestId, setManualCheckRequestId] = useState<string | null>(null);
	const [channelSwitch, setChannelSwitch] = useState<{ channel: UpdateChannel; requestId: string } | null>(null);
	const channelSwitchRef = useRef<typeof channelSwitch>(null);
	channelSwitchRef.current = channelSwitch;
	const manualCheckStartedAtRef = useRef<number | null>(null);
	const manualCheckFinishTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
	const developerMode = useUiStore((state) => state.developerMode);

	const finishManualCheck = (requestId: string) => {
		if (manualCheckFinishTimerRef.current !== null) clearTimeout(manualCheckFinishTimerRef.current);
		const elapsed = manualCheckStartedAtRef.current === null ? MIN_MANUAL_CHECK_VISIBLE_MS : Date.now() - manualCheckStartedAtRef.current;
		const clear = () => {
			manualCheckFinishTimerRef.current = null;
			setManualCheckRequestId((pending) => {
				if (pending !== requestId) return pending;
				manualCheckStartedAtRef.current = null;
				return null;
			});
		};
		const remaining = Math.max(0, MIN_MANUAL_CHECK_VISIBLE_MS - elapsed);
		if (remaining === 0) clear();
		else manualCheckFinishTimerRef.current = setTimeout(clear, remaining);
	};

	const startManualCheck = (requestId: string) => {
		manualCheckStartedAtRef.current = Date.now();
		setManualCheckRequestId(requestId);
	};

	const status = useUpdateStatus((next) => {
		if (next.requestId && next.requestId === manualCheckRequestId && next.state !== "checking") {
			finishManualCheck(next.requestId);
		}
		const pending = channelSwitchRef.current;
		if (pending && next.requestId === pending.requestId && ["not-available", "error", "unsupported"].includes(next.state)) {
			setChannelSwitch(null);
		}
	});

	useEffect(
		() => () => {
			if (manualCheckFinishTimerRef.current !== null) clearTimeout(manualCheckFinishTimerRef.current);
		},
		[],
	);
	// Set only for the owned pin/home transition request, so unrelated hourly
	// updater events cannot auto-progress through download/install.
	const autoProgressRef = useRef<string | null>(null);
	const handledStatusRef = useRef<UpdateState | null>(null);

	useEffect(() => {
		if (query.data) setForm(query.data);
	}, [query.data]);

	useEffect(() => {
		const requestId = autoProgressRef.current;
		if (!requestId || status.requestId !== requestId) return;
		if (handledStatusRef.current === status.state) return;
		handledStatusRef.current = status.state;
		if (status.state === "available") {
			void aoBridge.updates.download(requestId);
		} else if (status.state === "downloaded") {
			void aoBridge.updates.install();
			autoProgressRef.current = null;
		} else if (status.state === "error" || status.state === "unsupported" || status.state === "not-available") {
			autoProgressRef.current = null;
		}
	}, [status]);

	const save = useMutation({
		mutationFn: async (next: UpdateSettings) => {
			await aoBridge.updateSettings.set(next);
			return next;
		},
		onSuccess: (next) => {
			setSavingField(null);
			setForm(next);
			void queryClient.invalidateQueries({ queryKey: updateSettingsQueryKey });
		},
		onError: () => {
			setSavingField(null);
			const previous = queryClient.getQueryData<UpdateSettings>(updateSettingsQueryKey);
			if (previous) setForm(previous);
		},
	});

	const channelOptions: { value: PrimaryValue; label: string }[] = [
		{ value: "latest", label: t("settings.updates.channel.stable") },
		{ value: "nightly", label: t("settings.updates.channel.nightly") },
		{ value: "feature", label: t("settings.updates.channel.feature") },
	];
	const primaryValue: PrimaryValue = developerMode && (form.feature !== null || showFeature) ? "feature" : form.channel;

	const setEnabled = (enabled: boolean) => {
		setSavingField("automatic");
		const next = { ...formRef.current, enabled };
		setForm(next);
		save.mutate(next);
	};

	const handlePrimaryChannel = (value: PrimaryValue) => {
		if (value === "feature") {
			setShowFeature(true);
			return;
		}
		setShowFeature(false);
		setSavingField("channel");
		const next = {
			...formRef.current,
			channel: value,
			nightlyAck: value === "nightly",
			feature: null,
		};
		const from = releaseChannelFrom(formRef.current);
		const to = releaseChannelFrom(next);
		setForm(next);
		save.mutate(next);
		if (from !== to) {
			// Reported on the switch rather than inferred later, because someone who
			// moves to nightly and does not update yet is on nightly by intent while
			// still running a stable build.
			setReleaseChannelContext(to);
			void captureRendererEvent("ao.renderer.update_channel_changed", { from_channel: from, to_channel: to });
		}
		const requestId = nextUpdateRequestId("channel-update");
		setChannelSwitch({ channel: value, requestId });
		startManualCheck(requestId);
		void aoBridge.updates
			.check({ settings: next, requestId })
			.catch(() => {
				setChannelSwitch((pending) => (pending?.requestId === requestId ? null : pending));
			})
			.finally(() => finishManualCheck(requestId));
	};

	const confirmPinBuild = async () => {
		if (!pendingPin) return;
		const { pr } = pendingPin;
		setPendingPin(null);
		const next = { ...formRef.current, feature: { pr } };
		setForm(next);
		const requestId = nextUpdateRequestId();
		autoProgressRef.current = requestId;
		handledStatusRef.current = null;
		try {
			await aoBridge.updates.check({ settings: next, requestId });
			void queryClient.invalidateQueries({ queryKey: updateSettingsQueryKey });
		} catch {
			if (autoProgressRef.current === requestId) autoProgressRef.current = null;
			void queryClient.invalidateQueries({ queryKey: updateSettingsQueryKey });
		}
	};

	const handleReturnToHome = async () => {
		setShowFeature(false);
		// Optimistic; the main process clears the pin against persisted state.
		setForm({ ...formRef.current, feature: null });
		const requestId = nextUpdateRequestId();
		autoProgressRef.current = requestId;
		handledStatusRef.current = null;
		try {
			// Single updater-serialized op: clears the pin and checks the home channel
			// atomically, so a concurrent settings-write cannot restore the pin.
			await aoBridge.updates.returnHome(requestId);
			void queryClient.invalidateQueries({ queryKey: updateSettingsQueryKey });
		} catch {
			if (autoProgressRef.current === requestId) autoProgressRef.current = null;
			// The optimistic form update may now disagree with disk; re-sync to truth.
			void queryClient.invalidateQueries({ queryKey: updateSettingsQueryKey });
		}
	};

	const activeQuery = useQuery({
		queryKey: ["feature-active"],
		queryFn: () => aoBridge.featureBuilds.getActive(),
	});
	const activeBuild = activeQuery.data ?? null;
	// Show the escape hatch whenever a feature build is running or pinned.
	const featurePr = activeBuild?.pr ?? (developerMode ? null : (form.feature?.pr ?? null));

	return (
		<>
			<SettingsSection title={t("settings.updates")} sectionId="updates" titleHidden={titleHidden} grouped>
				<UpdateActions
					status={status}
					manualCheckRequestId={manualCheckRequestId}
					startManualCheck={startManualCheck}
					finishManualCheck={finishManualCheck}
					channelSwitch={channelSwitch}
				/>

				{featurePr != null && (
					<div className="settings-row-bar h-auto min-h-(--size-settings-row) items-start gap-3 py-3">
						<Badge className="mt-0.5" variant="accent">PR #{featurePr}</Badge>
						<div className="min-w-0 flex-1">
							<p className="text-sm leading-5 text-settings-label">
								{activeBuild
									? t("settings.updates.onFeatureBuild", { pr: featurePr })
									: t("settings.updates.featurePinned", { pr: featurePr })}
							</p>
							<p className="mt-1 text-xs leading-4 text-settings-muted">
								{t("settings.updates.featureTracking", { pr: featurePr })}
							</p>
						</div>
						<Button type="button" variant="outline" size="sm" onClick={() => void handleReturnToHome()}>
							{form.channel === "nightly" ? t("settings.updates.returnToNightly") : t("settings.updates.returnToStable")}
						</Button>
					</div>
				)}

				<SettingsRow label={t("settings.updates.automatic")}>
					<Switch
						aria-label={t("settings.updates.automatic")}
						checked={form.enabled}
						onCheckedChange={setEnabled}
						disabled={savingField === "automatic"}
					/>
				</SettingsRow>

				<SettingsRow label={t("settings.updates.channel")}>
					<SettingsOptionMenu
						aria-label={t("settings.updates.channel")}
						value={primaryValue}
						options={developerMode ? channelOptions : channelOptions.filter((option) => option.value !== "feature")}
						onChange={handlePrimaryChannel}
						disabled={savingField === "channel"}
					/>
				</SettingsRow>

				{primaryValue === "feature" && (
					<FeatureBuildsSelect
						currentPr={form.feature?.pr ?? null}
						onPin={(pr, title) => setPendingPin({ pr, title })}
					/>
				)}

				{primaryValue === "nightly" && (
					<p className="nightly-warning -mt-1 pl-3 pr-(--size-settings-row-padding) pb-(--size-settings-row-padding) text-xs leading-row text-warning">
						{t("settings.updates.nightlyWarning")}
					</p>
				)}

				{save.isError && (
					<p className="mt-2 px-(--size-settings-row-padding) text-xs text-error">
						{save.error instanceof Error ? save.error.message : t("settings.updates.saveFailed")}
					</p>
				)}
			</SettingsSection>
			<ConfirmDialog
				open={pendingPin !== null}
				title={t("settings.updates.switchFeatureTitle")}
				description={pendingPin ? t("settings.updates.switchFeatureBody", pendingPin) : null}
				confirmLabel={t("settings.updates.confirm")}
				onConfirm={() => void confirmPinBuild()}
				onOpenChange={(open) => !open && setPendingPin(null)}
			/>
		</>
	);
}

function FeatureBuildsSelect({
	currentPr,
	onPin,
}: {
	currentPr: number | null;
	onPin: (pr: number, title: string) => void;
}) {
	const { t } = useTranslation();
	const buildsQuery = useQuery({ queryKey: ["feature-builds"], queryFn: () => aoBridge.featureBuilds.list() });
	const builds = buildsQuery.data ?? [];

	if (!buildsQuery.isLoading && builds.length === 0) {
		return <p className="px-3 text-xs text-settings-muted">{t("settings.updates.noFeatureReleases")}</p>;
	}

	return (
		<SettingsRow label={t("settings.updates.featureBuild")}>
			<SettingsOptionMenu
				aria-label={t("settings.updates.featureBuild")}
				value={currentPr === null ? "__none__" : currentPr.toString()}
				placeholder={t("settings.updates.selectFeature")}
				options={builds.map((build) => ({ value: build.pr.toString(), label: `PR #${build.pr}: ${build.title}` }))}
				disabled={buildsQuery.isLoading}
				onChange={(value) => {
					const build = builds.find((item) => item.pr === Number(value));
					if (build) onPin(build.pr, build.title);
				}}
			/>
		</SettingsRow>
	);
}

function UpdateActions({
	status,
	manualCheckRequestId,
	startManualCheck,
	finishManualCheck,
	channelSwitch,
}: {
	status: UpdateStatus;
	manualCheckRequestId: string | null;
	startManualCheck: (requestId: string) => void;
	finishManualCheck: (requestId: string) => void;
	channelSwitch: { channel: UpdateChannel; requestId: string } | null;
}) {
	const { t, i18n } = useTranslation();
	const version = useQuery({ queryKey: ["app-version"], queryFn: () => aoBridge.app.getVersion() });
	const installedChannel = installedUpdateChannel(version.data);
	const effectiveStatus = status;

	const manualCheckPending = manualCheckRequestId !== null;
	const checking = effectiveStatus.state === "checking" || manualCheckPending;
	const downloading = effectiveStatus.state === "downloading";
	const busy = checking || downloading;
	const displayStatus: UpdateStatus = manualCheckPending && effectiveStatus.state !== "checking" ? { ...effectiveStatus, state: "checking" } : effectiveStatus;
	const checkedAt = effectiveStatus.checkedAt
		? new Intl.DateTimeFormat(i18n.resolvedLanguage ?? i18n.language, {
				dateStyle: "medium",
				timeStyle: "short",
			}).format(effectiveStatus.checkedAt)
			: null;
	const channelSwitchInFlight = channelSwitch !== null && (!status.requestId || status.requestId === channelSwitch.requestId);
	const channelSwitchMessage = channelSwitchInFlight &&
		(displayStatus.state === "available" || displayStatus.state === "downloading" || displayStatus.state === "downloaded")
		? t(displayStatus.state === "downloaded" ? "settings.updates.channelSwitchRestart" : "settings.updates.channelSwitchUpdate", {
			channel: channelSwitch.channel === "nightly" ? t("settings.updates.channel.nightly") : t("settings.updates.channel.stable"),
		})
		: null;

	const checkNow = async () => {
		const requestId = nextUpdateRequestId("manual-update");
		startManualCheck(requestId);
		try {
			await aoBridge.updates.check({ requestId });
		} catch {
			// The main process publishes the actionable updater error state.
		} finally {
			finishManualCheck(requestId);
		}
	};

	return (
		<div className="settings-row-bar update-status-row h-auto flex-col items-stretch gap-4 py-4">
			<div className="grid min-w-0 gap-4 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start">
				<div className="min-w-0">
					<div className="flex min-w-0 flex-wrap items-center gap-2">
						<span
							aria-label={t("settings.updates.currentVersion", { version: version.data ? `v${version.data}` : "…" })}
							className="min-w-0 text-2xl font-semibold leading-none tracking-tight tabular-nums text-settings-label"
							data-testid="app-version"
						>
							{version.data ? `v${version.data}` : "…"}
						</span>
						<Badge data-testid="installed-update-channel" variant={installedChannel === "nightly" ? "warning" : "neutral"}>
							{installedChannel === "nightly" ? t("settings.updates.channel.nightly") : t("settings.updates.channel.stable")}
						</Badge>
					</div>

					<div
						id="update-status-line"
						role="status"
						aria-live="polite"
						aria-atomic="true"
						aria-busy={checking}
						className="mt-2 min-w-0"
					>
						{displayStatus.state === "checking" ? (
							<span className="sr-only">{t("settings.updates.checking")}</span>
						) : displayStatus.state !== "available" && displayStatus.state !== "idle" && displayStatus.state !== "downloading" ? (
							<UpdateStatusLine status={displayStatus} />
						) : null}
						{channelSwitchMessage && <p className="mt-1 text-xs leading-4 text-settings-muted">{channelSwitchMessage}</p>}
					</div>

					<div className="relative mt-2 h-5 text-sm font-medium leading-5 text-settings-muted">
						<AnimatePresence initial={false} mode="wait">
							{checkedAt ? (
								<motion.div
									key={checkedAt}
									initial={{ opacity: 0, filter: "blur(4px)" }}
									animate={{ opacity: 1, filter: "blur(0px)" }}
									exit={{ opacity: 0, filter: "blur(4px)" }}
									transition={{ duration: 0.22, ease: "easeOut" }}
									className="absolute inset-0 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1"
								>
									<span className="inline-flex items-center gap-1.5 tabular-nums" data-testid="update-checked-at">
										<Clock3 className="size-3" aria-hidden="true" />
										{t("settings.updates.lastChecked", { time: checkedAt })}
									</span>
								</motion.div>
							) : displayStatus.state === "idle" ? (
								<motion.span
									key="not-checked"
									initial={{ opacity: 1, filter: "blur(0px)" }}
									animate={{ opacity: 1, filter: "blur(0px)" }}
									exit={{ opacity: 0, filter: "blur(4px)" }}
									className="absolute inset-0"
								>
									{t("settings.updates.notChecked")}
								</motion.span>
							) : null}
						</AnimatePresence>
					</div>
				</div>

				<div className="flex w-full flex-wrap items-center gap-2 sm:w-auto sm:justify-end [&>button]:flex-1 sm:[&>button]:flex-none">
					{effectiveStatus.state === "downloading" && (
						<Button type="button" variant="primary" size="sm" disabled>
							<DownloadProgressIcon percent={effectiveStatus.percent ?? 0} />
							{t("settings.updates.downloading", { percent: effectiveStatus.percent ?? 0 })}
						</Button>
					)}
					{!checking && effectiveStatus.state === "available" && (
						<Button type="button" variant="primary" size="sm" onClick={() => void aoBridge.updates.download()}>
							{effectiveStatus.version ? t("settings.updates.updateTo", { version: `v${effectiveStatus.version}` }) : t("settings.updates.updateToLatest")}
						</Button>
					)}
					{!checking && effectiveStatus.state === "downloaded" && (
						<Button type="button" variant="primary" size="sm" onClick={() => void aoBridge.updates.install()}>
							{t("settings.updates.restartInstall")}
						</Button>
					)}
					{displayStatus.state !== "available" && displayStatus.state !== "downloaded" && displayStatus.state !== "downloading" && <Button
						type="button"
						aria-label={checking ? t("settings.updates.checking") : t("settings.updates.check")}
						aria-describedby="update-status-line"
						variant="outline"
						size="sm"
						className="min-w-36"
						onClick={() => void checkNow()}
						disabled={busy}
					>
						{checking ? (
							<Loader2 className="size-icon-sm animate-spin motion-reduce:animate-none" aria-hidden="true" />
						) : (
							<RefreshCw className="size-icon-sm" aria-hidden="true" />
						)}
						{checking ? t("settings.updates.checking") : t("settings.updates.check")}
					</Button>}
				</div>
			</div>

			{!status.staleCheckNudge && status.checksFailing && (
				<p className="flex items-start gap-2 text-xs leading-5 text-warning">
					<AlertTriangle className="mt-0.5 size-icon-sm shrink-0" aria-hidden="true" />
					<span>{t("settings.updates.checksFailing")}</span>
				</p>
			)}

			{status.staleCheckNudge && (
				<p className="flex items-start gap-2 text-xs leading-5 text-warning">
					<AlertTriangle className="mt-0.5 size-icon-sm shrink-0" aria-hidden="true" />
					<span>{t("settings.updates.networkStale")}</span>
				</p>
			)}
		</div>
	);
}

function DownloadProgressIcon({ percent }: { percent: number }) {
	const clamped = Math.min(100, Math.max(0, percent));
	return (
		<span className="relative grid size-icon-sm shrink-0 place-items-center" aria-hidden="true">
			<svg className="absolute inset-0 size-full -rotate-90" viewBox="0 0 24 24" fill="none">
				<circle cx="12" cy="12" r="9" className="stroke-current/20" strokeWidth="2.5" />
				<circle cx="12" cy="12" r="9" className="stroke-current" strokeWidth="2.5" strokeLinecap="round" strokeDasharray={`${clamped * 0.5655} 56.55`} />
			</svg>
		</span>
	);
}

function installedUpdateChannel(version: string | undefined): UpdateChannel {
	return /-nightly(?:[.+]|$)/.test(version ?? "") ? "nightly" : "latest";
}

function UpdateStatusLine({ status }: { status: UpdateStatus }) {
	const { t } = useTranslation();
	let className = "text-settings-muted";
	let label: string;

	switch (status.state) {
		case "checking":
			label = t("settings.updates.checking");
			break;
		case "available":
			className = "text-settings-label";
			label = t("settings.updates.available", { version: status.version ? ` (v${status.version})` : "" });
			break;
		case "downloading":
			className = "text-settings-label tabular-nums";
			label = t("settings.updates.downloading", { percent: status.percent ?? 0 });
			break;
		case "downloaded":
			className = "text-success";
			label = t("settings.updates.downloaded");
			break;
		case "not-available":
			className = "text-success";
			label = t("settings.updates.latest");
			break;
		case "unsupported":
			label = status.message ?? t("settings.updates.needInstalledApp");
			break;
		case "error":
			className = "text-error";
			label = status.netError
				? t("settings.updates.netErrorRestartGuidance")
				: status.message ?? t("settings.updates.updateFailed");
			break;
		default:
			label = t("settings.updates.notChecked");
	}

	return (
		<p className={cn("text-pretty text-sm font-medium leading-5", className)}>{label}</p>
	);
}

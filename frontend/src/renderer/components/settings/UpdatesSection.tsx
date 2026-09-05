import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { AlertTriangle, CheckCircle2, Clock3, Download, Info, Loader2, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";
import { aoBridge } from "../../lib/bridge";
import { cn } from "../../lib/utils";
import { parseNightlyVersion } from "../../lib/build-channel";
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
// Hard ceiling on how long a manual check may hold the button. The IPC call is
// awaited and normally settles on its own, but if it ever does not, the pending
// request id disables the Check button for the rest of the session with nothing
// on screen to explain it. Releasing the button is always safe: the main process
// serializes updater operations, so a redundant check queues rather than racing.
const MAX_MANUAL_CHECK_MS = 90_000;

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
	const manualCheckWatchdogRef = useRef<ReturnType<typeof setTimeout> | null>(null);
	const developerMode = useUiStore((state) => state.developerMode);

	const clearManualCheckWatchdog = () => {
		if (manualCheckWatchdogRef.current === null) return;
		clearTimeout(manualCheckWatchdogRef.current);
		manualCheckWatchdogRef.current = null;
	};

	const finishManualCheck = (requestId: string) => {
		clearManualCheckWatchdog();
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
		clearManualCheckWatchdog();
		manualCheckStartedAtRef.current = Date.now();
		setManualCheckRequestId(requestId);
		manualCheckWatchdogRef.current = setTimeout(() => {
			manualCheckWatchdogRef.current = null;
			setManualCheckRequestId((pending) => (pending === requestId ? null : pending));
		}, MAX_MANUAL_CHECK_MS);
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
			if (manualCheckWatchdogRef.current !== null) clearTimeout(manualCheckWatchdogRef.current);
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

				<SettingsRow
					label={t("settings.updates.automatic")}
					description={t("settings.updates.automaticHelp")}
				>
					<Switch
						aria-label={t("settings.updates.automatic")}
						checked={form.enabled}
						onCheckedChange={setEnabled}
						disabled={savingField === "automatic"}
					/>
				</SettingsRow>

				<SettingsRow
					label={t("settings.updates.channel")}
					description={t("settings.updates.channelHelp")}
				>
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
					// Helper copy for the row above, not an alert. This is a standing
					// property of the channel the user chose, not something wrong right
					// now, and --color-warning is a red-orange one step from
					// --destructive: rendered in it, a permanent note read as a permanent
					// failure. Red is kept for states that are actually broken and
					// actionable — failing checks, a stale network stack, update errors.
					<p className="nightly-warning -mt-1 flex items-start gap-2 px-(--size-settings-row-padding) pb-(--size-settings-row-padding) text-xs leading-4 text-settings-muted">
						<Info className="mt-px size-icon-sm shrink-0" aria-hidden="true" />
						<span className="min-w-0">{t("settings.updates.nightlyWarning")}</span>
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
	const locale = i18n.resolvedLanguage ?? i18n.language;
	const version = useQuery({ queryKey: ["app-version"], queryFn: () => aoBridge.app.getVersion() });
	const openUpdateInstallPrompt = useUiStore((state) => state.openUpdateInstallPrompt);
	const installedChannel = installedUpdateChannel(version.data);
	const installed = parseNightlyVersion(version.data);

	const manualCheckPending = manualCheckRequestId !== null;
	const checking = status.state === "checking" || manualCheckPending;
	const replacementAvailable = status.state === "replacing" && status.replacementPhase === "checking";
	const downloading = status.state === "downloading" || (status.state === "replacing" && !replacementAvailable);
	// Only the user's OWN in-flight check blocks the button. A background check
	// also reports "checking", and gating on that swallowed the first click
	// whenever Settings was opened during one — every 15 minutes on nightly.
	// A click during a background check is fine: the main process serializes
	// updater operations, so the manual check simply queues behind it.
	const busy = manualCheckPending || downloading;
	// The minimum-spinner window keeps "checking" on screen briefly after the
	// updater has already answered, so the status line and the primary action
	// read from the live state and only the button's own label follows `checking`.
	const displayState: UpdateState = checking && !downloading && status.state !== "replacement-failed" ? "checking" : status.state;

	const checkedAt = status.checkedAt
		? new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(status.checkedAt)
		: null;

	const channelSwitchInFlight = channelSwitch !== null && (!status.requestId || status.requestId === channelSwitch.requestId);
	// Use the live updater state, not displayState: the manual-check minimum
	// spinner time forces displayState back to "checking" even after a channel
	// switch finds an update, which would hide this guidance until the timer fires.
	// Not shown for "downloaded": the status line already says "Downloaded.
	// Restart to finish updating." and names the build under it, so adding
	// "Restart to switch to Nightly." put two restart sentences side by side.
	const channelSwitchMessage = channelSwitchInFlight &&
		(status.state === "available" || status.state === "downloading")
		? t("settings.updates.channelSwitchUpdate", {
			// Short form: "Restart to switch to Nightly (Pre-release)." reads as a
			// parenthetical stuck mid-sentence. The suffix belongs in the picker.
			channel: channelSwitch.channel === "nightly" ? t("settings.updates.channel.nightlyShort") : t("settings.updates.channel.stable"),
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

	// Deliberately no border or accent fill on this block. --primary is
	// oklch(0.92 0.004 286.32) in dark and near-black in light: both are
	// effectively neutral, so tinting drew a grey frame that carried no meaning
	// and competed with the row backgrounds around it. The primary button and
	// the status line already carry the emphasis.
	return (
		<div className="settings-row-bar update-status-row h-auto flex-col items-stretch gap-4 py-4">
			<div className="flex min-w-0 flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
				{/* Identity. The nightly stamp lives on its own monospace line: as one
				    heading it wrapped mid-token and swallowed the row. */}
				<div className="min-w-0 flex-1">
					<p className="text-caption font-medium uppercase tracking-wide text-settings-muted">
						{t("settings.updates.installedVersion")}
					</p>
					<div className="mt-1 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
						<span
							aria-label={t("settings.updates.currentVersion", { version: version.data ? `v${version.data}` : "…" })}
							className="text-2xl font-semibold leading-none tracking-tight tabular-nums text-settings-label"
							data-testid="app-version"
						>
							{version.data ? `v${installed?.base ?? version.data}` : "…"}
						</span>
						<Badge data-testid="installed-update-channel" variant="neutral">
							{installedChannel === "nightly" ? t("settings.updates.channel.nightlyShort") : t("settings.updates.channel.stable")}
						</Badge>
					</div>
					{installed && (
						// Two spans, not one string: the version needs break-all so a long
						// nightly stamp can never overflow, but break-all applied to the
						// whole line also snapped the date mid-word ("20/26, 5:21 PM").
						<p className="mt-1.5 flex min-w-0 flex-wrap items-baseline gap-x-1.5 text-caption leading-4 text-settings-muted">
							<span className="min-w-0 break-all font-mono">{version.data}</span>
							<span className="whitespace-nowrap">
								{t("settings.updates.nightlyBuiltAt", {
									date: new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(installed.builtAt),
								})}
							</span>
						</p>
					)}
				</div>

				{/* Actions. Fixed to the right and never widened by a version string:
				    the primary label used to carry the full nightly stamp and grew
				    across the heading. The target build is named in the status line. */}
				<div className="flex w-full shrink-0 flex-wrap items-center gap-2 sm:w-auto sm:justify-end [&>button]:flex-1 sm:[&>button]:flex-none">
					{replacementAvailable && (
						<Button type="button" variant="primary" size="sm" onClick={() => void aoBridge.updates.download()}>
							{t("settings.updates.updateTo", { version: status.replacementCandidate?.version ?? status.version })}
						</Button>
					)}
					{status.state === "replacing" && status.replacementPhase === "native-handoff" && (
						<Button type="button" variant="primary" size="sm" disabled aria-describedby="update-replacement-install-reason">
							{t("settings.updates.update")}
						</Button>
					)}
					{status.state === "replacing" && !replacementAvailable && status.replacementPhase !== "native-handoff" && (
						<Button
							type="button"
							variant="primary"
							size="sm"
							disabled
							aria-label={t("settings.updates.downloadingVersion", { version: status.replacementCandidate?.version ?? status.version })}
							aria-describedby="update-replacement-install-reason"
						>
							<DownloadProgressIcon percent={status.percent ?? 0} />
							{status.percent ?? 0}%
						</Button>
					)}
					{status.state === "replacement-failed" && (
						<Button type="button" variant="primary" size="sm" onClick={() => void aoBridge.updates.download()}>
							{t("settings.updates.retryVersion", { version: status.replacementCandidate?.version ?? status.version })}
						</Button>
					)}
					{status.state === "downloading" && (
						<Button type="button" variant="primary" size="sm" disabled>
							<DownloadProgressIcon percent={status.percent ?? 0} />
							{t("settings.updates.downloading", { percent: status.percent ?? 0 })}

						</Button>
					)}
					{status.state === "available" && (
						<Button type="button" variant="primary" size="sm"
							aria-label={status.version ? t("settings.updates.updateTo", { version: `v${status.version}` }) : t("settings.updates.updateToLatest")}
							onClick={() => void aoBridge.updates.download()}
						>
							<Download className="size-icon-sm" aria-hidden="true" />
							{t("settings.updates.update")}
						</Button>
					)}
					{status.state === "downloaded" && (
						// Opens the restart confirmation rather than installing outright:
						// installing quits the app, which costs a turn on any chat session
						// running a daemon-owned driver.
						<Button type="button" variant="primary" size="sm" onClick={openUpdateInstallPrompt} disabled={!!status.installDisabledReason} title={status.installDisabledReason}>
							<RefreshCw className="size-icon-sm" aria-hidden="true" />
							{t("settings.updates.restartInstall")}
						</Button>
					)}
					{/* Always rendered. Hiding it whenever something was available or
					    staged left no way to re-check at all: a user sitting on a build
					    that will not install had a single dead Restart button and no
					    other action on the page. */}
					<Button
						type="button"
						aria-label={checking ? t("settings.updates.checking") : t("settings.updates.check")}
						aria-describedby="update-status-line"
						variant="outline"
						size="sm"
						onClick={() => void checkNow()}
						disabled={busy}
					>
						{checking ? (
							<Loader2 className="size-icon-sm animate-spin motion-reduce:animate-none" aria-hidden="true" />
						) : (
							<RefreshCw className="size-icon-sm" aria-hidden="true" />
						)}
						{checking ? t("settings.updates.checking") : t("settings.updates.check")}
					</Button>
				</div>
			</div>

			{/* One status block, in normal flow. It used to be an absolutely
			    positioned h-5 strip, so a two-line message overlapped the
			    last-checked line and the cross-fade left it looking half-rendered. */}
			<div
				id="update-status-line"
				data-testid="update-status-line"
				role="status"
				aria-live={status.state === "replacing" ? "off" : "polite"}
				aria-atomic="true"
				aria-busy={checking}
				className="flex min-w-0 flex-col gap-1"
			>
				<UpdateStatusLine
					state={displayState}
					status={status}
					locale={locale}
				/>
				{channelSwitchMessage && <p className="text-xs leading-4 text-settings-muted">{channelSwitchMessage}</p>}
				{checkedAt ? (
					<p className="flex min-w-0 items-center gap-1.5 text-xs leading-4 tabular-nums text-settings-muted" data-testid="update-checked-at">
						<Clock3 className="size-3 shrink-0" aria-hidden="true" />
						{t("settings.updates.lastChecked", { time: checkedAt })}
					</p>
				) : null}
			</div>

			{!status.staleCheckNudge && status.checksFailing && (
				<UpdateNotice tone="warning" text={t("settings.updates.checksFailing")} />
			)}

			{status.staleCheckNudge && <UpdateNotice tone="warning" text={t("settings.updates.networkStale")} />}
		</div>
	);
}

/** Contained caution/failure note. A bare coloured paragraph read as body copy. */
function UpdateNotice({ tone, text }: { tone: "warning" | "error"; text: string }) {
	return (
		<p
			className={cn(
				"flex items-start gap-2 rounded-md border px-3 py-2 text-xs leading-5",
				tone === "warning" ? "border-warning/30 bg-warning/8 text-warning" : "border-error/30 bg-error/8 text-error",
			)}
		>
			<AlertTriangle className="mt-0.5 size-icon-sm shrink-0" aria-hidden="true" />
			<span className="min-w-0">{text}</span>
		</p>
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

/**
 * The single status line for the section. Every state gets a visible line with
 * a leading state icon: `available` used to be suppressed entirely (the button
 * was the only clue) and `checking` was rendered `sr-only`, which meant a
 * check in progress and a wedged check looked identical — an empty panel.
 */
function UpdateStatusLine({
	state,
	status,
	locale,
}: {
	state: UpdateState;
	status: UpdateStatus;
	locale: string;
}) {
	const { t } = useTranslation();
	let className = "text-settings-muted";
	let icon: ReactNode = null;
	let label: string;
	let detail: string | null = null;

	const targetBuild = (version: string | undefined): string | null => {
		if (!version) return null;
		const nightly = parseNightlyVersion(version);
		if (!nightly) return `v${version}`;
		// Same compact form as the sidebar row, so the two never disagree.
		return t("shell.nightlyBuild", {
			version: nightly.base,
			date: new Intl.DateTimeFormat(locale, { month: "short", day: "numeric" }).format(nightly.builtAt),
		});
	};

	switch (state) {
		case "replacing": {
			const from = status.stagedCandidate?.version ?? "?";
			const quitCandidates = [...new Set([from, ...(status.nativeCandidates ?? []).map((candidate) => candidate.version)])].join(", ");
			const to = status.replacementCandidate?.version ?? status.version ?? "?";
			const metrics = status.transferred !== undefined || status.total !== undefined || status.bytesPerSecond !== undefined || status.etaSeconds !== undefined
				? t("settings.updates.replacementMetrics", {
					transferred: status.transferred ?? "?",
					total: status.total ?? "?",
					rate: status.bytesPerSecond ?? "?",
					eta: status.etaSeconds ?? "?",
				})
				: null;
			return (
				<div className="text-settings-label">
					<p className="text-pretty text-sm font-medium leading-5">
						{status.replacementPhase === "checking"
							? t("settings.updates.updateTo", { version: to })
							: status.replacementPhase === "native-handoff"
								? status.installDisabledReason
								: t("settings.updates.replacingTitle", { from, to })}
					</p>
					<p id="update-replacement-install-reason" role="status" aria-live="polite" className="mt-1 text-xs leading-4 text-warning">
						{t("settings.updates.replacingWarning", { from: quitCandidates, phase: status.replacementPhase })}
					</p>
					{metrics && <p className="mt-1 text-xs leading-4 tabular-nums text-settings-muted">{metrics}</p>}
					<div role="progressbar" aria-live="off" aria-label={t("settings.updates.progressVersion", { version: to })} aria-valuemin={0} aria-valuemax={100} aria-valuenow={status.percent ?? 0} className="sr-only" />
				</div>
			);
		}
		case "replacement-failed": {
			const from = status.stagedCandidate?.version ?? "?";
			const quitCandidates = [...new Set([from, ...(status.nativeCandidates ?? []).map((candidate) => candidate.version)])].join(", ");
			const to = status.replacementCandidate?.version ?? status.version ?? "?";
			return (
				<div className="text-error">
					<p className="text-pretty text-sm font-medium leading-5">{t("settings.updates.replacementFailed", { to, message: status.message })}</p>
					<p id="update-replacement-install-reason" role="status" aria-live="polite" className="mt-1 text-xs leading-4 text-warning">{t("settings.updates.replacementFailedWarning", { from: quitCandidates, to })}</p>
				</div>
			);
		}
		case "checking":
			icon = <Loader2 className="size-icon-sm shrink-0 animate-spin motion-reduce:animate-none" aria-hidden="true" />;
			label = t("settings.updates.checking");
			break;
		case "available":
			className = "text-settings-label";
			icon = <Download className="size-icon-sm shrink-0" aria-hidden="true" />;
			label = t("settings.updates.availableNow");
			detail = targetBuild(status.version);
			break;
		case "downloading":
			className = "text-settings-label tabular-nums";
			icon = <DownloadProgressIcon percent={status.percent ?? 0} />;
			label = t("settings.updates.downloading", { percent: status.percent ?? 0 });
			detail = targetBuild(status.version);
			break;
		case "downloaded":
			className = "text-success";
			icon = <CheckCircle2 className="size-icon-sm shrink-0" aria-hidden="true" />;
			label = t("settings.updates.downloaded");
			detail = targetBuild(status.version);
			break;
		case "not-available":
			className = "text-success";
			icon = <CheckCircle2 className="size-icon-sm shrink-0" aria-hidden="true" />;
			label = t("settings.updates.latest");
			break;
		case "unsupported":
			icon = <Info className="size-icon-sm shrink-0" aria-hidden="true" />;
			label = status.message ?? t("settings.updates.needInstalledApp");
			break;
		case "error":
			className = "text-error";
			icon = <AlertTriangle className="size-icon-sm shrink-0" aria-hidden="true" />;
			label = status.netError
				? t("settings.updates.netErrorRestartGuidance")
				: status.message ?? t("settings.updates.updateFailed");
			break;
		default:
			label = t("settings.updates.notChecked");
	}

	return (
		<div className={cn("flex min-w-0 items-start gap-2", className)}>
			{icon !== null && <span className="mt-px shrink-0">{icon}</span>}
			<div className="min-w-0">
				<p className="text-pretty text-sm font-medium leading-5">{label}</p>
				{detail !== null && (
					<p className="mt-0.5 truncate text-xs leading-4 font-normal text-settings-muted">{detail}</p>
				)}
			</div>
		</div>
	);
}

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Fragment, useEffect, useRef, useState } from "react";
import { ArrowUpRight, Check, Copy, Loader2, RotateCcw } from "lucide-react";
import { apiClient, apiErrorMessage } from "../../lib/api-client";
import { aoBridge } from "../../lib/bridge";
import { captureRendererEvent } from "../../lib/telemetry";
import { ANDROID_PLAY_STORE_URL, IOS_APP_STORE_URL } from "./ConnectMobileGetApp";
import { reasonMessage, type SetupMode } from "./ConnectMobileSetup";
import { StyledQRCode } from "./StyledQRCode";
import { PairingQr } from "./PairingQr";
import { scramblePairingCodes } from "./qrScramble";
import { InstallCloudflared } from "./InstallCloudflared";
import { Button } from "../ui/button";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "../ui/dialog";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { cn } from "../../lib/utils";

// Match the panel's full width so the generated code uses the same visual
// footprint as the preview. The containing panel still scales it down on
// narrower layouts.
const QR_CODE_SIZE = 320;
const STORE_QR_SIZE = 140;

const STORE_LINKS = [
	{
		key: "ios",
		Icon: AppleIcon,
		url: IOS_APP_STORE_URL,
		labelKey: "mobile.ios",
		ariaKey: "mobile.iosStoreAria",
		testId: "ios-store-qr",
	},
	{
		key: "android",
		Icon: AndroidIcon,
		url: ANDROID_PLAY_STORE_URL,
		labelKey: "mobile.android",
		ariaKey: "mobile.androidSignupAria",
		testId: "android-play-qr",
	},
] as const;


import {
	buildPairingOffer,
	pairingCodeUrl,
	type PairingEndpoint,
} from "../../lib/pairing-payload";

export const mobileStatusQueryKey = ["mobile-status"] as const;

// One scan gives the mobile app every value required to connect. Keep the
// secure key absent for plaintext payloads so older mobile builds can decode
// the same bytes they already understand.
export function pairingPayload(host: string, port: number, password: string, secure?: boolean): string {
	return JSON.stringify(secure ? { v: 1, host, port, password, secure: true } : { v: 1, host, port, password });
}

/**
 * The value encoded into the pairing QR.
 *
 * Prefers the v2 deep link, which carries every endpoint the daemon advertises
 * so the phone can race them, and opens the app straight from the system
 * camera. The payload rides in the fragment, so the connection token never
 * reaches a server even when the link is opened in a browser.
 *
 * Falls back to the v1 payload when the daemon advertises no endpoints — an
 * older daemon, or one whose network is not up yet. Showing a dead v2 QR there
 * would be worse than the pairing that already works.
 */
export function qrValueFor(input: {
	hostId: string;
	host: string;
	platform: string;
	password: string;
	endpoints: readonly PairingEndpoint[];
}): string {
	// v2 only. The phone no longer accepts v1: those codes predate the identity
	// probe the race uses to verify a machine before sending it a credential, so
	// a daemon old enough to need one could never complete a race anyway. An
	// empty endpoint list means there is nothing to advertise yet, and the panel
	// shows the preparing state rather than a code — see qrIsReady.
	return pairingCodeUrl(
		buildPairingOffer({
			endpoints: input.endpoints,
			password: input.password,
			hostId: input.hostId,
			name: input.host,
			platform: input.platform,
		}),
		PAIRING_LINK_BASE,
	);
}

/**
 * How often to re-read mobile status, or false to stop.
 *
 * The connector takes roughly half a minute to become advertisable, and the
 * status query is otherwise fetched once on open and refetched only after a
 * mutation. Without this the modal would sit on "Preparing remote access…"
 * indefinitely while the daemon had long since finished.
 *
 * Polls only while there is a transient state to wait out, so an idle modal
 * does not keep hitting the daemon for the rest of the session.
 */
export function mobileStatusRefetchInterval(
	status: { tunnel?: { running: boolean; ready: boolean } } | undefined,
): number | false {
	const tunnel = status?.tunnel;
	return tunnel?.running && !tunnel.ready ? MOBILE_STATUS_POLL_MS : false;
}

const MOBILE_STATUS_POLL_MS = 2_000;

/**
 * Whether the pairing QR is safe to show.
 *
 * The connector takes roughly thirty seconds after the listener comes up
 * before its hostname resolves. A code scanned inside that window carries no
 * tunnel endpoint, so the pairing works on this network and fails everywhere
 * else — with nothing on either side to indicate why. Holding the code back is
 * the same discipline the daemon already applies to advertising the endpoint.
 *
 * A tunnel that is not running at all is not worth waiting for: LAN-only is a
 * legitimate setup, and blocking pairing forever would be worse than the wait.
 *
 * A daemon that does not report endpoints at all predates the endpoint race.
 * It has no tunnel to wait for, and its QR still works, so it is shown — the
 * absence of the field is not the same as an empty list.
 */
export function qrIsReady(status: {
	enabled: boolean;
	endpoints?: readonly PairingEndpoint[];
	tunnel?: { running: boolean; ready: boolean; [k: string]: unknown };
}): boolean {
	if (!status.enabled) return false;
	// Nothing to encode: a v2 code carries the endpoint list, and there is no
	// longer a v1 form to fall back to. An absent list is as unready as an empty
	// one — it means the daemon has not told us where it can be reached.
	if (!status.endpoints || status.endpoints.length === 0) return false;
	if (status.tunnel?.running && !status.tunnel.ready) return false;
	return true;
}

/** The app's registered scheme (app.json `expo.scheme`), not a universal link:
 * it works today, with no association files to host and no store listing
 * required. Universal links can be added later without changing the payload. */
const PAIRING_LINK_BASE = "aomobile://pair";

/** A decoy with the same payload shape as a real pairing offer. Keeping the
 * same QR version makes the blurred preview look like the code it becomes. */
const PLACEHOLDER_QR_VALUE = scramblePairingCodes(1)[0];

function AppleIcon({ className }: { className?: string }) {
	return (
		<svg aria-hidden="true" className={className} fill="currentColor" viewBox="0 0 384 512">
			<path d="M318.7 268.7c-.2-36.7 16.4-64.4 50-84.8-18.8-26.9-47.2-41.7-84.7-44.6-35.5-2.8-74.3 20.7-88.5 20.7-15 0-49.4-19.7-76.4-19.7C63.3 141.2 4 184.8 4 273.5q0 39.3 14.4 81.2c12.8 36.7 59 126.7 107.2 125.2 25.2-.6 43-17.9 75.8-17.9 31.8 0 48.3 17.9 76.4 17.9 48.6-.7 90.4-82.5 102.6-119.3-65.2-30.7-61.7-90-61.7-91.9zm-56.6-164.2c27.3-32.4 24.8-61.9 24-72.5-24.1 1.4-52 16.4-67.9 34.9-17.5 19.8-27.8 44.3-25.6 71.9 26.1 2 49.9-11.4 69.5-34.3Z" />
		</svg>
	);
}

function AndroidIcon({ className }: { className?: string }) {
	return (
		<svg aria-hidden="true" className={className} fill="currentColor" viewBox="0 0 576 512">
			<path d="M420.55 301.93a24 24 0 1 1 24-24 24 24 0 0 1-24 24m-265.1 0a24 24 0 1 1 24-24 24 24 0 0 1-24 24m273.7-144.48 47.94-83a10 10 0 1 0-17.27-10l-48.54 84.07a301.25 301.25 0 0 0-246.56 0L116.18 64.45a10 10 0 1 0-17.27 10l47.94 83C64.53 202.22 8.24 285.55 0 384h576c-8.24-98.45-64.54-181.78-146.85-226.55" />
		</svg>
	);
}

/** Trailing store link at the end of a walkthrough step. Border-bottom instead
 *  of text-decoration so the underline runs under the arrow too. */
const STEP_LINK_CLASS =
	"inline-flex items-center gap-0.5 border-b border-[color-mix(in_oklch,var(--color-settings-label)_45%,transparent)] align-baseline text-settings-label transition-colors hover:border-current hover:text-settings-title";

interface MobileStatus {
	enabled: boolean;
	/** This machine's stable identity, echoed into the pairing code so the phone
	 * can verify the endpoints it races. Optional: a daemon predating the
	 * endpoint race does not send it, and the QR falls back to the v1 payload. */
	hostId?: string;
	/** Every advertised way to reach this daemon, in preference order. Optional
	 * for the same reason as hostId. */
	endpoints?: PairingEndpoint[];
	/** Managed remote-access connector state, for showing progress while the
	 * tunnel comes up. */
	tunnel?: {
		/** False when this machine has no connector at all — cloudflared is
		 * absent. Distinct from "not started yet", which is running:false. */
		supported?: boolean;
		running: boolean;
		ready: boolean;
		hostname: string;
		location: string;
		lastError: string;
	};
	host: string;
	tailscaleHost: string;
	port: number;
	password: string;
	warning: string;
	securePairing: {
		enabled: boolean;
		available: boolean;
		active: boolean;
		host: string;
		port: number;
		reason: string;
	};
}

export async function fetchMobileStatus(): Promise<MobileStatus> {
	const { data, error } = await apiClient.GET("/api/v1/mobile/status");
	if (error || !data) throw new Error(apiErrorMessage(error));
	return data;
}

export function ConnectMobileContent({ active }: { active: boolean }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const [copied, setCopied] = useState(false);
	const [optimisticEnabled, setOptimisticEnabled] = useState<boolean | null>(null);
	const [trustConfirmationOpen, setTrustConfirmationOpen] = useState(false);
	const copiedTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
	const lastQrValueRef = useRef<string | null>(null);
	// Pinned to "lan" while the connection picker below is commented out.
	// setMode is still called by the close-reset effect.
	const [mode, setMode] = useState<SetupMode>("lan");
	/*
	const modeOptions = [
		{ value: "lan", label: t("mobile.lan") },
		{ value: "tailscale", label: t("mobile.tailscale") },
	] satisfies SettingsOption<SetupMode>[];
	*/

	useEffect(() => {
		return () => {
			if (copiedTimeoutRef.current) clearTimeout(copiedTimeoutRef.current);
		};
	}, []);

	const query = useQuery({
		queryKey: mobileStatusQueryKey,
		queryFn: fetchMobileStatus,
		enabled: active,
		// Only while the connector is coming up — see
		// mobileStatusRefetchInterval.
		refetchInterval: (q) => mobileStatusRefetchInterval(q.state.data),
	});

	const reportedOpen = useRef(false);
	const initialEnabled = query.data?.enabled;
	useEffect(() => {
		if (!active) {
			reportedOpen.current = false;
			setMode("lan");
			setOptimisticEnabled(null);
			return;
		}
		if (initialEnabled === undefined || reportedOpen.current) return;
		reportedOpen.current = true;
		void captureRendererEvent("ao.renderer.mobile_connect_opened", { bridge_enabled: initialEnabled });
	}, [active, initialEnabled]);

	const invalidate = () => {
		void queryClient.invalidateQueries({ queryKey: mobileStatusQueryKey });
	};

	const serverEnabled = query.data?.enabled;
	useEffect(() => {
		if (optimisticEnabled !== null && serverEnabled === optimisticEnabled) {
			setOptimisticEnabled(null);
		}
	}, [optimisticEnabled, serverEnabled]);

	const enable = useMutation({
		mutationFn: async () => {
			const { data, error } = await apiClient.POST("/api/v1/mobile/enable");
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: invalidate,
		onError: () => setOptimisticEnabled(null),
	});

	const startRemoteAccess = useMutation({
		mutationFn: async () => {
			const { data, error } = await apiClient.POST("/api/v1/mobile/remote-access");
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: invalidate,
	});

	const regenerate = useMutation({
		mutationFn: async () => {
			const { data, error } = await apiClient.POST("/api/v1/mobile/regenerate");
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: invalidate,
	});

	const disable = useMutation({
		mutationFn: async () => {
			const { error } = await apiClient.POST("/api/v1/mobile/disable");
			if (error) throw new Error(apiErrorMessage(error));
		},
		// Shutdown can finish before returning an error. Refresh the actual
		// server state while retaining the mutation error for the user.
		onSettled: invalidate,
		onError: () => setOptimisticEnabled(null),
	});

	const setSecure = useMutation({
		mutationFn: async (secureEnabled: boolean) => {
			const { data, error } = await apiClient.POST("/api/v1/mobile/secure-pairing", { body: { enabled: secureEnabled } });
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: invalidate,
	});

	// TLS turns itself on wherever Tailscale exists — it is not a switch, and it
	// is deliberately not tied to the connection picker. iOS refuses cleartext
	// to a 100.x address, so a Tailscale pairing without it works on Android and
	// fails on iPhone with nothing on either side to say why. Keying this to a
	// UI mode would mean hiding that picker also silently disables TLS, which is
	// the opposite of what a hidden control should do.
	//
	// secureAttempted keeps it to one attempt per panel session: a tailnet with
	// no certificates fails every time, and retrying on each status poll would
	// hammer the daemon. secureReasonText surfaces the failure; reopening the
	// panel is the retry.
	const secureAttempted = useRef(false);
	useEffect(() => {
		if (!active) {
			secureAttempted.current = false;
			return;
		}
		// No tailnet address on this machine means nothing for TLS to serve.
		if (!query.data?.tailscaleHost) return;
		const secure = query.data?.securePairing;
		if (!secure || secure.enabled || !secure.available) return;
		if (secureAttempted.current || setSecure.isPending) return;
		secureAttempted.current = true;
		setSecure.mutate(true);
	}, [active, query.data?.tailscaleHost, query.data?.securePairing, setSecure]);

	const status = query.data;
	const enabled = optimisticEnabled ?? status?.enabled ?? false;
	const secureActive = mode === "tailscale" && (status?.securePairing?.active ?? false);
	const activeHost = secureActive
		? status!.securePairing.host
		: mode === "tailscale"
			? (status?.tailscaleHost ?? "")
			: (status?.host ?? "");
	const activePort = secureActive ? status!.securePairing.port : (status?.port ?? 0);
	// No connector on this machine at all: cloudflared is absent and nothing
	// installs it. Pairing still works on the local network, so this is a
	// caveat to state rather than a failure to block on.
	const remoteAccessUnavailable = status?.tunnel ? status.tunnel.supported === false : false;
	const secureBlocked = mode === "tailscale" && (status?.securePairing?.enabled ?? false) && !secureActive;
	const busy =
		enable.isPending ||
		startRemoteAccess.isPending ||
		regenerate.isPending ||
		disable.isPending ||
		setSecure.isPending;

	const clearActionErrors = () => {
		enable.reset();
		startRemoteAccess.reset();
		regenerate.reset();
		disable.reset();
		setSecure.reset();
	};

	const copyPassword = async () => {
		if (!status?.password) return;
		try {
			await navigator.clipboard.writeText(status.password);
			setCopied(true);
			if (copiedTimeoutRef.current) clearTimeout(copiedTimeoutRef.current);
			copiedTimeoutRef.current = setTimeout(() => setCopied(false), 1500);
		} catch {
			// Clipboard can reject (permissions / non-secure context).
		}
	};

	const reportToggle = (next: boolean, outcome: "succeeded" | "failed") => {
		void captureRendererEvent("ao.renderer.mobile_bridge_toggled", { enabled: next, outcome });
	};

	const startBridge = () => {
		if (busy || enabled) return;
		setTrustConfirmationOpen(true);
	};

	const confirmStartBridge = () => {
		if (busy || enabled) return;
		setTrustConfirmationOpen(false);
		clearActionErrors();
		setOptimisticEnabled(true);
		enable.mutate(undefined, {
			onSuccess: () => reportToggle(true, "succeeded"),
			onError: () => reportToggle(true, "failed"),
		});
	};

	const actionError =
		(enable.error instanceof Error && enable.error.message) ||
		(startRemoteAccess.error instanceof Error && startRemoteAccess.error.message) ||
		(regenerate.error instanceof Error && regenerate.error.message) ||
		(disable.error instanceof Error && disable.error.message) ||
		(setSecure.error instanceof Error && setSecure.error.message) ||
		null;

	if (query.isLoading) {
		return <p className="py-4 text-center text-xs text-settings-muted">{t("mobile.checkingStatus")}</p>;
	}
	if (query.isError) {
		return (
			<p className="py-4 text-center text-xs text-error">
				{query.error instanceof Error ? query.error.message : t("mobile.loadFailed")}
			</p>
		);
	}
	if (!status) return null;

	const showRealQR =
		enabled &&
		activeHost &&
		!secureBlocked &&
		qrIsReady({ enabled, endpoints: status?.endpoints, tunnel: status?.tunnel });
	// v2 when the daemon advertises endpoints, v1 otherwise. Computed once so
	// the rendered QR and its data attribute cannot drift apart.
	const qrValue = showRealQR
		? qrValueFor({
				hostId: status?.hostId ?? "",
				host: activeHost,
				platform: "",
				password: status?.password ?? "",
				endpoints: status?.endpoints ?? [],
			})
		: undefined;
	if (qrValue) lastQrValueRef.current = qrValue;
	// Keep the preview in place while enable/remote access is still preparing.
	// Only reveal the generated panel once there is a real, scannable payload.
	const generatedQrVisible = Boolean(showRealQR && qrValue);
	const generatedQrValue = generatedQrVisible ? (qrValue ?? null) : lastQrValueRef.current;
	const shouldRenderGeneratedQr = generatedQrVisible || Boolean(lastQrValueRef.current);
	const secureReasonText = reasonMessage(status.securePairing?.reason ?? "", t);

	return (
		<div className="flex flex-col gap-4">
			<p className="text-xs leading-4 text-settings-muted">{t("mobile.description")}</p>
			{status.warning ? (
				<p
					className="rounded-md border border-warning/35 bg-warning/10 px-3 py-2 text-xs leading-5 text-warning"
					role="alert"
				>
					{status.warning}
				</p>
			) : null}

			<div className="flex flex-col gap-6 sm:flex-row sm:items-start">
				{/* Left: the walkthrough. */}
				<div className="flex min-w-0 flex-1 flex-col">
					{/* Connection picker commented out: one v2 code carries every
					    advertised endpoint — LAN, Tailscale and tunnel — and the phone
					    races them, so the mode never changed what a scan produces. TLS
					    is enabled off the tailnet address rather than this control (see
					    the effect above), so hiding it does not disable secure pairing.
					    What it does take off-screen is Tailscale's setup step and the
					    address/password rows for the tailnet host. */}
					{/*
					<div className="flex flex-nowrap items-center gap-2">
						<SettingsOptionMenu
							aria-label={t("mobile.connectionMethod")}
							value={mode}
							options={modeOptions}
							onChange={setMode}
							triggerClassName="w-44 justify-between"
							menuClassName="!w-44 !min-w-0"
							menuAlign="start"
						/>
					</div>
					*/}

					{/* One walkthrough per connection method. Steps are plain text with
					    trailing store links; address/password join the list once the QR
					    is generated. */}
					<ol className="settings-mobile-steps mt-4 !text-[13px] !leading-6 !text-[color-mix(in_oklch,var(--color-settings-label)_75%,var(--color-text-settings-muted))]">
						{/* Both stores are a public one-tap listing now, so the step names
						    both rather than making people pick a platform first — the
						    choice only ever selected which of these two links to show. */}
						<li>
							{t("mobile.getApp.step1")}{" "}
							{STORE_LINKS.map(({ key, Icon, url, labelKey, ariaKey, testId }, index) => (
								<Fragment key={key}>
									{index > 0 ? <span className="mx-1 text-settings-muted">{t("mobile.getApp.or")}</span> : null}
									<Tooltip>
										<TooltipTrigger asChild>
											<button
												type="button"
												className={STEP_LINK_CLASS}
												aria-label={t(ariaKey)}
												onClick={() => void aoBridge.app.openExternal(url)}
											>
												<Icon className="size-3.5 shrink-0" />
												{t(labelKey)}
												<ArrowUpRight className="size-3.5" aria-hidden="true" />
											</button>
										</TooltipTrigger>
										<TooltipContent side="bottom" className="p-2" data-testid={testId}>
											<div className="rounded-md bg-(--color-bg-settings-input) p-2">
												<StyledQRCode value={url} size={STORE_QR_SIZE} showLogo={false} className="block" />
											</div>
										</TooltipContent>
									</Tooltip>
								</Fragment>
							))}
						</li>
						{mode === "tailscale" ? <li>{t("mobile.tailscale.step1")}</li> : null}
						<li>{t("mobile.pairStep")}</li>
						{showRealQR && (
							<>
								<li data-testid="mobile-pairing-address">
									{t("mobile.address")}:{" "}
									<span className="tracking-settings-mono text-settings-label">{`${activeHost}:${activePort}`}</span>
								</li>
								<li>
									{t("mobile.password")}:{" "}
									<span className="tracking-settings-mono text-settings-label">{status.password}</span>
									<button
										type="button"
										aria-label={copied ? t("mobile.passwordCopied") : t("mobile.copyPassword")}
										className="ml-1.5 inline-flex size-5 items-center justify-center align-middle text-settings-muted transition-colors hover:text-settings-label"
										onClick={() => void copyPassword()}
									>
										{copied ? <Check className="size-3.5" aria-hidden="true" /> : <Copy className="size-3.5" aria-hidden="true" />}
									</button>
									<Tooltip>
										<TooltipTrigger asChild>
											<span className="inline-flex">
												<button
													type="button"
													aria-label={t("mobile.regenerate")}
													className="ml-0.5 inline-flex size-5 items-center justify-center align-middle text-settings-muted transition-colors hover:text-settings-label disabled:opacity-50"
													disabled={busy}
													onClick={() => {
														clearActionErrors();
														regenerate.mutate();
													}}
												>
													{regenerate.isPending ? (
														<Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
													) : (
														<RotateCcw className="size-3.5" aria-hidden="true" />
													)}
												</button>
											</span>
										</TooltipTrigger>
										<TooltipContent side="bottom">{t("mobile.regenerate")}</TooltipContent>
									</Tooltip>
								</li>
							</>
						)}
					</ol>

					{/* Tailscale extras: secure pairing (required on iPhone) + status. */}
					{status.tailscaleHost && secureReasonText && (
						<p
							data-testid="secure-pairing-reason"
							className="mt-4 text-caption leading-(--leading-settings-mobile-hint) text-warning"
						>
							{secureReasonText}
						</p>
					)}

					{remoteAccessUnavailable && (
						// Stated rather than hidden: pairing still works on this network,
						// so this is a limitation of what the code can reach, not an
						// error. Without it the QR looks entirely normal and the user
						// discovers the gap only by being away from home.
						<div className="mt-3">
							<p className="text-xs text-settings-muted" data-testid="mobile-remote-unavailable">
								{t(
									"mobile.remoteAccessUnavailable",
									"Works on this network only — cloudflared isn't installed, so this machine can't be reached from elsewhere.",
								)}
							</p>
							{/* Deliberately not enable(): that mints a fresh password, so
							    installing remote access would invalidate the phone the user
							    had already paired. This re-checks for the binary and starts
							    the connector, leaving the credential alone. */}
							<InstallCloudflared onInstalled={() => void startRemoteAccess.mutate()} />
						</div>
					)}
					{actionError && <p className="mt-3 text-xs text-error">{actionError}</p>}
				</div>

				{/* Right: dedicated pairing-QR panel — square, clipping, flush with
				    the content's right edge so bottom/right spacing match. */}
				<div className="flex w-full shrink-0 flex-col gap-3 self-start sm:w-80">
					<div className="relative">
						<div
							className={cn(
								"transition-opacity duration-200",
								generatedQrVisible ? "relative opacity-100" : "pointer-events-none absolute inset-0 opacity-0",
							)}
						>
							{shouldRenderGeneratedQr && (
								<PairingQr
									value={generatedQrValue}
									size={QR_CODE_SIZE}
									caption={t("mobile.tunnelStarting")}
								/>
							)}
						</div>
						<div
							className={cn(
								"transition-opacity duration-200",
								generatedQrVisible
									? "pointer-events-none absolute inset-0 opacity-0"
									: "relative pb-6 opacity-100",
							)}
						>
							<div className="relative aspect-square w-full overflow-hidden rounded-md">
								{enabled && !activeHost ? (
									<div className="flex size-full items-center justify-center bg-(--color-bg-settings-input) p-4">
										<p className="text-center text-caption leading-(--leading-settings-mobile-hint) text-settings-muted">
											{mode === "tailscale" ? t("mobile.noTailscaleHost") : t("mobile.noPairingHost")}
										</p>
									</div>
								) : (
									<>
										{/* The QR stays dark on white so Android can scan it. */}
										<div className="size-full bg-white opacity-60 blur-[6px]" aria-hidden="true">
											<StyledQRCode
												value={PLACEHOLDER_QR_VALUE}
												size={QR_CODE_SIZE}
												className="ao-qr-visual block size-full [&_svg]:size-full"
											/>
										</div>
										<div className="absolute inset-0 flex items-center justify-center">
											<Button
												type="button"
												variant="footer-primary"
												className="rounded-md shadow-lg"
												onClick={startBridge}
												disabled={busy || enabled}
											>
												{t("mobile.generate")}
											</Button>
										</div>
									</>
								)}
							</div>
						</div>
					</div>
					{enabled && (
						<Button
							type="button"
							variant="footer"
							className="w-full"
							disabled={busy}
							onClick={() => {
								clearActionErrors();
								setOptimisticEnabled(false);
								disable.mutate();
							}}
						>
							{t("mobile.disable", "Turn off mobile connection")}
						</Button>
					)}
					</div>
			</div>

			<Dialog open={trustConfirmationOpen} onOpenChange={setTrustConfirmationOpen}>
				<DialogContent className={settingsDialogContentClass} showCloseButton={false}>
					<DialogHeader className={settingsDialogHeaderClass}>
						<DialogTitle className="settings-dialog-title">{t("mobile.unencryptedConfirmTitle")}</DialogTitle>
						<DialogDescription>{status.warning}</DialogDescription>
					</DialogHeader>
					<DialogFooter className={settingsDialogFooterClass}>
						<Button type="button" variant="footer" onClick={() => setTrustConfirmationOpen(false)}>
							{t("confirm.cancel")}
						</Button>
						<Button type="button" variant="footer-primary" onClick={confirmStartBridge}>
							{t("mobile.trustNetworkAndGenerate")}
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
		</div>
	);
}

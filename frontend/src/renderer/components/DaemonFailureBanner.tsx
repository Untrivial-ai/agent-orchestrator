import { AlertTriangle, Clock3 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { DaemonStatus } from "../../shared/daemon-status";
import { isSlowDaemonStartupStatus } from "../../shared/daemon-startup-status";
import { daemonFailureHint, daemonFailureMessage, daemonFailureTitle } from "../lib/daemon-failure";
import { aoBridge } from "../lib/bridge";

export function DaemonFailureBanner({ status }: { status: DaemonStatus }) {
	if ((!status.code && !isSlowDaemonStartupStatus(status)) || status.state === "ready") return null;
	return <DaemonFailureContent status={status} />;
}

function DaemonFailureContent({ status }: { status: DaemonStatus }) {
	const { t } = useTranslation();
	const [detailsOpen, setDetailsOpen] = useState(false);
	const [copied, setCopied] = useState(false);
	const [restarting, setRestarting] = useState(false);
	const [restartError, setRestartError] = useState<string | null>(null);
	const copiedTimeout = useRef<ReturnType<typeof setTimeout> | null>(null);
	const slowStartup = isSlowDaemonStartupStatus(status);
	const details = status.details?.trim();
	const hint = slowStartup ? "" : daemonFailureHint(status, t);
	const title = slowStartup ? t("daemon.title.notReady") : daemonFailureTitle(status, t);
	const canRestart =
		!slowStartup &&
		(status.code === "not_ready" ||
			status.code === "spawn_failed" ||
			status.code === "exited" ||
			status.code === "identity_mismatch" ||
			status.code === "daemon_unreachable");
	useEffect(() => {
		setCopied(false);
		return () => {
			if (copiedTimeout.current !== null) clearTimeout(copiedTimeout.current);
		};
	}, [details]);
	const copyDetails = async () => {
		const lines = [
			title,
			status.code ? t("daemon.details.code", { code: status.code }) : "",
			t("daemon.details.message", { message: daemonFailureMessage(status, t) }),
			details ? `\n${t("daemon.details.heading", { details })}` : "",
		];
		await aoBridge.clipboard.writeText(lines.filter(Boolean).join("\n"));
		setCopied(true);
		if (copiedTimeout.current !== null) clearTimeout(copiedTimeout.current);
		copiedTimeout.current = setTimeout(() => {
			setCopied(false);
			copiedTimeout.current = null;
		}, 2_000);
	};
	const restartDaemon = async () => {
		setRestarting(true);
		setRestartError(null);
		try {
			const nextStatus = await aoBridge.daemon.restart();
			if (nextStatus.state === "error" || nextStatus.state === "stopped") {
				setRestartError(daemonFailureMessage(nextStatus, t));
			}
		} catch (error) {
			setRestartError(error instanceof Error ? error.message : t("daemon.restartFailed"));
		} finally {
			setRestarting(false);
		}
	};
	return (
		<section
			aria-live={slowStartup ? "polite" : "assertive"}
			className="pointer-events-auto fixed top-3 right-3 z-overlay flex w-daemon-failure-toast flex-col rounded-welcome-panel border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-modal)] px-3.5 py-3 text-xs shadow-[var(--shadow-import-modal)]"
			role={slowStartup ? "status" : "alert"}
		>
			<div className="flex items-start gap-3">
				{slowStartup ? (
					<Clock3
						className="mt-0.5 size-icon-base shrink-0 text-[var(--color-text-import-muted)]"
						aria-hidden="true"
					/>
				) : (
					<AlertTriangle className="mt-0.5 size-icon-base shrink-0 text-error" aria-hidden="true" />
				)}
				<div className="min-w-0 flex-1">
					<p className="font-medium text-(--color-text-import-title)">{title}</p>
					<p className="mt-0.5 wrap-break-word text-pretty text-[var(--color-text-import-muted)]">
						{daemonFailureMessage(status, t)}
					</p>
					{hint ? <p className="mt-1 text-[var(--color-text-import-muted)]">{hint}</p> : null}
					{canRestart ? (
						<button
							type="button"
							className="mt-2 inline-flex h-control-md items-center rounded-md bg-accent-strong px-3 font-semibold text-accent-foreground transition-[filter,opacity] hover:brightness-110 disabled:cursor-wait disabled:opacity-60"
							disabled={restarting}
							onClick={() => void restartDaemon()}
						>
							{restarting ? t("daemon.restarting") : t("daemon.restart")}
						</button>
					) : null}
					{restartError ? <p className="mt-2 text-error">{restartError}</p> : null}
					{details ? (
						<div className="mt-2 flex items-center gap-3">
							<button
								type="button"
								className="text-xs text-[var(--color-text-import-title)] underline-offset-2 hover:underline"
								onClick={() => setDetailsOpen((open) => !open)}
							>
								{detailsOpen ? t("daemon.hideDetails") : t("daemon.showDetails")}
							</button>
							<button
								type="button"
								className="text-xs text-[var(--color-text-import-title)] underline-offset-2 hover:underline"
								onClick={() => void copyDetails()}
							>
								{copied ? t("daemon.copied") : t("daemon.copyDetails")}
							</button>
						</div>
					) : null}
				</div>
				{status.code ? (
					<code className="shrink-0 rounded-md bg-(--color-bg-import-chip) px-1.5 py-0.5 font-mono text-micro text-[var(--color-text-import-muted)]">
						{status.code}
					</code>
				) : null}
			</div>
			{details && detailsOpen ? (
				<pre className="mt-2 max-h-daemon-failure-details-max w-full overflow-auto rounded-md border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] px-1.5 py-1 font-mono text-caption leading-relaxed text-[var(--color-text-import-muted)]">
					{details}
				</pre>
			) : null}
		</section>
	);
}

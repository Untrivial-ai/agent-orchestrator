import { useTranslation } from "react-i18next";
import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import {
	collectReportProblemDiagnostics,
	formatReportProblemDraft,
	reportProblemDestinationUrl,
	type ReportProblemDiagnostics,
	type ReportProblemOutput,
} from "../../lib/report-problem";
import { aoBridge } from "../../lib/bridge";
import { captureRendererEvent } from "../../lib/telemetry";
import { Button } from "../ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";

type DestinationIconProps = {
	className?: string;
};

function GithubIcon({ className }: DestinationIconProps) {
	return (
		<svg className={className} viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
			<path d="M12 .5C5.65.5.5 5.65.5 12c0 5.08 3.29 9.38 7.86 10.9.58.1.79-.25.79-.56v-2.15c-3.2.7-3.88-1.37-3.88-1.37-.52-1.34-1.28-1.7-1.28-1.7-1.05-.72.08-.7.08-.7 1.16.08 1.77 1.2 1.77 1.2 1.03 1.76 2.7 1.25 3.36.96.1-.75.4-1.25.73-1.54-2.56-.29-5.26-1.28-5.26-5.7 0-1.26.45-2.29 1.19-3.1-.12-.3-.52-1.47.11-3.05 0 0 .97-.31 3.18 1.18A10.96 10.96 0 0 1 12 5.99c.98 0 1.97.13 2.9.38 2.2-1.49 3.17-1.18 3.17-1.18.63 1.58.23 2.75.11 3.05.74.81 1.19 1.84 1.19 3.1 0 4.43-2.7 5.4-5.27 5.69.41.36.78 1.07.78 2.16v3.2c0 .31.21.67.8.55A11.51 11.51 0 0 0 23.5 12C23.5 5.65 18.35.5 12 .5Z" />
		</svg>
	);
}

function DiscordIcon({ className }: DestinationIconProps) {
	return (
		<svg className={className} viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
			<path d="M20.32 4.37A19.8 19.8 0 0 0 15.36 2.8a13.7 13.7 0 0 0-.64 1.32 18.4 18.4 0 0 0-5.44 0 13.7 13.7 0 0 0-.64-1.32 19.7 19.7 0 0 0-4.96 1.57C.54 9.04-.32 13.6.1 18.1a19.9 19.9 0 0 0 6.08 3.08c.49-.67.93-1.38 1.3-2.12-.72-.27-1.4-.6-2.05-.98.17-.12.34-.25.5-.38a14.2 14.2 0 0 0 12.14 0c.16.13.33.26.5.38-.65.39-1.34.72-2.06.99.38.74.81 1.45 1.31 2.12a19.9 19.9 0 0 0 6.08-3.08c.5-5.22-.86-9.74-3.58-13.73ZM8.02 15.33c-1.18 0-2.15-1.08-2.15-2.41 0-1.34.95-2.42 2.15-2.42 1.2 0 2.17 1.09 2.15 2.42 0 1.33-.96 2.41-2.15 2.41Zm7.96 0c-1.18 0-2.15-1.08-2.15-2.41 0-1.34.95-2.42 2.15-2.42 1.2 0 2.17 1.09 2.15 2.42 0 1.33-.95 2.41-2.15 2.41Z" />
		</svg>
	);
}

function EmailIcon({ className }: DestinationIconProps) {
	return (
		<svg className={className} viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
			<path d="M20 4H4c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V6c0-1.1-.9-2-2-2Zm0 4-8 5L4 8V6l8 5 8-5v2Z" />
		</svg>
	);
}

const DEFAULT_DIAGNOSTICS: ReportProblemDiagnostics = {
	appVersion: "unknown",
	buildMode: "unknown",
	daemonState: "unknown",
	generatedAt: "unknown",
	platform: "unknown",
	routeSurface: "unknown",
};

type DestinationOption = {
	value: ReportProblemOutput;
	label: string;
	action: string;
	icon: (props: DestinationIconProps) => ReactNode;
};

export function ReportProblemContent({ active }: { active: boolean }) {
	const { t } = useTranslation();
	const destinations: DestinationOption[] = [
		{ value: "github", label: t("report.github"), action: t("report.githubAction"), icon: GithubIcon },
		{ value: "discord", label: t("report.discord"), action: t("report.discordAction"), icon: DiscordIcon },
		{ value: "email", label: t("report.email"), action: t("report.emailAction"), icon: EmailIcon },
	];
	const titleId = useId();
	const detailsId = useId();
	const titleRef = useRef<HTMLInputElement>(null);
	const [summary, setSummary] = useState("");
	const [details, setDetails] = useState("");
	const [copiedOutput, setCopiedOutput] = useState<ReportProblemOutput | null>(null);
	const [copyError, setCopyError] = useState<string | null>(null);
	const [diagnostics, setDiagnostics] = useState<ReportProblemDiagnostics>(DEFAULT_DIAGNOSTICS);

	const copiedLabel = destinations.find((option) => option.value === copiedOutput)?.label;

	useEffect(() => {
		if (!active) {
			setSummary("");
			setDetails("");
			setCopiedOutput(null);
			setCopyError(null);
			return;
		}
		void captureRendererEvent("ao.renderer.support_opened");
		let cancelled = false;
		void collectReportProblemDiagnostics().then((nextDiagnostics) => {
			if (!cancelled) setDiagnostics(nextDiagnostics);
		});
		return () => {
			cancelled = true;
		};
	}, [active]);

	const input = { summary, details };
	const canSubmit = summary.trim().length > 0 && details.trim().length > 0;

	const clearStatus = () => {
		setCopiedOutput(null);
		setCopyError(null);
	};

	const copyDraft = async (output: ReportProblemOutput) => {
		if (!canSubmit) return;
		setCopyError(null);
		const draft = formatReportProblemDraft(input, diagnostics, output);
		try {
			await aoBridge.clipboard.writeText(draft);
			const destinationUrl = reportProblemDestinationUrl(input, diagnostics, output);
			if (destinationUrl) {
				await aoBridge.app.openExternal(destinationUrl);
			}
			setCopiedOutput(output);
			setSummary("");
			setDetails("");
			void captureRendererEvent("ao.renderer.support_submitted", { destination: output, outcome: "succeeded" });
		} catch (err) {
			setCopyError(err instanceof Error ? err.message : t("report.copyFailed"));
			setCopiedOutput(null);
			void captureRendererEvent("ao.renderer.support_submitted", { destination: output, outcome: "failed" });
		}
	};

	return (
		<div
			className="flex flex-col gap-4"
			onKeyDown={(event) => {
				if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
					event.preventDefault();
					void copyDraft("github");
				}
			}}
		>
			<div className="flex flex-col gap-1.5">
				<label className="settings-field-label" htmlFor={titleId}>
					{t("report.titleLabel")}
				</label>
				<input
					ref={titleRef}
					id={titleId}
					className="settings-field-control h-(--size-settings-action-height) rounded-md!"
					value={summary}
					onChange={(event) => {
						setSummary(event.target.value);
						clearStatus();
					}}
					placeholder={t("report.titlePlaceholder")}
				/>
			</div>

			<div className="flex flex-col gap-1.5">
				<label className="settings-field-label" htmlFor={detailsId}>
					{t("report.whatHappened")}
				</label>
				<textarea
					id={detailsId}
					className="settings-field-control min-h-(--size-textarea-min) resize-none overflow-y-auto py-2.5 rounded-md!"
					value={details}
					onChange={(event) => {
						setDetails(event.target.value);
						clearStatus();
					}}
					placeholder={t("report.detailsPlaceholder")}
				/>
			</div>

			<div role="group" aria-label={t("report.destination")} className="flex flex-wrap items-center justify-end gap-3">
				{copyError ? (
					<p role="alert" className="text-caption leading-4 text-error">
						{copyError}
					</p>
				) : null}
				{copiedLabel && !copyError ? (
					<p className="text-caption leading-4 text-success">{t("report.draftCopied", { label: copiedLabel })}</p>
				) : null}
				<div className="flex items-center gap-1.5">
					{destinations.map((option) => (
						<Tooltip key={option.value}>
							<TooltipTrigger asChild>
								<Button
									type="button"
									variant="footer"
									className="size-(--size-settings-action-height) shrink-0 rounded-md! p-0"
									disabled={!canSubmit}
									aria-label={option.action}
									onClick={() => {
										if (!canSubmit) return;
										void copyDraft(option.value);
									}}
								>
									<option.icon className="size-icon-sm" aria-hidden="true" />
								</Button>
							</TooltipTrigger>
							<TooltipContent>{option.action}</TooltipContent>
						</Tooltip>
					))}
				</div>
			</div>
		</div>
	);
}

import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import aoLogo from "../../../assets/ao-logo.svg";
import { aoBridge } from "../lib/bridge";
import { useSystemRequirementsGate } from "../hooks/useSystemRequirementsGate";
import { InstallDependencyDialog } from "./InstallDependencyDialog";

const STARTUP_PHRASE_KEYS = [
	"startup.startingServices",
	"startup.connectingDaemon",
	"startup.loadingWorkspaces",
	"startup.preparingBoard",
] as const;

// Shown instead of the normal phrases when the current boot is a post-update
// relaunch, so the swap reads as "the app is updating" rather than "the app is
// slow to connect".
const UPDATE_PHRASE_KEYS = [
	"startup.updatingApp",
	"startup.restartingApp",
	"startup.startingServices",
	"startup.preparingBoard",
] as const;

const PHRASE_INTERVAL_MS = 2_200;

export function DaemonStartupLoader() {
	const { t } = useTranslation();
	const [phraseIndex, setPhraseIndex] = useState(0);
	const [postUpdate, setPostUpdate] = useState(false);
	const {
		query: requirementsQuery,
		requirements,
		requirementsBlocked,
	} = useSystemRequirementsGate();

	useEffect(() => {
		let active = true;
		// Defensive: the loader must render even when the updates bridge is absent
		// (web fallback, or a test/preload stub without this namespace). A missing
		// signal simply means "not a post-update relaunch".
		const isPostUpdateRelaunch = aoBridge.updates?.isPostUpdateRelaunch;
		if (typeof isPostUpdateRelaunch !== "function") {
			return;
		}
		void isPostUpdateRelaunch().then(
			(value) => {
				if (active) setPostUpdate(value);
			},
			() => undefined,
		);
		return () => {
			active = false;
		};
	}, []);

	const phraseKeys = postUpdate ? UPDATE_PHRASE_KEYS : STARTUP_PHRASE_KEYS;

	useEffect(() => {
		const timer = window.setInterval(() => {
			setPhraseIndex((current) => (current + 1) % phraseKeys.length);
		}, PHRASE_INTERVAL_MS);
		return () => window.clearInterval(timer);
	}, [phraseKeys.length]);

	const phrase = t(phraseKeys[phraseIndex % phraseKeys.length]);

	return (
		<div
			aria-busy="true"
			aria-label={t("startup.aria", { brand: "Agent Orchestrator" })}
			aria-live="polite"
			className="ao-startup-screen flex h-full w-full items-center justify-center bg-background text-foreground"
			data-testid="daemon-startup-loader"
			role="status"
		>
			<div className="ao-startup-content flex -translate-y-[3vh] flex-col items-center text-center">
				<div className="grid h-28 w-32 place-items-center" aria-hidden="true">
					<img className="ao-startup-logo h-22 w-25 object-contain" src={aoLogo} alt="" />
				</div>
				<p className="mt-5 text-base font-semibold tracking-tight text-foreground">Agent Orchestrator</p>
				<p className="mt-2 min-h-5 text-md-sm text-muted-foreground">
					<span aria-hidden="true" className={phraseIndex === 0 ? undefined : "ao-startup-status"} key={phraseIndex}>
						{phrase}
					</span>
				</p>
				<div className="ao-startup-dots mt-3 flex h-4 items-center gap-1.5" aria-hidden="true">
					<span />
					<span />
					<span />
				</div>
			</div>
			{requirementsBlocked ? (
				<InstallDependencyDialog requirements={requirements} onRefetchRequirements={() => requirementsQuery.refetch()} />
			) : null}
		</div>
	);
}

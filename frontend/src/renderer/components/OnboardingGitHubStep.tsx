import { Check, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { useGitHubSetup } from "../hooks/useGitHubSetup";
import { AuthTerminalPanel } from "./AuthTerminalPanel";
import { SetupList, SetupRow } from "./SetupList";
import { GitHubMarkIcon } from "./icons";

/** Step: GitHub. One page handles both halves of the prerequisite, because
 *  installing the CLI and signing in are one intention. The checks run from
 *  the first step's mount, so this page opens already knowing the state. */
export function OnboardingGitHubStep({ setup }: { setup: ReturnType<typeof useGitHubSetup> }) {
	const { t } = useTranslation();
	const installFailed = setup.job?.status === "failed" || setup.job?.status === "unsupported" || setup.job?.status === "interrupted";
	const installDetail = setup.installError ?? (installFailed ? setup.job?.error : undefined);

	if (!setup.gh) {
		return (
			<p className="px-1 text-caption text-muted-foreground" role="status">
				{t("onboarding.checkingAvailability")}
			</p>
		);
	}

	if (setup.authSatisfied) {
		return (
			<SetupList className="max-w-[420px]">
				<SetupRow
					icon={<GitHubMarkIcon aria-hidden="true" />}
					label={t("startup.githubConnected")}
					description={t("onboarding.githubConnectedDetail")}
					trailing={<Check aria-hidden="true" className="size-3.5 text-status-ready" />}
					disabled
				/>
			</SetupList>
		);
	}

	return (
		<div className="flex w-full max-w-[420px] flex-col text-left">
			<SetupList>
			{setup.cliMissing ? (
				<SetupRow
					icon={<GitHubMarkIcon aria-hidden="true" />}
					label={installFailed ? t("onboarding.tryAgain") : t("startup.installGh")}
					description={t("onboarding.githubInstallDetail")}
					disabled={setup.installing}
					onClick={() => void setup.install()}
					trailing={setup.installing ? <Loader2 aria-hidden="true" className="size-3.5 animate-spin motion-reduce:animate-none" /> : undefined}
				/>
			) : (
				<SetupRow
					icon={<GitHubMarkIcon aria-hidden="true" />}
					label={setup.loginEnded ? t("startup.githubLoginTryAgain") : t("startup.githubLogin")}
					description={t("onboarding.githubSignInDetail")}
					disabled={setup.signInPending || setup.loginRunning}
					onClick={setup.signIn}
					trailing={setup.signInPending || setup.loginRunning ? <Loader2 aria-hidden="true" className="size-3.5 animate-spin motion-reduce:animate-none" /> : undefined}
				/>
			)}
			</SetupList>
			{installDetail ? (
				<p className="px-4 text-caption leading-snug text-warning" role="status">
					{installDetail}
				</p>
			) : null}
			{setup.signInError ? (
				<p className="px-4 text-caption leading-snug text-destructive" role="alert">
					{setup.signInError}
				</p>
			) : null}
			{setup.workflow ? (
				<AuthTerminalPanel
					workflow={setup.workflow}
					onClose={setup.closeSignIn}
					onRetry={setup.signIn}
					onTerminalState={setup.handleTerminalState}
					closeLabel={t("common.close")}
					terminalHeightClass="h-[200px]"
					testId="github-auth-terminal"
				/>
			) : null}
		</div>
	);
}

import { GitFork, GitPullRequest, LoaderCircle, MessagesSquare } from "lucide-react";
import { useEffect, useState } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { useTranslation } from "react-i18next";
import { useCloseShellTerminal } from "../hooks/useShellTerminals";
import { useGitHubAuthRequirement, useGitHubAuthTerminal, useStartGitHubAuthTerminal, useSystemRequirementsGate } from "../hooks/useSystemRequirementsGate";
import { aoBridge } from "../lib/bridge";
import { useUiStore } from "../stores/ui-store";
import { TopbarButton } from "./TopbarButton";
import { CopyButton } from "./chat/CopyButton";

const GITHUB_CLI_INSTALL_URL = "https://cli.github.com/";
// gh's device-verification page. Opened only on an explicit user click;
// AO never launches a browser by itself.
const GITHUB_DEVICE_URL = "https://github.com/login/device";

/** A quiet Home-page entry point for optional GitHub setup. The login PTY is
 * deliberately kept behind an explicit dialog action rather than starting as
 * a side effect of visiting Home. */
export function GitHubOnboardingNotice() {
	const { t } = useTranslation();
	const gate = useSystemRequirementsGate();
	const authQuery = useGitHubAuthRequirement(false);
	const auth = authQuery.data;
	const gh = (gate.requirements ?? []).find((requirement) => requirement.id === "gh");
	const [open, setOpen] = useState(false);

	if (!auth || auth.satisfied) return null;

	return (
		<>
			<div className="flex w-full justify-center px-3" data-testid="github-onboarding-notice" role="alert">
				<p className="text-[12px] leading-5 text-[var(--color-text-import-muted)]">
					{t("startup.githubNotConnected", { defaultValue: "GitHub isn't connected." })}{" "}
					<button
						aria-label={t("startup.connectGitHub", { defaultValue: "Connect GitHub" })}
						className="font-medium text-[var(--color-text-import-title)] underline decoration-[var(--color-text-import-muted)] underline-offset-2 hover:decoration-current focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
						onClick={() => setOpen(true)}
						type="button"
					>
						{t("startup.connectGitHub", { defaultValue: "Connect GitHub" })}
					</button>
				</p>
			</div>
			<GitHubAuthDialog open={open} onOpenChange={setOpen} cliMissing={gh?.satisfied === false} />
		</>
	);
}

function GitHubAuthDialog({ open, onOpenChange, cliMissing }: { open: boolean; onOpenChange: (open: boolean) => void; cliMissing: boolean }) {
	const { t } = useTranslation();
	const startLogin = useStartGitHubAuthTerminal();
	const terminalQuery = useGitHubAuthTerminal();
	const terminal = terminalQuery.data;
	const deviceCode = terminalQuery.deviceCode;
	const { mutate: closeTerminal } = useCloseShellTerminal();
	const authQuery = useGitHubAuthRequirement(Boolean(terminal));
	const showGlobalToast = useUiStore((state) => state.showGlobalToast);

	const [loginStarted, setLoginStarted] = useState(false);
	useEffect(() => {
		if (!open || !authQuery.data?.satisfied || !terminal || !loginStarted) return;
		showGlobalToast(t("startup.githubConnected"));
		closeTerminal(terminal.handleId, {
			onSuccess: () => {
				terminalQuery.clear();
				onOpenChange(false);
			},
		});
	}, [authQuery.data?.satisfied, closeTerminal, loginStarted, onOpenChange, open, showGlobalToast, t, terminal, terminalQuery.clear]);

	const start = () => {
		if (cliMissing) {
			void aoBridge.app.openExternal(GITHUB_CLI_INSTALL_URL);
			return;
		}
		setLoginStarted(true);
		startLogin.mutate();
	};

	const close = () => {
		if (!terminal) {
			onOpenChange(false);
			return;
		}
		closeTerminal(terminal.handleId, { onSuccess: () => { terminalQuery.clear(); onOpenChange(false); } });
	};

	const openDevicePage = () => {
		void aoBridge.app.openExternal(GITHUB_DEVICE_URL);
	};

	return (
		<Dialog.Root open={open} onOpenChange={(nextOpen) => nextOpen ? onOpenChange(true) : close()}>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay data-[state=open]:animate-overlay-in data-[state=closed]:animate-overlay-out" />
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay w-[min(620px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 overflow-hidden rounded-lg border border-border bg-popover p-0 text-popover-foreground shadow-xl data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none">
					<Dialog.Title className="settings-dialog-title px-4 pt-3">{t("startup.githubSetupTitle")}</Dialog.Title>
					<div className="space-y-4 px-4 py-4">
						<Dialog.Description className="text-sm text-muted-foreground">
							{t("startup.githubLoginDialogDescription", { defaultValue: "Connect GitHub to let AO create remotes and work with pull requests." })}
						</Dialog.Description>
						{cliMissing ? (
							<div>
								<p className="text-sm leading-6 text-muted-foreground">{t("startup.githubSetupMissingCli")}</p>
								<p className="mt-2 flex items-center gap-2 text-sm leading-6 text-muted-foreground">
									<code className="font-mono text-[13px] text-foreground">{t("startup.githubInstallCommand")}</code>
									<CopyButton compact text={t("startup.githubInstallCommand")} label={t("startup.copyCommand")} />
								</p>
							</div>
						) : (
							<>
								<ul className="space-y-2.5">
									{[
										{ icon: GitPullRequest, title: t("startup.githubBenefitPrs"), detail: t("startup.githubBenefitPrsDetail") },
										{ icon: GitFork, title: t("startup.githubBenefitRemotes"), detail: t("startup.githubBenefitRemotesDetail") },
										{ icon: MessagesSquare, title: t("startup.githubBenefitIssues"), detail: t("startup.githubBenefitIssuesDetail") },
									].map(({ icon: Icon, title, detail }) => (
										<li key={title} className="flex items-start gap-2.5">
											<Icon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
											<div>
												<p className="text-sm font-medium leading-5 text-foreground">{title}</p>
												<p className="text-[13px] leading-5 text-muted-foreground">{detail}</p>
											</div>
										</li>
									))}
								</ul>
							</>
						)}
						{startLogin.isError ? <p className="text-xs text-destructive" role="alert">{startLogin.error.message}</p> : null}
						{terminal && deviceCode ? (
							<div>
								<p className="flex items-center gap-2">
									<code className="font-mono text-xl tracking-[0.2em] text-foreground">{deviceCode}</code>
									<CopyButton compact text={deviceCode} label={t("startup.copyCommand")} />
								</p>
								<p className="mt-1 text-[13px] leading-5 text-muted-foreground">{t("startup.githubDeviceCodeHint")}</p>
							</div>
						) : null}
						{terminal && !deviceCode ? (
							<p className="flex items-start gap-2 text-sm leading-6 text-muted-foreground">
								<LoaderCircle aria-hidden="true" className="mt-1 size-3.5 shrink-0 animate-spin" />
								{t("startup.githubLoginRunning")}
							</p>
						) : null}
					</div>
					<div className="flex justify-end gap-2 px-4 pb-4">
						{terminal && deviceCode ? <TopbarButton onClick={openDevicePage} variant="primary">{t("startup.githubOpenBrowser")}</TopbarButton> : null}
						{!terminal ? <TopbarButton disabled={startLogin.isPending} onClick={start} variant="primary">{startLogin.isPending ? <LoaderCircle aria-hidden="true" className="size-icon-sm animate-spin" /> : null}{cliMissing ? t("startup.openGithubCliDocs") : t("startup.githubLogin")}</TopbarButton> : null}
					</div>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}

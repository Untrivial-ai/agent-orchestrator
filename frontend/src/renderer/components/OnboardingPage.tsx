import { useNavigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowUp, CircleDashed, Loader2 } from "lucide-react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import aoLogo from "../../../assets/ao-logo.svg";
import visibilityBackground from "../../landing/public/optimized/feature.webp";
import { FleetBoardDemo, type FleetBoardAssets } from "./onboarding/previews/fleet-board-demo";
import { OnboardingProjectSetup } from "./OnboardingProjectSetup";
import { OnboardingGitHubStep } from "./OnboardingGitHubStep";
import { AuthTerminalPanel } from "./AuthTerminalPanel";
import { RequiredAgentField } from "./CreateProjectAgentSheet";
import { agentsQueryKey, refreshAgentsIfStale, useAgentsQuery } from "../hooks/useAgentsQuery";
import { useHarnessSetup } from "../hooks/useHarnessSetup";
import { useDaemonStatus } from "../hooks/useDaemonStatus";
import { useGitHubSetup } from "../hooks/useGitHubSetup";
import { markOnboardingComplete } from "../lib/onboarding-finish";
import { AGENT_OPTIONS, agentLabel } from "../lib/agent-options";
import type { MessageKey } from "../i18n";
import { buildRankedAgentOptions, DEFAULT_AGENT_PRIORITY_RANK, isReadyAgent, type AgentInfo, unknownAgentReadiness } from "../lib/agent-select-options";
import { cn } from "../lib/utils";
import { useUiStore } from "../stores/ui-store";
import type { PreparedProjectInput } from "./CreateProjectFlow";
import { applyDocumentTheme, applyDocumentThemeStyle, readStoredThemeStyle, resolveTheme } from "../lib/theme";
import claudeCodeLogo from "../assets/agents/claude-code.svg";
import codexLogo from "../assets/agents/codex.svg";
import cursorLogo from "../assets/agents/cursor.svg";
import opencodeLogo from "../assets/agents/opencode.svg";

type Step = "welcome" | "github" | "project" | "agent-setup" | "agents" | "guide";

type StepDetails = {
	title: MessageKey;
	subtitle: MessageKey;
	nextLabel: MessageKey;
};

type AgentSetupEntry = "required" | "optional";

// Cloud onboarding is intentionally disabled until the product has an explicit
// entitlement signal. Keep the standalone cloud step available for that later
// decision, but do not route any onboarding user through it today.
const STEPS: Step[] = ["welcome", "github", "project", "agent-setup", "agents", "guide"];

/** Project setup is one stage of the flow: pick a project, conditionally set
 * up a harness, then choose the agent used for both roles. */
const STAGES: Step[][] = [["welcome"], ["github"], ["project", "agent-setup", "agents"], ["guide"]];

const STEP_DETAILS: Record<Step, StepDetails> = {
	welcome: {
		title: "onboarding.step.welcome.title",
		subtitle: "onboarding.step.welcome.subtitle",
		nextLabel: "onboarding.step.welcome.next",
	},
	github: {
		title: "onboarding.step.github.title",
		subtitle: "onboarding.step.github.subtitle",
		nextLabel: "onboarding.step.github.next",
	},
	project: {
		title: "onboarding.step.project.title",
		subtitle: "onboarding.step.project.subtitle",
		nextLabel: "onboarding.step.project.next",
	},
	"agent-setup": {
		title: "onboarding.step.agentSetup.title",
		subtitle: "onboarding.step.agentSetup.subtitle",
		nextLabel: "onboarding.step.agentSetup.next",
	},
	agents: {
		title: "onboarding.step.agents.title",
		subtitle: "onboarding.step.agents.subtitle",
		nextLabel: "onboarding.step.agents.next",
	},
	guide: {
		title: "onboarding.step.guide.title",
		subtitle: "onboarding.step.guide.subtitle",
		nextLabel: "onboarding.step.guide.next",
	},
};

const OPTIONAL_AGENT_SETUP_DETAILS: StepDetails = {
	title: "onboarding.step.agentSetupOptional.title",
	subtitle: "onboarding.step.agentSetupOptional.subtitle",
	nextLabel: "onboarding.step.agentSetupOptional.next",
};

const ALL_IMAGES = [visibilityBackground];
// Availability is helpful context, never a gate for setup. A daemon that is
// still booting (or a stalled local probe) must not leave every choice looking
// perpetually busy.
const AGENT_CHECK_INDICATOR_TIMEOUT_MS = 2_500;
const AGENT_ICON_URLS = import.meta.glob<string>("../assets/agents/*.{png,svg}", {
	eager: true,
	import: "default",
	query: "?url",
});
const LANDING_PREVIEW_ASSETS: FleetBoardAssets = {
	"/app-icons/coverage-claude-code.svg": claudeCodeLogo,
	"/app-icons/coverage-codex.svg": codexLogo,
	"/app-icons/cursor.svg": cursorLogo,
	"/app-icons/opencode.svg": opencodeLogo,
};
function agentIcon(agentId: string) {
	const suffixes = [`/${agentId}.svg`, `/${agentId}.png`];
	return Object.entries(AGENT_ICON_URLS).find(([path]) => suffixes.some((suffix) => path.endsWith(suffix)))?.[1];
}

export function OnboardingPage() {
	const navigate = useNavigate();
	const { t } = useTranslation();
	const requestOnboardingFinish = useUiStore((state) => state.requestOnboardingFinish);
	const onboardingFinishRequest = useUiStore((state) => state.onboardingFinishRequest);
	const onboardingFinishError = useUiStore((state) => state.onboardingFinishError);
	const clearOnboardingFinishError = useUiStore((state) => state.clearOnboardingFinishError);
	const agentsQuery = useAgentsQuery();
	// The shell normally publishes the daemon port that API calls need. Loading
	// straight onto onboarding (a reload mid-setup, or a deep link) skips that,
	// which left every probe on this screen answering 503.
	useDaemonStatus();
	const harnessSetup = useHarnessSetup();
	const queryClient = useQueryClient();
	const [step, setStep] = useState<Step>("welcome");
	const [agentSetupEntry, setAgentSetupEntry] = useState<AgentSetupEntry>("required");
	// The GitHub checks run from the first step's mount, so that page opens
	// already knowing its state; polling only runs while the page is showing.
	const githubSetup = useGitHubSetup({ poll: step === "github" });
	const [selectedAgent, setSelectedAgent] = useState<string | null>(null);
	const [hoveredAgent, setHoveredAgent] = useState<string | null>(null);
	const [preparedProject, setPreparedProject] = useState<PreparedProjectInput | null>(null);
	const [agentCheckIndicatorTimedOut, setAgentCheckIndicatorTimedOut] = useState(false);
	const stepIndex = STEPS.indexOf(step);
	const stageIndex = Math.max(
		0,
		STAGES.findIndex((stage) => stage.includes(step)),
	);
	const details = step === "agent-setup" && agentSetupEntry === "optional" ? OPTIONAL_AGENT_SETUP_DETAILS : STEP_DETAILS[step];
	const agentCatalog = agentsQuery.data;
	const agentOptions = useMemo<AgentInfo[]>(() => {
		const fallbackAgents = AGENT_OPTIONS.map((id) => unknownAgentReadiness(id, agentLabel(id)));
		const isCatalogKnown = Boolean(agentCatalog);
		const installedIds = new Set(agentCatalog?.installed.map((agent) => agent.id));
		const authorizedIds = new Set(agentCatalog?.authorized.map((agent) => agent.id));
		const catalogAgents = (agentCatalog?.supported ?? []).map((agent) => ({
			...unknownAgentReadiness(agent.id, agent.label),
			installation: {
				state: installedIds.has(agent.id) ? ("installed" as const) : ("not_installed" as const),
				freshness: "fresh" as const,
			},
			authentication: {
				state: authorizedIds.has(agent.id) ? ("authorized" as const) : ("unknown" as const),
				freshness: "fresh" as const,
			},
			lastUsedAt: agent.lastUsedAt,
			usageCount: agent.usageCount ?? 0,
		}));
		return buildRankedAgentOptions({
			agents: isCatalogKnown ? catalogAgents : undefined,
			priorityRank: DEFAULT_AGENT_PRIORITY_RANK,
			fallbackAgents,
		});
	}, [agentCatalog]);
	const onboardingAgents = useMemo<OnboardingAgent[]>(() => {
		const isCatalogKnown = Boolean(agentCatalog);
		const isCheckingCatalog = !isCatalogKnown && (agentsQuery.isLoading || agentsQuery.isFetching) && !agentCheckIndicatorTimedOut;
		const installedIds = new Set(agentCatalog?.installed.map((agent) => agent.id));
		return agentOptions.map((agent) => ({
			id: agent.id,
			installed: !isCatalogKnown || installedIds.has(agent.id),
			name: agent.label,
			indicator: isCheckingCatalog ? "checking" : isCatalogKnown && installedIds.has(agent.id) && !isReadyAgent(agent) ? "auth" : "none",
		}));
	}, [agentCatalog, agentCheckIndicatorTimedOut, agentOptions, agentsQuery.isFetching, agentsQuery.isLoading]);

	// The conditional setup step is a real prerequisite: the normal role picker
	// only appears after at least one installed agent is ready to run.
	const hasReadyAgent = Boolean(agentCatalog && agentOptions.some(isReadyAgent));

	useEffect(() => {
		if (agentCatalog || (!agentsQuery.isLoading && !agentsQuery.isFetching)) {
			setAgentCheckIndicatorTimedOut(false);
			return;
		}
		const timeout = window.setTimeout(() => setAgentCheckIndicatorTimedOut(true), AGENT_CHECK_INDICATOR_TIMEOUT_MS);
		return () => window.clearTimeout(timeout);
	}, [agentCatalog, agentsQuery.isFetching, agentsQuery.isLoading]);

	useEffect(() => {
		for (const src of ALL_IMAGES) {
			const image = new Image();
			image.src = src;
		}
	}, []);

	useEffect(() => {
		// Match the task composer: probe when this agent-picking surface opens so
		// a newly installed or authenticated harness is reflected immediately.
		// The result goes into the cache rather than a local snapshot, so the
		// invalidation an install triggers still lands: a snapshot would win over
		// every later fetch and leave the new agent unselectable until reload.
		void refreshAgentsIfStale().then((catalog) => {
			if (catalog) queryClient.setQueryData(agentsQueryKey, catalog);
		});
	}, [queryClient]);

	// A failed handoff comes back here with its request still in the store.
	// Restore the choices that produced it so the next attempt does not make the
	// user redo the project and agent steps.
	const restoredFailureRef = useRef<number | null>(null);
	useEffect(() => {
		if (!onboardingFinishError || !onboardingFinishRequest) return;
		if (onboardingFinishError.nonce !== onboardingFinishRequest.nonce) return;
		if (restoredFailureRef.current === onboardingFinishError.nonce) return;
		restoredFailureRef.current = onboardingFinishError.nonce;
		setPreparedProject({
			asWorkspace: onboardingFinishRequest.asWorkspace,
			clonePreparationId: onboardingFinishRequest.clonePreparationId,
			defaultBranch: onboardingFinishRequest.defaultBranch,
			path: onboardingFinishRequest.path,
		});
		setSelectedAgent(onboardingFinishRequest.orchestratorAgent || onboardingFinishRequest.workerAgent);
		setStep("guide");
	}, [onboardingFinishError, onboardingFinishRequest]);

	const goToStep = useCallback((index: number) => {
		if (index >= 0 && index < STEPS.length) setStep(STEPS[index]);
	}, []);

	const next = useCallback(() => {
		if (step === "guide") {
			if (!preparedProject || !selectedAgent) return;
			// Completion is recorded by the handoff itself, once the project is
			// actually registered. Marking it here stranded anyone whose project
			// failed to create on an empty board with onboarding already spent.
			requestOnboardingFinish({
				...preparedProject,
				orchestratorAgent: selectedAgent,
				workerAgent: selectedAgent,
			});
			void navigate({ to: "/" });
			return;
		}
		if (step === "agent-setup") {
			if (agentSetupEntry === "optional" || hasReadyAgent) setStep("agents");
			return;
		}
		goToStep(stepIndex + 1);
	}, [agentSetupEntry, goToStep, hasReadyAgent, navigate, preparedProject, requestOnboardingFinish, selectedAgent, step, stepIndex]);

	const back = useCallback(() => {
		// Required setup belongs between project selection and the picker. Optional
		// setup is launched from the picker, so both exit paths return to their source.
		if (step === "agent-setup") {
			setStep(agentSetupEntry === "optional" ? "agents" : "project");
			return;
		}
		if (step === "agents") {
			setStep("project");
			return;
		}
		goToStep(stepIndex - 1);
	}, [agentSetupEntry, goToStep, step, stepIndex]);

	// A cloud project is created by the flow that owns it, so onboarding just
	// records completion and hands off to the app.
	const handleCloudProjectCreated = useCallback(() => {
		markOnboardingComplete();
		void navigate({ to: "/" });
	}, [navigate]);
	const handleInstallAgent = useCallback(
		(agentId: string) => {
			void harnessSetup.startInstall(agentId);
		},
		[harnessSetup],
	);
	const handleSignInAgent = useCallback(
		(agentId: string) => {
			void harnessSetup.startAuth(agentId);
		},
		[harnessSetup],
	);
	const handleInstallAnotherAgent = useCallback(() => {
		setAgentSetupEntry("optional");
		setStep("agent-setup");
	}, []);

	const isProjectStep = step === "project";
	const isAgentSetupStep = step === "agent-setup";
	const isAgentStep = step === "agents";
	const isGuideStep = step === "guide";
	const isSetupStep = step === "github";
	const isListStep = isProjectStep || isSetupStep;
	// The single product overview opens on the product mark; setup pages want the space.
	const isFeatureStep = step === "welcome";

	useLayoutEffect(() => {
		// Onboarding is a branded first-run surface: keep it dark and on the
		// default Orchestrate palette even when the app was previously themed.
		applyDocumentTheme("dark");
		applyDocumentThemeStyle("orchestrate");

		return () => {
			applyDocumentTheme(resolveTheme());
			applyDocumentThemeStyle(readStoredThemeStyle());
		};
	}, []);

	return (
		<main className="relative h-[100dvh] min-h-[640px] w-screen overflow-hidden bg-background text-foreground">
			<div className="fixed inset-x-0 top-0 z-titlebar h-8" style={{ WebkitAppRegion: "drag" } as React.CSSProperties} />

			<div className="mx-auto grid h-full w-full max-w-[1240px] grid-rows-[80px_minmax(0,1fr)_104px] px-8 max-[1040px]:px-6">
				<header className="flex items-end justify-between pb-3" aria-label={t("onboarding.progressLabel")}>
					{isFeatureStep ? (
						// The mark moves above the headline on these two pages. The spacer
						// keeps the progress bars where they were.
						<span aria-hidden="true" className="h-6 w-7" />
					) : (
						<img src={aoLogo} alt={t("onboarding.logoAlt")} className="h-6 w-7 object-contain" />
					)}
					<div
						className="flex gap-1.5"
						aria-label={t("onboarding.stepOf", {
							current: stageIndex + 1,
							total: STAGES.length,
						})}
					>
						{STAGES.map((stage, index) => (
							<span
								key={stage[0]}
								className={cn(
									"h-1 rounded-full transition-[width,background-color] duration-normal ease-out motion-reduce:transition-none",
									// The step you are on keeps full width; the rest shrink, and the
									// width animates so moving through the flow reads as movement.
									index === stageIndex ? "w-4" : "w-2",
									index <= stageIndex ? "bg-foreground/70" : "bg-foreground/15",
								)}
							/>
						))}
					</div>
				</header>

				<div
					className={cn(
						"min-h-0",
						isListStep
							? "flex items-center justify-center overflow-y-auto"
							: isAgentSetupStep
								? "flex items-center justify-center overflow-y-auto"
								: isAgentStep || isGuideStep
								? "grid grid-cols-[minmax(360px,1.1fr)_minmax(300px,0.9fr)] items-center gap-10 max-[1040px]:grid-cols-[minmax(340px,1.15fr)_minmax(240px,0.85fr)] max-[1040px]:gap-6"
								: "grid grid-cols-[minmax(280px,0.72fr)_minmax(520px,1.35fr)] items-center gap-14 max-[1040px]:grid-cols-[minmax(270px,0.75fr)_minmax(0,1.25fr)] max-[1040px]:gap-8",
					)}
				>
					<section
						key={step}
						className={cn(
							"grid h-[360px] grid-rows-[180px_180px]",
							(isAgentSetupStep || isAgentStep || isGuideStep) && "h-[480px] grid-rows-[210px_minmax(0,1fr)]",
							isAgentSetupStep && "w-full max-w-[680px]",
							// Setup steps size to content so a missing prerequisite adds a
							// block instead of overflowing the fixed wizard height.
							isListStep && "h-auto min-h-[280px] w-full max-w-[680px] grid-rows-[auto_auto] text-center",
						)}
						aria-labelledby={`onboarding-title-${step}`}
					>
						<div className={cn("flex flex-col justify-end pb-7", (isAgentSetupStep || isAgentStep || isGuideStep) && "justify-center pb-5", isAgentSetupStep && "items-center text-center")}>
							{isFeatureStep ? <img src={aoLogo} alt="" aria-hidden="true" className="mb-5 h-30 w-35 object-contain" /> : null}
							<h1
								id={`onboarding-title-${step}`}
								className={cn(
									isAgentSetupStep || isAgentStep || isGuideStep ? "max-w-[500px]" : "max-w-[410px]",
									"text-[clamp(2rem,3.2vw,3.15rem)] font-normal leading-[1.02] tracking-[-0.045em] text-balance",
									isListStep && "mx-auto",
									isProjectStep && "max-w-none whitespace-nowrap",
								)}
							>
								{t(details.title)}
							</h1>
							<p className={cn("mt-5 max-w-[350px] text-[15px] leading-6 text-muted-foreground text-pretty", (isAgentSetupStep || isAgentStep || isGuideStep) && "max-w-[430px]", isListStep && "mx-auto")}>
								{t(details.subtitle)}
							</p>
						</div>
						<div className={cn("min-h-0 pt-2", isListStep && "flex justify-center")}>
							{isAgentSetupStep && (
								<div className="mx-auto w-full max-w-[440px] space-y-4 text-left">
									{harnessSetup.authWorkflow ? (
										<AuthTerminalPanel
											workflow={harnessSetup.authWorkflow}
											onClose={() => void harnessSetup.closeAuth()}
											onRetry={() => void harnessSetup.retryAuth()}
											onTerminalState={harnessSetup.handleTerminalState}
											closeLabel={t("common.close")}
											terminalContextMenu="compact"
										/>
									) : (
										<AgentRolePicker
											label={t(details.title)}
											agents={onboardingAgents}
											harnessSetup={harnessSetup}
											hovered={hoveredAgent}
											onHover={setHoveredAgent}
											onInstall={handleInstallAgent}
											onSignIn={handleSignInAgent}
										/>
									)}
								</div>
							)}
							{isAgentStep && (
								<div className="w-full max-w-[440px] text-left">
									<RequiredAgentField
										id="onboardingAgent"
										label={t("newTask.agent")}
										placeholder={t("newTask.selectAgent")}
										variant="onboarding"
										agents={agentCatalog ? agentOptions : undefined}
										value={selectedAgent ?? ""}
										onChange={setSelectedAgent}
										managementActionLabel={t("onboarding.installAnotherAgent")}
										onManagementAction={handleInstallAnotherAgent}
									/>
								</div>
							)}
							{step === "github" && <OnboardingGitHubStep setup={githubSetup} />}
							{step === "project" && (
								<div className="flex w-full flex-col items-center gap-4">
									<OnboardingProjectSetup
										cloudAvailable={false}
										onPrepared={(project) => {
											setPreparedProject(project);
											if (project) {
												setAgentSetupEntry("required");
												setStep(hasReadyAgent ? "agents" : "agent-setup");
											}
										}}
										onCloudProjectCreated={handleCloudProjectCreated}
									/>
								</div>
							)}
							{isGuideStep &&
								(onboardingFinishError ? (
									<div className="w-full max-w-[500px] text-left" role="alert">
										<p className="text-sm font-medium text-foreground">{t("onboarding.finishFailedTitle")}</p>
										<p className="mt-1 text-caption leading-snug text-muted-foreground">{onboardingFinishError.message || t("onboarding.finishFailedBody")}</p>
										<div className="mt-3 flex flex-wrap items-center gap-2">
											<button
												type="button"
												onClick={() => {
													clearOnboardingFinishError();
													void navigate({ to: "/" });
												}}
												className="inline-flex h-9 items-center rounded-md bg-primary px-3 text-sm font-medium text-primary-foreground hover:opacity-85 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
											>
												{t("onboarding.finishRetry")}
											</button>
											<button
												type="button"
												onClick={clearOnboardingFinishError}
												className="inline-flex h-9 items-center rounded-md border border-border px-3 text-sm text-muted-foreground hover:bg-interactive-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
											>
												{t("onboarding.finishChangeSetup")}
											</button>
										</div>
									</div>
								) : (
									<div className="w-full max-w-[500px]">
										<OnboardingGuide />
									</div>
								))}
						</div>
					</section>

					{isAgentStep || isGuideStep ? (
						harnessSetup.authWorkflow ? (
							// The login terminal takes the illustration's place rather than
							// floating over the step, so the primary action stays reachable
							// while a first-run user completes sign-in.
							<div className="w-full max-w-[460px] justify-self-center">
								<AuthTerminalPanel
									workflow={harnessSetup.authWorkflow}
									onClose={() => void harnessSetup.closeAuth()}
									onRetry={() => void harnessSetup.retryAuth()}
									onTerminalState={harnessSetup.handleTerminalState}
									closeLabel={t("common.close")}
									terminalContextMenu="compact"
								/>
							</div>
						) : (
							<AgentTopologyPreview orchestratorAgent={selectedAgent} workerAgent={selectedAgent} />
						)
					) : step === "welcome" ? (
						<PreviewStage />
					) : null}
				</div>

				<footer className="flex items-center justify-between">
					{step === "github" && githubSetup.workflow ? (
						<span aria-hidden="true" />
					) : (
						<button
							type="button"
							onClick={back}
							disabled={stepIndex === 0}
							className="h-10 px-1 text-sm text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-0"
						>
							{t("onboarding.back")}
						</button>
					)}
					<button
						type="button"
						onClick={next}
						disabled={
							(step === "project" && !preparedProject) ||
							(isAgentSetupStep && agentSetupEntry === "required" && !hasReadyAgent) ||
							(isAgentStep && !selectedAgent) ||
							// GitHub is the one prerequisite the flow will not let you skip:
							// agents cannot open pull requests or read issues without it.
							(step === "github" && !githubSetup.authSatisfied)
						}
						className="relative inline-flex w-auto items-center justify-center whitespace-nowrap rounded-xl bg-primary px-4 py-2 text-sm font-semibold! text-primary-foreground transition-[scale,opacity] duration-150 ease-out after:absolute after:inset-x-0 after:-inset-y-0.5 after:content-[''] hover:opacity-85 active:not-disabled:scale-[0.96] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-30"
					>
						{t(details.nextLabel)}
					</button>
				</footer>
			</div>
		</main>
	);
}

type OnboardingAgent = {
	id: string;
	installed: boolean;
	name: string;
	indicator: "auth" | "checking" | "none";
};

function OnboardingGuide() {
	const { t } = useTranslation();
	return (
		<div className="w-full max-w-[500px] text-left">
			<p className="mb-2 text-xs font-medium text-muted-foreground">{t("onboarding.guidePromptLabel")}</p>
			{/* A still of the chat composer rather than a quoted block: the prompt
			    sits where the user will type it, on the composer's own surface and
			    radius, so it is recognisable when they reach the real one. Inert
			    on purpose — it is a picture of the control, not the control. */}
			<div aria-hidden="true" className="cursor-chat-composer flex flex-col gap-2.5 border px-3 pb-2.5 pt-3">
				<p className="text-[13px] leading-5 text-foreground">{t("onboarding.guidePrompt")}</p>
				<div className="flex items-center justify-end">
					<span className="grid size-7 shrink-0 place-items-center rounded-full bg-foreground text-background">
						<ArrowUp className="size-3.5" aria-hidden="true" />
					</span>
				</div>
			</div>
			<p className="mt-4 text-xs leading-5 text-muted-foreground">{t("onboarding.guideExplainer")}</p>
		</div>
	);
}

export function AgentRolePicker({
	label,
	agents,
	harnessSetup,
	hovered,
	onHover,
	onInstall,
	onSignIn,
}: {
	label: string;
	agents: OnboardingAgent[];
	harnessSetup: HarnessSetup;
	hovered: string | null;
	onHover: (id: string | null) => void;
	onInstall: (id: string) => void;
	onSignIn: (id: string) => void;
}) {
	const { t } = useTranslation();
	const installed = agents.filter((agent) => agent.installed);
	const available = agents.filter((agent) => !agent.installed);
	const [showTopFade, setShowTopFade] = useState(false);
	return (
		<section aria-label={label}>
			<div className="relative">
				<div
					className="max-h-[240px] space-y-0.5 overflow-y-auto rounded-lg pb-5 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
					onScroll={(event) => setShowTopFade(event.currentTarget.scrollTop > 0)}
				>
					{installed.map((agent) => (
						<div key={agent.id} className="flex flex-col">
							<div className="flex items-center gap-2">
								<div
									onMouseEnter={() => onHover(agent.id)}
									onMouseLeave={() => onHover(null)}
									className={cn("flex h-10 min-w-0 flex-1 items-center gap-3 rounded-md px-2 text-left text-sm", hovered === agent.id && "bg-foreground/[0.07] text-foreground")}
								>
									<img src={agentIcon(agent.id)} alt="" className="size-5 shrink-0 object-contain" />
									<span className="min-w-0 flex-1 truncate">{agent.name}</span>
									{agent.indicator === "none" ? <CheckIcon className="text-status-ready" /> : <AgentAvailabilityIndicator indicator={agent.indicator} />}
								</div>
								{agent.indicator === "auth" && !harnessSetup.authWorkflow ? (
									harnessSetup.authPlanFor(agent.id) && harnessSetup.authPlanFor(agent.id)?.action !== "instructions" ? (
										<button
											type="button"
											onClick={() => onSignIn(agent.id)}
											disabled={!harnessSetup.authPlanFor(agent.id)?.available}
											title={harnessSetup.authPlanFor(agent.id)?.reason}
											aria-label={t("onboarding.signInToAgent", {
												agent: agent.name,
											})}
											className="shrink-0 rounded-md bg-foreground/[0.035] px-2 py-1 text-xs text-muted-foreground hover:bg-interactive-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-40"
										>
											{harnessSetup.authPlanFor(agent.id)?.action === "setup" ? t("settings.harness.setup") : t("onboarding.signIn")}
										</button>
									) : (
										<span className="shrink-0 text-[11px] text-muted-foreground">{t("onboarding.setupRequired")}</span>
									)
								) : null}
							</div>
							{harnessSetup.actionErrors[agent.id] ? (
								<p className="px-2 pb-1 text-[11px] leading-4 text-warning" role="status">
									{harnessSetup.actionErrors[agent.id]}
								</p>
							) : null}
						</div>
					))}
					{available.map((agent) => (
						<InstallableAgentRow key={agent.id} agent={agent} hovered={hovered === agent.id} onHover={onHover} onInstall={onInstall} setup={harnessSetup} />
					))}
				</div>
				{showTopFade ? <div className="pointer-events-none absolute inset-x-0 top-0 h-10 bg-gradient-to-b from-background via-background/80 to-transparent" aria-hidden="true" /> : null}
			</div>
		</section>
	);
}

function AgentAvailabilityIndicator({ indicator }: { indicator: OnboardingAgent["indicator"] }) {
	const { t } = useTranslation();
	if (indicator === "checking") return <Loader2 aria-label={t("onboarding.checkingAvailability")} className="size-3.5 shrink-0 animate-spin text-muted-foreground motion-reduce:animate-none" />;
	if (indicator === "auth") return <CircleDashed aria-label={t("onboarding.notSignedIn")} className="size-3.5 shrink-0 text-muted-foreground" />;
	return null;
}

type HarnessSetup = ReturnType<typeof useHarnessSetup>;

/** A harness that is not on this machine yet. The row reports the real install
 *  job (running, failed, retrying) instead of sending the user to Settings. */
function InstallableAgentRow({
	agent,
	hovered,
	onHover,
	onInstall,
	setup,
}: {
	agent: OnboardingAgent;
	hovered: boolean;
	onHover: (id: string | null) => void;
	onInstall: (id: string) => void;
	setup: HarnessSetup;
}) {
	const { t } = useTranslation();
	const job = setup.jobFor(agent.id);
	const installing = setup.isInstalling(agent.id);
	const failed = job?.status === "failed" || job?.status === "unsupported" || job?.status === "interrupted";
	const error = setup.actionErrors[agent.id] ?? (failed ? job?.error : undefined);
	return (
		<div className="flex flex-col">
			<button
				type="button"
				onClick={() => onInstall(agent.id)}
				onMouseEnter={() => onHover(agent.id)}
				onMouseLeave={() => onHover(null)}
				disabled={installing}
				aria-label={
					installing
						? t("onboarding.installingAgent", { agent: agent.name })
						: failed
							? t("onboarding.tryAgainToInstallAgent", { agent: agent.name })
							: t("onboarding.installAgent", { agent: agent.name })
				}
				className={cn(
					"flex h-10 w-full items-center gap-3 rounded-md bg-transparent px-2 text-left text-sm text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-progress disabled:opacity-80",
					hovered && "bg-foreground/[0.07] text-foreground",
				)}
			>
				<img src={agentIcon(agent.id)} alt="" className="size-5 shrink-0 object-contain opacity-65" />
				<span className="min-w-0 flex-1 truncate">{agent.name}</span>
				{installing ? (
					<span className="flex shrink-0 items-center gap-1.5 text-[10px] font-medium">
						<Loader2 aria-hidden="true" className="size-3.5 animate-spin motion-reduce:animate-none" />
						{t("onboarding.installing")}
					</span>
				) : (
					<span className="shrink-0 rounded-sm bg-foreground px-2 py-1 text-[10px] font-medium text-background">{failed ? t("onboarding.tryAgain") : t("onboarding.install")}</span>
				)}
			</button>
			{error ? (
				<p className="px-2 pb-1 text-[11px] leading-4 text-warning" role="status">
					{error}
				</p>
			) : null}
		</div>
	);
}

function AgentTopologyPreview({ orchestratorAgent, workerAgent }: { orchestratorAgent: string | null; workerAgent: string | null }) {
	const { t } = useTranslation();
	const reduceMotion = useReducedMotion();
	const signals = useTopologySignals(Boolean(reduceMotion));
	// The illustration mirrors the two role selections from the combined step.
	const orchestratorPreview = orchestratorAgent;
	const workerPreview = workerAgent;
	const orchestratorSrc = (orchestratorPreview && agentIcon(orchestratorPreview)) || aoLogo;
	const workerSrc = workerPreview ? agentIcon(workerPreview) : undefined;
	const orchestratorName = t("onboarding.topologyOrchestrator");
	const workerName = t("onboarding.topologyWorkers");

	return (
		<div className="relative mx-auto aspect-[560/430] w-full max-w-[400px] overflow-hidden rounded-2xl" aria-label={t("onboarding.hierarchyIllustration")}>
			<svg viewBox="0 0 560 430" className="absolute inset-0 size-full text-foreground/20" fill="none" aria-hidden="true">
				<path d="M280 160v46M120 206h320M120 206v30M280 206v30M440 206v30" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" />
				{signals.map((signal) => (
					<TopologySignal key={signal.id} signal={signal} />
				))}
			</svg>

			<div className="absolute left-1/2 top-[9.3%] -translate-x-1/2">
				<AgentIdentity iconClassName="size-12" name={orchestratorName} src={orchestratorSrc} textClassName="text-sm" />
			</div>

			{["left", "center", "right"].map((position) => (
				<div
					key={position}
					className={cn(
						"absolute top-[58.6%] flex -translate-x-1/2 flex-col items-center gap-2",
						position === "left" && "left-[21.43%]",
						position === "center" && "left-1/2 -translate-x-1/2",
						position === "right" && "left-[78.57%]",
					)}
				>
					<AgentIdentity iconClassName="size-8" name={workerName} src={workerSrc} textClassName="text-xs" />
				</div>
			))}
		</div>
	);
}

type TopologySignal = {
	id: number;
	workerIndex: 0 | 1 | 2;
	direction: "to-orchestrator" | "to-worker";
};

const WORKER_X = [120, 280, 440] as const;

function useTopologySignals(reduceMotion: boolean) {
	const [signals, setSignals] = useState<TopologySignal[]>([]);
	const nextId = useRef(0);

	useEffect(() => {
		if (reduceMotion) return;
		let signalTimer = 0;
		let scheduleTimer = 0;
		let active = true;

		const scheduleSignal = () => {
			scheduleTimer = window.setTimeout(
				() => {
					if (!active) return;
					const signal: TopologySignal = {
						id: nextId.current++,
						workerIndex: Math.floor(Math.random() * WORKER_X.length) as 0 | 1 | 2,
						direction: Math.random() > 0.5 ? "to-orchestrator" : "to-worker",
					};
					setSignals([signal]);
					signalTimer = window.setTimeout(() => setSignals([]), 920);
					scheduleSignal();
				},
				1800 + Math.random() * 2200,
			);
		};

		scheduleSignal();
		return () => {
			active = false;
			window.clearTimeout(signalTimer);
			window.clearTimeout(scheduleTimer);
		};
	}, [reduceMotion]);

	return signals;
}

function TopologySignal({ signal }: { signal: TopologySignal }) {
	const workerX = WORKER_X[signal.workerIndex];
	const isUpstream = signal.direction === "to-orchestrator";
	const x = isUpstream ? [workerX, workerX, 280, 280] : [280, 280, workerX, workerX];
	const y = isUpstream ? [236, 206, 206, 160] : [160, 206, 206, 236];

	return (
		<motion.circle
			cx="0"
			cy="0"
			r="2.25"
			fill="currentColor"
			initial={{ opacity: 0, x: x[0], y: y[0] }}
			animate={{ opacity: [0, 0.85, 0.85, 0], x, y }}
			transition={{
				duration: 0.86,
				ease: "easeInOut",
				times: [0, 0.12, 0.82, 1],
			}}
		/>
	);
}

function AgentIdentity({ iconClassName, name, src, textClassName }: { iconClassName: string; name: string; src?: string; textClassName: string }) {
	const reduceMotion = useReducedMotion();
	return (
		<div className="flex flex-col items-center gap-2">
			<AnimatePresence initial={false} mode="wait">
				<motion.div
					key={src ?? "generic-worker"}
					initial={{ opacity: 0, filter: "blur(4px)" }}
					animate={{ opacity: 1, filter: "blur(0px)" }}
					exit={{ opacity: 0, filter: "blur(4px)" }}
					transition={{ duration: reduceMotion ? 0 : 0.14, ease: "easeOut" }}
				>
					{src ? <img src={src} alt="" className={cn(iconClassName, "object-contain")} /> : <GenericWorkerIcon />}
				</motion.div>
			</AnimatePresence>
			<span className={cn("whitespace-nowrap text-muted-foreground", textClassName)}>{name}</span>
		</div>
	);
}

function GenericWorkerIcon() {
	return (
		<svg viewBox="0 0 32 32" className="size-7 text-muted-foreground" fill="none" aria-hidden="true">
			<rect x="6" y="8" width="20" height="17" rx="4" stroke="currentColor" strokeWidth="1.5" />
			<path d="M16 4v4M11 15h.01M21 15h.01M11 20c2.7 2 7.3 2 10 0" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
		</svg>
	);
}

function PreviewStage() {
	return (
		<div className="relative mx-auto aspect-[4/3] w-full max-w-[720px] overflow-hidden">
			<img src={visibilityBackground} alt="" className="pointer-events-none absolute inset-0 size-full select-none object-cover" />
			<div className="absolute inset-0 bg-background/35" />
			<div className="relative z-10 flex size-full items-center justify-center p-6">
				<FleetBoardDemo assets={LANDING_PREVIEW_ASSETS} />
			</div>
		</div>
	);
}

function CheckIcon({ className }: { className?: string }) {
	return (
		<svg viewBox="0 0 16 16" className={cn("size-3 shrink-0", className)} fill="none" stroke="currentColor" strokeWidth="1.6" aria-hidden="true">
			<path d="m4 8 2.5 2.5L12 5" strokeLinecap="round" strokeLinejoin="round" />
		</svg>
	);
}

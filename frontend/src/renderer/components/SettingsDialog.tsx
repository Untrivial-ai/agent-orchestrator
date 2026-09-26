import { Bot, Check, Loader2, MonitorCog, TriangleAlert, X, type LucideIcon } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import * as Dialog from "@radix-ui/react-dialog";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useCloudGate } from "../hooks/useCloudGate";
import { ensureCodexAccounts } from "../hooks/useCodexAccountsQuery";
import { writeCodexAccounts } from "../hooks/codex-accounts-state";
import { GlobalSettingsForm } from "./GlobalSettingsForm";
import { ProjectSettingsForm, type ProjectSettingsSaveState, type ProjectSettingsSection } from "./ProjectSettingsForm";
import { DialogHeader, settingsDialogBodyClass, settingsDialogContentClass, settingsDialogHeaderClass } from "./ui/dialog";
import { type GlobalSettingsSection, type SettingsModal, useUiStore } from "../stores/ui-store";
import { cn } from "../lib/utils";
import { globalSettingsItem, visibleGlobalSettings } from "./settings/settingsCatalog";

function initialProjectSaveState(): ProjectSettingsSaveState {
	return { phase: "idle" };
}

export function SettingsDialog() {
	const settingsModal = useUiStore((state) => state.settingsModal);
	const projectSettings = settingsModal?.scope === "project" ? settingsModal : settingsModal?.returnTo;
	return (
		<>
			{projectSettings && <SettingsDialogLayer key={projectSettings.projectId} settingsModal={projectSettings} />}
			{settingsModal?.scope === "global" && <SettingsDialogLayer key="global" settingsModal={settingsModal} />}
		</>
	);
}

function SettingsDialogLayer({ settingsModal }: { settingsModal: SettingsModal }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const closeSettings = useUiStore((state) => state.closeSettings);
	// Reads the daemon settings the dialog tree already queries; no extra fetch.
	const { cloudEnabled } = useCloudGate();

	const displaySettings = settingsModal;
	// The selected page includes several store/query subscribers. Mount it one
	// frame after the lightweight dialog chrome so the opening interaction can
	// paint first.
	const deferSettingsBody = settingsModal.scope === "global";
	const [bodySettings, setBodySettings] = useState<SettingsModal | null>(() =>
		deferSettingsBody ? null : settingsModal,
	);
	useEffect(() => {
		if (settingsModal === null) return;
		if (!deferSettingsBody) {
			setBodySettings(settingsModal);
			return;
		}
		const frame = requestAnimationFrame(() => setBodySettings(settingsModal));
		return () => cancelAnimationFrame(frame);
	}, [deferSettingsBody, settingsModal]);
	const isBodyReady = bodySettings === displaySettings;

	const globalSections = visibleGlobalSettings({ cloudEnabled });

	const projectSections: Array<{
		id: ProjectSettingsSection;
		label: string;
		icon: LucideIcon;
	}> = [
		{ id: "general", label: t("settings.project.general"), icon: MonitorCog },
		{ id: "agents", label: t("settings.project.agents"), icon: Bot },
	];

	const isProjectSettings = displaySettings?.scope === "project";
	const [activeSection, setActiveSection] = useState<GlobalSettingsSection>("general");
	const [focusAgentId, setFocusAgentId] = useState<string>();
	const [activeProjectSection, setActiveProjectSection] = useState<ProjectSettingsSection>("general");
	const [projectSaveState, setProjectSaveState] = useState<ProjectSettingsSaveState>(initialProjectSaveState);
	const closeWhenSavedRef = useRef(false);
	const globalSettingsWasOpen = useRef(false);

	const activeLabel = isProjectSettings
		? (projectSections.find((s) => s.id === activeProjectSection)?.label ?? t("settings.project.general"))
		: globalSettingsItem(activeSection, { cloudEnabled }).label(t);

	const closeSettingsDialog = () => {
		if (isProjectSettings && (projectSaveState.phase === "pending" || projectSaveState.phase === "saving")) {
			closeWhenSavedRef.current = true;
			return;
		}
		closeSettings();
	};
	useEffect(() => {
		if (!closeWhenSavedRef.current || (projectSaveState.phase !== "saved" && projectSaveState.phase !== "idle")) return;
		closeWhenSavedRef.current = false;
		closeSettings();
	}, [closeSettings, projectSaveState.phase]);
	const closeButtonRef = useRef<HTMLButtonElement>(null);
	const contentRef = useRef<HTMLDivElement>(null);
	const returnFocusRef = useRef(document.activeElement as HTMLElement | null);
	const returnDialogRef = useRef(returnFocusRef.current?.closest<HTMLElement>('[role="dialog"]') ?? null);
	const hasAgentFocusTarget = settingsModal.scope === "global" && Boolean(settingsModal.focusAgentId);
	useEffect(() => {
		if (hasAgentFocusTarget) return;
		// The modal contains focus immediately. Move visible focus after the
		// first paint because focus() forces style resolution.
		let focusTimer = 0;
		const focusFrame = requestAnimationFrame(() => {
			focusTimer = window.setTimeout(() => closeButtonRef.current?.focus({ preventScroll: true }), 0);
		});
		return () => {
			cancelAnimationFrame(focusFrame);
			window.clearTimeout(focusTimer);
		};
	}, [hasAgentFocusTarget]);

	useEffect(() => {
		if (settingsModal?.scope === "global") {
			setActiveSection(globalSettingsItem(settingsModal.section ?? "general", { cloudEnabled }).id);
		}
		if (settingsModal?.scope === "project") {
			setActiveProjectSection("general");
			setProjectSaveState(initialProjectSaveState());
		}
	}, [cloudEnabled, settingsModal]);

	useEffect(() => {
		setFocusAgentId(settingsModal?.scope === "global" ? settingsModal.focusAgentId : undefined);
	}, [settingsModal]);

	useEffect(() => {
		const globalSettingsOpen = settingsModal?.scope === "global";
		if (!globalSettingsOpen) {
			globalSettingsWasOpen.current = false;
			return;
		}
		if (globalSettingsWasOpen.current) return;
		globalSettingsWasOpen.current = true;
		// Warm account management as soon as global Settings opens, regardless of
		// which page is selected. By the time the user visits Accounts, external
		// login/logout changes and saved-account observations are already current.
		void ensureCodexAccounts([], {
			includeUsage: true,
			forceAuthentication: true,
			forceDeviceReconciliation: true,
		})
			.then((next) => writeCodexAccounts(queryClient, next, "replace"))
			.catch(() => undefined);
	}, [queryClient, settingsModal?.scope]);

	return (
		<Dialog.Root
			open
			onOpenChange={(open) => {
				if (!open) closeSettingsDialog();
			}}
		>
			<Dialog.Portal>
				<Dialog.Overlay
					className="dialog-overlay animate-overlay-in motion-reduce:animate-none"
					data-testid="settings-dialog-overlay"
					onWheel={(event) => event.preventDefault()}
				/>
				<Dialog.Content
					aria-modal="true"
					className={cn(
						settingsDialogContentClass,
						"fixed left-1/2 top-1/2 h-(--size-settings-dialog-height) w-(--size-settings-dialog-wide) max-h-none -translate-x-1/2 -translate-y-1/2 origin-center overflow-hidden p-0 animate-modal-in motion-reduce:animate-none sm:rounded-lg",
						isProjectSettings && "h-[min(40rem,calc(100vh-3rem))]",
					)}
					onOpenAutoFocus={(event) => event.preventDefault()}
					onEscapeKeyDown={(event) => {
						if (contentRef.current?.contains(event.target as Node)) return;
						const target = event.target instanceof Element ? event.target : null;
						const activeElement = document.activeElement instanceof Element ? document.activeElement : null;
						const nestedPopup = [target, activeElement].some((element) => element?.closest('[role="menu"], [role="listbox"], [data-radix-popper-content-wrapper]'));
						if (nestedPopup) event.preventDefault();
					}}
					onCloseAutoFocus={(event) => {
						event.preventDefault();
						// Successful recovery removes its CTA. The originating dialog
						// remains mounted and can receive focus when that happens.
						const target = returnFocusRef.current?.isConnected ? returnFocusRef.current : returnDialogRef.current;
						if (target?.isConnected) target.focus({ preventScroll: true });
					}}
					ref={contentRef}
				>
					<div className="flex h-full min-h-0">
						<aside className="flex w-48 shrink-0 flex-col border-r border-(--color-border-settings-dialog-header) bg-card">
							<p className="px-3 pb-1 pt-3 text-2xs font-semibold tracking-wider text-muted-foreground/60">{t("settings.title")}</p>
							<nav aria-label={t("settings.navSectionsAria")} className="flex flex-col gap-0.5 p-2 pt-0">
								{isProjectSettings
									? projectSections.map(({ id, label, icon }) => (
											<SettingsNavItem active={activeProjectSection === id} icon={icon} key={id} label={label} onClick={() => setActiveProjectSection(id)} />
										))
									: globalSections.map(({ id, label, icon }) => (
											<SettingsNavItem
												active={activeSection === id}
												icon={icon}
												key={id}
												label={label(t)}
												onClick={() => {
													setActiveSection(id);
													setFocusAgentId(undefined);
												}}
											/>
										))}
							</nav>
							{isProjectSettings && projectSaveState.phase !== "idle" && (
								<div className="mt-auto border-t border-(--color-border-settings-dialog-header) px-4 py-3 text-xs" role="status" aria-live="polite">
									{projectSaveState.phase === "failed" ? (
										<div className="space-y-2 text-error">
											<p className="flex items-start gap-2" role="alert"><TriangleAlert className="size-4 shrink-0" aria-hidden="true" />{projectSaveState.error ?? t("settings.project.saveFailed")}</p>
											<button className="text-settings-label underline underline-offset-2" onClick={() => (document.getElementById("project-settings-form") as HTMLFormElement | null)?.requestSubmit()} type="button">{t("settings.models.retry")}</button>
										</div>
									) : projectSaveState.phase === "saved" ? (
										<p className="flex items-center gap-2 text-settings-muted">
											<Check className="size-4 shrink-0" aria-hidden="true" />
											{t("settings.project.saved")}
										</p>
									) : (
										<p className="flex items-center gap-2 text-settings-muted">
											<Loader2 className="size-4 shrink-0 animate-spin" aria-hidden="true" />
											<span className="sr-only">{t("settings.project.saving")}</span>
										</p>
									)}
								</div>
							)}
						</aside>

						{/* Main area — same bg as the app page */}
						<div className="flex min-w-0 flex-1 flex-col bg-card">
							<DialogHeader className={cn(settingsDialogHeaderClass, "flex h-auto shrink-0 flex-row items-center justify-between border-b-0 pb-3")}>
								<Dialog.Title className="text-2xl font-bold text-foreground">{activeLabel}</Dialog.Title>
								<Dialog.Description className="sr-only">
									{isProjectSettings
										? t("settings.project.dialogDescription")
										: t("settings.dialogDescription", {
												section: activeLabel.toLowerCase(),
											})}
								</Dialog.Description>
								<button
									aria-label={t("settings.close")}
									className="settings-close-button"
									onClick={closeSettingsDialog}
									ref={closeButtonRef}
									type="button"
								>
									<X aria-hidden="true" className="size-4" />
								</button>
							</DialogHeader>
							<div aria-busy={!isBodyReady} className={cn(settingsDialogBodyClass, "settings-dialog-body flex-1 px-(--size-modal-padding) pt-0")}>
								{isBodyReady ? (
									displaySettings?.scope === "project" ? (
										<ProjectSettingsForm projectId={displaySettings.projectId} section={activeProjectSection} onSaveState={setProjectSaveState} />
									) : (
										<GlobalSettingsForm cloudEnabled={cloudEnabled} focusAgentId={focusAgentId} section={activeSection} />
									)
								) : (
									<div aria-hidden="true" className="h-full" data-testid="settings-dialog-body-pending" />
								)}
							</div>
						</div>
					</div>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}

function SettingsNavItem({ active, disabled, icon: Icon, label, onClick }: { active: boolean; disabled?: boolean; icon: LucideIcon; label: string; onClick: () => void }) {
	return (
		<button
			aria-current={active ? "page" : undefined}
			className={cn(
				"flex h-9 w-full items-center gap-2 rounded-md px-2.5 text-left text-sm font-medium transition-[background-color,color,transform] duration-fast ease-out active:scale-press focus:outline-none focus-visible:outline-none focus-visible:ring-0 disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:bg-transparent disabled:hover:text-muted-foreground",
				active ? "bg-interactive-active text-foreground" : "text-muted-foreground hover:bg-interactive-hover hover:text-foreground",
			)}
			disabled={disabled}
			onClick={onClick}
			type="button"
		>
			<Icon aria-hidden="true" className="size-4 shrink-0" />
			{label}
		</button>
	);
}

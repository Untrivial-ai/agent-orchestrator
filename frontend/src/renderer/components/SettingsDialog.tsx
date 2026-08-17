import { Bot, CircleHelp, GitBranch, Inbox, MonitorCog, RefreshCw, Settings2, TriangleAlert, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { GlobalSettingsForm, type GlobalSettingsSection } from "./GlobalSettingsForm";
import {
	ProjectSettingsForm,
	type ProjectSettingsSaveState,
	type ProjectSettingsSection,
} from "./ProjectSettingsForm";
import { ConnectMobileModal } from "./ConnectMobileModal";
import { KeyboardShortcutsSettingsDialog } from "./settings/KeyboardShortcutsSettingsDialog";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";
import { type SettingsModal, useUiStore } from "../stores/ui-store";
import { cn } from "../lib/utils";
import { Button } from "./ui/button";

function initialProjectSaveState(): ProjectSettingsSaveState {
	return {
		isPending: false,
		showSaving: false,
		validationError: null,
		mutationError: null,
		saved: false,
		replacementError: null,
	};
}

export function SettingsDialog() {
	const { t } = useTranslation();
	const settingsModal = useUiStore((state) => state.settingsModal);
	const closeSettings = useUiStore((state) => state.closeSettings);
	const openGlobalSettings = useUiStore((state) => state.openGlobalSettings);
	const openProjectSettings = useUiStore((state) => state.openProjectSettings);
	const [keyboardShortcutsOpen, setKeyboardShortcutsOpen] = useState(false);
	const [connectMobileOpen, setConnectMobileOpen] = useState(false);
	const keyboardShortcutsRestoreRef = useRef<SettingsModal | null>(null);
	const connectMobileRestoreRef = useRef<SettingsModal | null>(null);

	// Keep the last non-null settings so the content stays rendered during the
	// exit animation (when settingsModal is already null but the dialog hasn't
	// finished animating out). Using a ref updated inline avoids the one-frame
	// gap that would occur with a useEffect-based approach: if DialogContent is
	// not rendered on the same frame that Dialog becomes open={true}, Radix's
	// DismissableLayer never registers and outside-click detection breaks.
	const lastSettingsRef = useRef<SettingsModal | null>(settingsModal);
	if (settingsModal !== null) lastSettingsRef.current = settingsModal;
	const displaySettings = lastSettingsRef.current;

	const globalSections: Array<{ id: Exclude<GlobalSettingsSection, "all">; label: string; icon: typeof Settings2 }> = [
		{ id: "general", label: t("settings.general"), icon: Settings2 },
		{ id: "updates", label: t("settings.updates"), icon: RefreshCw },
		{ id: "help", label: t("settings.help"), icon: CircleHelp },
	];

	const projectSections: Array<{ id: ProjectSettingsSection; label: string; icon: typeof Settings2 }> = [
		{ id: "general", label: t("settings.project.identity"), icon: MonitorCog },
		{ id: "agents", label: t("settings.project.agents"), icon: Bot },
		{ id: "workflow", label: t("settings.project.workflow"), icon: GitBranch },
		{ id: "intake", label: t("settings.project.intake"), icon: Inbox },
	];

	const isProjectSettings = displaySettings?.scope === "project";
	const [activeSection, setActiveSection] = useState<Exclude<GlobalSettingsSection, "all">>("general");
	const [activeProjectSection, setActiveProjectSection] = useState<ProjectSettingsSection>("general");
	const [projectSaveState, setProjectSaveState] = useState<ProjectSettingsSaveState>(initialProjectSaveState);

	const activeLabel = isProjectSettings
		? (projectSections.find((s) => s.id === activeProjectSection)?.label ?? t("settings.project.identity"))
		: (globalSections.find((section) => section.id === activeSection)?.label ?? t("settings.general"));

	const openKeyboardShortcuts = () => {
		if (!settingsModal) return;
		keyboardShortcutsRestoreRef.current = settingsModal;
		setKeyboardShortcutsOpen(true);
		closeSettings();
	};

	const restoreSettings = () => {
		const previousSettings = keyboardShortcutsRestoreRef.current;
		keyboardShortcutsRestoreRef.current = null;
		if (!previousSettings) return;
		if (previousSettings.scope === "global") openGlobalSettings();
		else openProjectSettings(previousSettings.projectId);
	};

	const openConnectMobile = () => {
		if (!settingsModal) return;
		connectMobileRestoreRef.current = settingsModal;
		setConnectMobileOpen(true);
		closeSettings();
	};

	const restoreConnectMobileSettings = () => {
		const previousSettings = connectMobileRestoreRef.current;
		connectMobileRestoreRef.current = null;
		if (!previousSettings) return;
		if (previousSettings.scope === "global") openGlobalSettings();
		else openProjectSettings(previousSettings.projectId);
	};

	const closeSettingsDialog = () => {
		if (isProjectSettings && projectSaveState.isPending) return;
		closeSettings();
	};

	useEffect(() => {
		if (settingsModal?.scope === "global") setActiveSection("general");
		if (settingsModal?.scope === "project") {
			setActiveProjectSection("general");
			setProjectSaveState(initialProjectSaveState());
		}
	}, [settingsModal]);

	return (
		<>
			<Dialog open={settingsModal !== null} onOpenChange={(open) => !open && closeSettingsDialog()}>
			<DialogContent
				className={cn(
					settingsDialogContentClass,
					"h-(--size-settings-dialog-height) w-(--size-settings-dialog-wide) max-h-none origin-center overflow-hidden p-0",
				)}
				showCloseButton={false}
			>
				{displaySettings && (
					<div className="flex h-full min-h-0">
						<aside className="flex w-48 shrink-0 flex-col border-r border-(--color-border-settings-dialog-header) bg-card">
						<p className="px-3 pb-1 pt-3 text-2xs font-semibold tracking-wider text-muted-foreground/60">{t("settings.title")}</p>
						<nav aria-label={t("settings.navSectionsAria")} className="flex flex-col gap-0.5 p-2 pt-0">
							{isProjectSettings
								? projectSections.map(({ id, label, icon }) => (
										<SettingsNavItem
											active={activeProjectSection === id}
											icon={icon}
											key={id}
											label={label}
											onClick={() => setActiveProjectSection(id)}
										/>
									))
								: globalSections.map(({ id, label, icon }) => (
										<SettingsNavItem
											active={activeSection === id}
											icon={icon}
											key={id}
											label={label}
											onClick={() => setActiveSection(id)}
										/>
									))}
						</nav>
						{isProjectSettings && (
							<div className="mt-auto flex flex-col gap-2 border-t border-(--color-border-settings-dialog-header) p-3">
								<Button
									type="submit"
									form="project-settings-form"
									variant="footer-primary"
									className={cn(
										"w-full rounded-md",
										(projectSaveState.validationError || projectSaveState.mutationError) &&
											"border-error bg-error/15 text-error hover:bg-error/20",
									)}
									disabled={projectSaveState.isPending}
									aria-live="polite"
									title={
										projectSaveState.validationError ??
										projectSaveState.mutationError ??
										(projectSaveState.replacementError
											? t("settings.project.restartFailed", { error: projectSaveState.replacementError })
											: undefined)
									}
								>
									{projectSaveState.showSaving ? (
										t("settings.project.saving")
									) : projectSaveState.saved ? (
										t("settings.project.saved")
									) : projectSaveState.validationError || projectSaveState.mutationError ? (
										<>
											<TriangleAlert className="size-4" aria-hidden="true" />
											{t("settings.project.saveFailed")}
										</>
									) : (
										t("settings.project.saveChanges")
									)}
								</Button>
								<span className="sr-only" role="status" aria-live="polite">
									{projectSaveState.validationError ?? projectSaveState.mutationError ??
										(projectSaveState.saved ? t("settings.project.saved") : "")}
								</span>
							</div>
						)}
					</aside>

					{/* Main area — same bg as the app page */}
					<div className="flex min-w-0 flex-1 flex-col bg-card">
						<DialogHeader className={cn(settingsDialogHeaderClass, "flex h-auto shrink-0 flex-row items-center justify-between border-b-0")}>
							<DialogTitle className="text-2xl font-bold text-foreground">{activeLabel}</DialogTitle>
							<DialogDescription className="sr-only">
								{isProjectSettings ? t("settings.project.dialogDescription") : t("settings.dialogDescription", { section: activeLabel.toLowerCase() })}
							</DialogDescription>
							<DialogClose
								aria-label={t("settings.close")}
								className="settings-close-button border border-transparent transition-colors hover:border-(--color-border-settings-input) hover:bg-[var(--color-bg-settings-input)]"
								disabled={isProjectSettings && projectSaveState.isPending}
							>
								<X aria-hidden="true" className="size-4" />
							</DialogClose>
						</DialogHeader>
						<div className={cn(settingsDialogBodyClass, "flex-1")}>
							{displaySettings?.scope === "project" ? (
								<ProjectSettingsForm
									projectId={displaySettings.projectId}
									section={activeProjectSection}
									onSaveState={setProjectSaveState}
								/>
							) : (
								<GlobalSettingsForm
									section={activeSection}
									onOpenKeyboardShortcuts={openKeyboardShortcuts}
									onOpenConnectMobile={openConnectMobile}
								/>
							)}
						</div>
					</div>
					</div>
				)}
		</DialogContent>
			</Dialog>
			<KeyboardShortcutsSettingsDialog
				open={keyboardShortcutsOpen}
				onOpenChange={(open) => {
					setKeyboardShortcutsOpen(open);
					if (!open) restoreSettings();
				}}
			/>
			<ConnectMobileModal
				open={connectMobileOpen}
				onOpenChange={(open) => {
					setConnectMobileOpen(open);
					if (!open) restoreConnectMobileSettings();
				}}
			/>
		</>
	);
}

function SettingsNavItem({
	active,
	icon: Icon,
	label,
	onClick,
}: {
	active: boolean;
	icon: typeof Settings2;
	label: string;
	onClick: () => void;
}) {
	return (
		<button
			aria-current={active ? "page" : undefined}
			className={cn(
				"flex h-9 w-full items-center gap-2 rounded-md px-2.5 text-left text-sm font-medium transition-[background-color,color,transform] duration-fast ease-out active:scale-press focus:outline-none focus-visible:outline-none focus-visible:ring-0",
				active
					? "bg-interactive-active text-foreground"
					: "text-muted-foreground hover:bg-interactive-hover hover:text-foreground",
			)}
			onClick={onClick}
			type="button"
		>
			<Icon aria-hidden="true" className="size-4 shrink-0" />
			{label}
		</button>
	);
}

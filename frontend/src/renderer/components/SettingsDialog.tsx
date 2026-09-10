import { Bot, GitBranch, Inbox, MonitorCog, TriangleAlert, X, type LucideIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useCloudGate } from "../hooks/useCloudGate";
import { useCloudProject } from "../hooks/useCloudProject";
import { GlobalSettingsForm } from "./GlobalSettingsForm";
import {
	ProjectSettingsForm,
	type ProjectSettingsSaveState,
	type ProjectSettingsSection,
} from "./ProjectSettingsForm";
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
import { type GlobalSettingsSection, type SettingsModal, useUiStore } from "../stores/ui-store";
import { cn } from "../lib/utils";
import { Button } from "./ui/button";
import { globalSettingsItem, visibleGlobalSettings } from "./settings/settingsCatalog";

function initialProjectSaveState(): ProjectSettingsSaveState {
	return { phase: "idle" };
}

export function SettingsDialog() {
	const { t } = useTranslation();
	const settingsModal = useUiStore((state) => state.settingsModal);
	const closeSettings = useUiStore((state) => state.closeSettings);
	// Reads the daemon settings the dialog tree already queries; no extra fetch.
	const { cloudEnabled } = useCloudGate();

	// Keep the last non-null settings so the content stays rendered during the
	// exit animation (when settingsModal is already null but the dialog hasn't
	// finished animating out). Using a ref updated inline avoids the one-frame
	// gap that would occur with a useEffect-based approach: if DialogContent is
	// not rendered on the same frame that Dialog becomes open={true}, Radix's
	// DismissableLayer never registers and outside-click detection breaks.
	const lastSettingsRef = useRef<SettingsModal | null>(settingsModal);
	if (settingsModal !== null) lastSettingsRef.current = settingsModal;
	const displaySettings = lastSettingsRef.current;

	const globalSections = visibleGlobalSettings({ cloudEnabled });

	const projectSections: Array<{ id: ProjectSettingsSection; label: string; icon: LucideIcon }> = [
		{ id: "general", label: t("settings.project.identity"), icon: MonitorCog },
		{ id: "agents", label: t("settings.project.agents"), icon: Bot },
		{ id: "workflow", label: t("settings.project.workflow"), icon: GitBranch },
		{ id: "intake", label: t("settings.project.intake"), icon: Inbox },
	];

	const isProjectSettings = displaySettings?.scope === "project";
	// Cloud project settings are read-only, so the save control has nothing to
	// submit. Offering it only for a known-local project also avoids flashing a
	// control that may turn out to have no form behind it, while a local-only
	// deployment resolves immediately and is unaffected.
	const cloud = useCloudProject(
		displaySettings?.scope === "project" ? displaySettings.projectId : "",
	);
	const [activeSection, setActiveSection] = useState<GlobalSettingsSection>("general");
	const [activeProjectSection, setActiveProjectSection] = useState<ProjectSettingsSection>("general");
	const [projectSaveState, setProjectSaveState] = useState<ProjectSettingsSaveState>(initialProjectSaveState);

	const activeLabel = isProjectSettings
		? (projectSections.find((s) => s.id === activeProjectSection)?.label ?? t("settings.project.identity"))
		: globalSettingsItem(activeSection, { cloudEnabled }).label(t);

	const closeSettingsDialog = () => {
		if (isProjectSettings && (projectSaveState.phase === "pending" || projectSaveState.phase === "saving")) return;
		closeSettings();
	};

	useEffect(() => {
		if (settingsModal?.scope === "global") {
			setActiveSection(globalSettingsItem(settingsModal.section ?? "general", { cloudEnabled }).id);
		}
		if (settingsModal?.scope === "project") {
			setActiveProjectSection("general");
			setProjectSaveState(initialProjectSaveState());
		}
	}, [cloudEnabled, settingsModal]);

	return (
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
											label={label(t)}
											onClick={() => setActiveSection(id)}
										/>
									))}
						</nav>
						{isProjectSettings && cloud.isKnownLocal && (
							<div className="mt-auto flex flex-col gap-2 border-t border-(--color-border-settings-dialog-header) p-3">
								<Button
									type="submit"
									form="project-settings-form"
									variant="footer-primary"
									className={cn(
										"w-full rounded-md",
										projectSaveState.phase === "failed" &&
											"border-error bg-error/15 text-error hover:bg-error/20",
									)}
									disabled={projectSaveState.phase === "pending" || projectSaveState.phase === "saving"}
									aria-live="polite"
									title={
										projectSaveState.error ??
										(projectSaveState.replacementError
											? t("settings.project.restartFailed", { error: projectSaveState.replacementError })
											: undefined)
									}
								>
									{projectSaveState.phase === "saving" ? (
										t("settings.project.saving")
									) : projectSaveState.phase === "saved" ? (
										t("settings.project.saved")
									) : projectSaveState.phase === "failed" ? (
										<>
											<TriangleAlert className="size-4" aria-hidden="true" />
											{t("settings.project.saveFailed")}
										</>
									) : (
										t("settings.project.saveChanges")
									)}
								</Button>
								<span className="sr-only" role="status" aria-live="polite">
									{projectSaveState.error ?? (projectSaveState.phase === "saved" ? t("settings.project.saved") : "")}
								</span>
							</div>
						)}
					</aside>

					{/* Main area — same bg as the app page */}
					<div className="flex min-w-0 flex-1 flex-col bg-card">
						<DialogHeader className={cn(settingsDialogHeaderClass, "flex h-auto shrink-0 flex-row items-center justify-between border-b-0 pb-3")}>
							<DialogTitle className="text-2xl font-bold text-foreground">{activeLabel}</DialogTitle>
							<DialogDescription className="sr-only">
								{isProjectSettings ? t("settings.project.dialogDescription") : t("settings.dialogDescription", { section: activeLabel.toLowerCase() })}
							</DialogDescription>
							<DialogClose
								aria-label={t("settings.close")}
								className="settings-close-button"
								disabled={isProjectSettings && (projectSaveState.phase === "pending" || projectSaveState.phase === "saving")}
							>
								<X aria-hidden="true" className="size-4" />
							</DialogClose>
						</DialogHeader>
						<div className={cn(settingsDialogBodyClass, "settings-dialog-body flex-1 px-(--size-modal-padding) pt-0")}>
							{displaySettings?.scope === "project" ? (
								<ProjectSettingsForm
									projectId={displaySettings.projectId}
									section={activeProjectSection}
									onSaveState={setProjectSaveState}
								/>
							) : (
								<GlobalSettingsForm
									cloudEnabled={cloudEnabled}
									section={activeSection}
								/>
							)}
						</div>
					</div>
					</div>
				)}
		</DialogContent>
			</Dialog>
	);
}

function SettingsNavItem({
	active,
	disabled,
	icon: Icon,
	label,
	onClick,
}: {
	active: boolean;
	disabled?: boolean;
	icon: LucideIcon;
	label: string;
	onClick: () => void;
}) {
	return (
		<button
			aria-current={active ? "page" : undefined}
			className={cn(
				"flex h-9 w-full items-center gap-2 rounded-md px-2.5 text-left text-sm font-medium transition-[background-color,color,transform] duration-fast ease-out active:scale-press focus:outline-none focus-visible:outline-none focus-visible:ring-0 disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:bg-transparent disabled:hover:text-muted-foreground",
				active
					? "bg-interactive-active text-foreground"
					: "text-muted-foreground hover:bg-interactive-hover hover:text-foreground",
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

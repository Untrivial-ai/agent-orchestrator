import { Bot, GitBranch, Inbox, MonitorCog, TriangleAlert, X, type LucideIcon } from "lucide-react";
import { FocusScope } from "@radix-ui/react-focus-scope";
import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { useCloudGate } from "../hooks/useCloudGate";
import { GlobalSettingsForm } from "./GlobalSettingsForm";
import {
	ProjectSettingsForm,
	type ProjectSettingsSaveState,
	type ProjectSettingsSection,
} from "./ProjectSettingsForm";
import {
	DialogHeader,
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

	const displaySettings = settingsModal;
	// The selected page includes several store/query subscribers. Mount it one
	// frame after the lightweight dialog chrome so the opening interaction can
	// paint first.
	const [bodySettings, setBodySettings] = useState<SettingsModal | null>(null);
	useEffect(() => {
		if (settingsModal === null) return;
		const frame = requestAnimationFrame(() => setBodySettings(settingsModal));
		return () => cancelAnimationFrame(frame);
	}, [settingsModal]);
	const isBodyReady = bodySettings === displaySettings;

	const globalSections = visibleGlobalSettings({ cloudEnabled });

	const projectSections: Array<{ id: ProjectSettingsSection; label: string; icon: LucideIcon }> = [
		{ id: "general", label: t("settings.project.identity"), icon: MonitorCog },
		{ id: "agents", label: t("settings.project.agents"), icon: Bot },
		{ id: "workflow", label: t("settings.project.workflow"), icon: GitBranch },
		{ id: "intake", label: t("settings.project.intake"), icon: Inbox },
	];

	const isProjectSettings = displaySettings?.scope === "project";
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
	const requestCloseRef = useRef(closeSettingsDialog);
	requestCloseRef.current = closeSettingsDialog;
	const closeButtonRef = useRef<HTMLButtonElement>(null);
	const open = settingsModal !== null;
	useEffect(() => {
		if (!open) return;
		// FocusScope contains focus immediately. Move visible focus after the
		// first paint because focus() forces style resolution.
		let focusTimer = 0;
		const focusFrame = requestAnimationFrame(() => {
			focusTimer = window.setTimeout(() => closeButtonRef.current?.focus({ preventScroll: true }), 0);
		});
		return () => {
			cancelAnimationFrame(focusFrame);
			window.clearTimeout(focusTimer);
		};
	}, [open]);

	useEffect(() => {
		if (settingsModal?.scope === "global") {
			setActiveSection(globalSettingsItem(settingsModal.section ?? "general", { cloudEnabled }).id);
		}
		if (settingsModal?.scope === "project") {
			setActiveProjectSection("general");
			setProjectSaveState(initialProjectSaveState());
		}
	}, [cloudEnabled, settingsModal]);

	if (!open || !displaySettings) return null;

	return createPortal(
		<>
			<div
				aria-hidden="true"
				className="dialog-overlay animate-overlay-in motion-reduce:animate-none"
				data-testid="settings-dialog-overlay"
				onPointerDown={closeSettingsDialog}
				onWheel={(event) => event.preventDefault()}
			/>
			<FocusScope loop trapped onMountAutoFocus={(event) => event.preventDefault()}>
				<div
					aria-describedby="settings-dialog-description"
					aria-labelledby="settings-dialog-title"
					aria-modal="true"
					className={cn(
						settingsDialogContentClass,
						"fixed left-1/2 top-1/2 h-(--size-settings-dialog-height) w-(--size-settings-dialog-wide) max-h-none -translate-x-1/2 -translate-y-1/2 origin-center overflow-hidden p-0 animate-modal-in motion-reduce:animate-none sm:rounded-lg",
					)}
					onKeyDown={(event) => {
						if (event.key !== "Escape") return;
						event.preventDefault();
						requestCloseRef.current();
					}}
					role="dialog"
					tabIndex={-1}
				>
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
						{isProjectSettings && (
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
							<h2 className="text-2xl font-bold text-foreground" id="settings-dialog-title">{activeLabel}</h2>
							<p className="sr-only" id="settings-dialog-description">
								{isProjectSettings ? t("settings.project.dialogDescription") : t("settings.dialogDescription", { section: activeLabel.toLowerCase() })}
							</p>
							<button
								aria-label={t("settings.close")}
								className="settings-close-button"
								disabled={isProjectSettings && (projectSaveState.phase === "pending" || projectSaveState.phase === "saving")}
								onClick={closeSettingsDialog}
								ref={closeButtonRef}
								type="button"
							>
								<X aria-hidden="true" className="size-4" />
							</button>
						</DialogHeader>
						<div
							aria-busy={!isBodyReady}
							className={cn(settingsDialogBodyClass, "settings-dialog-body flex-1 px-(--size-modal-padding) pt-0")}
						>
							{isBodyReady ? (
								displaySettings?.scope === "project" ? (
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
								)
							) : (
								<div aria-hidden="true" className="h-full" data-testid="settings-dialog-body-pending" />
							)}
						</div>
					</div>
				</div>
				</div>
			</FocusScope>
		</>,
		document.body,
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

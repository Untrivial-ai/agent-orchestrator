import { ChevronDown, Code2, FolderOpen, SquareTerminal } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { OpenTarget, OpenTargetId } from "../../shared/editor-handoff";
import { useEditorHandoffState, useOpenSessionTarget } from "../hooks/useEditorHandoff";
import { TopbarActionError, TopbarButton } from "./TopbarButton";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";
import {
	AndroidStudioIcon,
	CursorIcon,
	JetBrainsIcon,
	SublimeIcon,
	VSCodeIcon,
	VSCodiumIcon,
	WindsurfIcon,
	ZedIcon,
} from "./icons";

const editorIcons: Record<string, typeof VSCodeIcon> = {
	vscode: VSCodeIcon,
	"vscode-insiders": VSCodeIcon,
	vscodium: VSCodiumIcon,
	cursor: CursorIcon,
	windsurf: WindsurfIcon,
	zed: ZedIcon,
	sublime: SublimeIcon,
	"android-studio": AndroidStudioIcon,
	intellij: JetBrainsIcon,
	webstorm: JetBrainsIcon,
	pycharm: JetBrainsIcon,
	goland: JetBrainsIcon,
	phpstorm: JetBrainsIcon,
	rubymine: JetBrainsIcon,
	clion: JetBrainsIcon,
	rider: JetBrainsIcon,
	fleet: JetBrainsIcon,
};

const editorColors: Record<string, string> = {
	vscode: "#1F9CF0",
	"vscode-insiders": "#1F9CF0",
	vscodium: "#2F80ED",
	sublime: "#FF9800",
	"android-studio": "#3DDC84",
};

function TargetIcon({ target, className }: { target?: OpenTarget; className?: string }) {
	if (target?.kind === "file_manager") return <FolderOpen className={className} aria-hidden="true" />;
	if (target?.kind === "terminal") return <SquareTerminal className={className} aria-hidden="true" />;
	const Icon = (target && editorIcons[target.id]) || Code2;
	const color = target ? editorColors[target.id] : undefined;
	return <Icon className={className} style={color ? { color } : undefined} aria-hidden="true" />;
}

// Electron main owns the complete handoff: it resolves the loopback-only
// workspace path, launches the native target, and persists editor preference.
// This renderer receives only safe target metadata and availability status.
export function TopbarOpenEditorButton({
	sessionId,
	projectId,
	sessionCreatedAt,
	sessionTerminated,
	style,
}: {
	sessionId: string;
	projectId: string;
	sessionCreatedAt?: string;
	sessionTerminated?: boolean;
	style?: React.CSSProperties;
}) {
	const { t } = useTranslation();
	const stateQuery = useEditorHandoffState(sessionId, { sessionCreatedAt, sessionTerminated });
	const open = useOpenSessionTarget();
	const state = stateQuery.data;
	const [menuOpen, setMenuOpen] = useState(false);
	const targets = state?.targets ?? [];
	const editors = targets.filter((target) => target.kind === "editor");
	const preferred = editors.find((target) => target.id === state?.preferredEditorId);
	const safeTargets = targets.filter((target) => target.kind !== "editor");
	const fileManagerName = safeTargets.find((target) => target.kind === "file_manager")?.name ?? t("editor.fileManager");
	const terminalName = safeTargets.find((target) => target.kind === "terminal")?.name ?? t("editor.terminal");
	const workspaceAvailable = state?.workspaceAvailable === true;
	const busy = stateQuery.isPending || open.isPending;
	const mainDisabled = busy || !workspaceAvailable || !preferred;
	const menuDisabled = busy || !workspaceAvailable || targets.length === 0;

	const launch = (targetId?: OpenTargetId) => {
		open.reset();
		open.mutate({ sessionId, projectId, ...(targetId ? { targetId } : {}) });
	};
	const launchError = open.error instanceof Error ? open.error.message : null;
	const guidance = !stateQuery.isPending && !workspaceAvailable
		? state?.unavailableReason ?? t("editor.workspaceUnavailable")
		: !stateQuery.isPending && editors.length === 0
			? t("editor.noEditorGuidance", { fileManager: fileManagerName, terminal: terminalName })
			: null;
	const mainTitle = stateQuery.isPending
		? t("editor.preparingWorkspace")
		: (guidance
			?? (preferred ? t("editor.openWorkspaceInTitle", { name: preferred.name }) : t("editor.chooseEditorTitle")));

	return (
		<>
			{launchError || guidance ? (
				<TopbarActionError className="max-w-content-max truncate" title={launchError ?? guidance ?? undefined}>
					{launchError ?? guidance}
				</TopbarActionError>
			) : null}
			<div
				className="inline-flex items-center gap-0 rounded-md transition-colors hover:bg-interactive-hover data-[state=open]:bg-interactive-hover"
				data-state={menuOpen ? "open" : "closed"}
				style={style}
			>
				<Tooltip>
					<TooltipTrigger asChild>
						<span className="inline-flex">
							<TopbarButton
								aria-label={stateQuery.isPending
									? t("editor.preparingWorkspace")
									: preferred
										? t("editor.openInAria", { name: preferred.name })
										: t("editor.chooseEditor")}
								className="hover:bg-transparent"
								disabled={mainDisabled}
								onClick={() => launch()}
								variant="icon"
							>
								<TargetIcon target={preferred} className="size-icon-md" />
							</TopbarButton>
						</span>
					</TooltipTrigger>
					<TooltipContent side="bottom">{mainTitle}</TooltipContent>
				</Tooltip>
				<DropdownMenu onOpenChange={setMenuOpen}>
					<Tooltip>
						<TooltipTrigger asChild>
							<span className="inline-flex">
								<DropdownMenuTrigger asChild>
									<TopbarButton
										aria-label={t("editor.openOptionsAria")}
										className="hover:bg-transparent"
										disabled={menuDisabled}
										variant="icon"
									>
										<ChevronDown className="size-icon-sm" aria-hidden="true" />
									</TopbarButton>
								</DropdownMenuTrigger>
							</span>
						</TooltipTrigger>
						<TooltipContent side="bottom">{t("editor.openOptionsAria")}</TooltipContent>
					</Tooltip>
					<DropdownMenuContent align="end" className="min-w-52">
						{safeTargets.map((target) => (
							<DropdownMenuItem key={target.id} onSelect={() => launch(target.id)}>
								<TargetIcon target={target} className="size-icon-sm" />
								{t("editor.openInTarget", { name: target.name })}
							</DropdownMenuItem>
						))}
						{safeTargets.length > 0 && editors.length > 0 ? <DropdownMenuSeparator /> : null}
						{editors.length > 0 ? <DropdownMenuLabel>{t("editor.openWith")}</DropdownMenuLabel> : null}
						{editors.map((editor) => (
							<DropdownMenuItem key={editor.id} onSelect={() => launch(editor.id)}>
								<TargetIcon target={editor} className="size-icon-sm" />
								{editor.name}
							</DropdownMenuItem>
						))}
					</DropdownMenuContent>
				</DropdownMenu>
			</div>
		</>
	);
}

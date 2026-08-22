import { ChevronDown, Code2, FolderOpen } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEditorsQuery, useOpenInEditor } from "../hooks/useEditors";
import { TopbarButton, TopbarKillError } from "./TopbarButton";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "./ui/dropdown-menu";
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

// Each editor shows its own mark. The JetBrains IDEs share the umbrella mark
// rather than nine near-identical monochrome glyphs; anything without one falls
// back to the generic icon instead of borrowing another editor's logo.
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

// Brand colours, for the editors that actually have one. VS Code's is sampled
// from its own app icon; the rest are simple-icons' published hexes. Cursor,
// Windsurf, Zed and the JetBrains marks are monochrome brands — their real
// logos are greyscale — so they inherit the menu's text colour, which also
// keeps them legible in both light and dark themes.
const editorColors: Record<string, string> = {
	vscode: "#1F9CF0",
	"vscode-insiders": "#1F9CF0",
	vscodium: "#2F80ED",
	sublime: "#FF9800",
	"android-studio": "#3DDC84",
};

function EditorIcon({ editorId, className }: { editorId: string | undefined; className?: string }) {
	const Icon = (editorId && editorIcons[editorId]) || Code2;
	const color = editorId ? editorColors[editorId] : undefined;
	return <Icon className={className} style={color ? { color } : undefined} aria-hidden="true" />;
}

// "Open" split button for the topbar, styled after the VS Code opener in t3
// chat: the main half opens the session's worktree in the detected editor and
// focuses the file the agent most recently changed, so a session that was
// fixing the download button lands on that file. The chevron half offers the
// folder-only open and the other installed editors.
//
// Path resolution and the launch both happen in the daemon (POST
// /sessions/{id}/open-editor) — worktree paths are deliberately not on the wire.
export function TopbarOpenEditorButton({
	sessionId,
	projectId,
	style,
}: {
	sessionId: string;
	projectId: string;
	style?: React.CSSProperties;
}) {
	const { t } = useTranslation();
	const editorsQuery = useEditorsQuery();
	const editors = editorsQuery.data ?? [];
	const primaryEditor = editors[0];
	const open = useOpenInEditor();

	// Detection still in flight, or the daemon is unreachable: render nothing
	// rather than a button that is guaranteed to fail.
	if (editorsQuery.isPending || editors.length === 0) return null;

	const launch = (input: { editorId?: string; path?: string }) => {
		open.reset();
		open.mutate({ sessionId, projectId, ...input });
	};
	const error = open.error instanceof Error ? open.error.message : null;

	return (
		<>
			{error ? (
				<TopbarKillError className="max-w-content-max truncate" title={error}>
					{error}
				</TopbarKillError>
			) : null}
			<div className="inline-flex items-center" style={style}>
				<TopbarButton
					aria-label={t("editor.openInAria", { name: primaryEditor.name })}
					disabled={open.isPending}
					onClick={() => launch({})}
					title={t("editor.openWorkspaceInTitle", { name: primaryEditor.name })}
					variant="splitMain"
				>
					<EditorIcon editorId={primaryEditor.id} className="size-icon-lg" />
					{open.isPending ? t("editor.opening") : t("editor.open")}
				</TopbarButton>
				<DropdownMenu>
					<DropdownMenuTrigger asChild>
						<TopbarButton aria-label={t("editor.openOptionsAria")} disabled={open.isPending} variant="splitTrigger">
							<ChevronDown className="size-icon-sm" aria-hidden="true" />
						</TopbarButton>
					</DropdownMenuTrigger>
					<DropdownMenuContent align="end" className="min-w-48">
						<DropdownMenuItem onSelect={() => launch({ path: "." })}>
							<FolderOpen className="size-icon-sm" aria-hidden="true" />
							{t("editor.openFolderOnly")}
						</DropdownMenuItem>
						{editors.length > 1 ? (
							<>
								<DropdownMenuSeparator />
								<DropdownMenuLabel>{t("editor.openWith")}</DropdownMenuLabel>
								{editors.map((editor) => (
									<DropdownMenuItem key={editor.id} onSelect={() => launch({ editorId: editor.id })}>
										<EditorIcon editorId={editor.id} className="size-icon-sm" />
										{editor.name}
									</DropdownMenuItem>
								))}
							</>
						) : null}
					</DropdownMenuContent>
				</DropdownMenu>
			</div>
		</>
	);
}

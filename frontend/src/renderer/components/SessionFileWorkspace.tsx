import { useCallback, useEffect } from "react";
import { FileContentPane } from "./FileContentPane";
import type { FileAnnotationModel } from "./WorkspaceDiffView";
import type { FileViewMode } from "./FileContentPane";
import type { WorkspaceDiffScope } from "../hooks/useSessionWorkspaceFiles";

export function SessionFileWorkspace({
	annotation,
	commitSha,
	initialEditing = false,
	initialMode = "file",
	initialRequestKey = 0,
	onDirtyChange,
	onInitialEditingConsumed,
	path,
	sessionId,
	split,
	scope = "combined",
}: {
	annotation: FileAnnotationModel;
	commitSha?: string;
	initialEditing?: boolean;
	initialMode?: FileViewMode;
	initialRequestKey?: number;
	onDirtyChange?: (path: string, dirty: boolean) => void;
	onInitialEditingConsumed?: (path: string, requestKey: number) => void;
	path: string;
	sessionId: string;
	split: boolean;
	scope?: WorkspaceDiffScope;
}) {
	const handleDirtyChange = useCallback(
		(dirty: boolean) => onDirtyChange?.(path, dirty),
		[onDirtyChange, path],
	);
	useEffect(
		() => () => {
			if (initialEditing) onInitialEditingConsumed?.(path, initialRequestKey);
		},
		[initialEditing, initialRequestKey, onInitialEditingConsumed, path],
	);
	return (
		<section className="relative flex h-full min-h-0 flex-col bg-background" data-testid="session-file-workspace">
			<div className="board-scrollbar min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain">
				<FileContentPane
					annotation={annotation}
					commitSha={commitSha}
					initialEditing={initialEditing}
					initialMode={initialMode}
					initialRequestKey={initialRequestKey}
					onDirtyChange={handleDirtyChange}
					path={path}
					sessionId={sessionId}
					split={split}
					scope={scope}
				/>
			</div>
		</section>
	);
}

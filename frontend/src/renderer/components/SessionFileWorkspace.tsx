import { useCallback, useEffect, useLayoutEffect, useRef } from "react";
import { FileContentPane } from "./FileContentPane";
import type { FileAnnotationModel } from "./WorkspaceDiffView";
import type { FileViewMode } from "./FileContentPane";
import type { WorkspaceDiffScope } from "../hooks/useSessionWorkspaceFiles";

export function SessionFileWorkspace({
	annotation,
	commitSha,
	initialEditing = false,
	initialLine,
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
	initialLine?: number;
	initialMode?: FileViewMode;
	initialRequestKey?: number;
	onDirtyChange?: (path: string, dirty: boolean) => void;
	onInitialEditingConsumed?: (path: string, requestKey: number) => void;
	path: string;
	sessionId: string;
	split: boolean;
	scope?: WorkspaceDiffScope;
}) {
	const scrollRef = useRef<HTMLDivElement>(null);
	const scrollKey = `${sessionId}:${path}`;
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
	useLayoutEffect(() => {
		const scroll = scrollRef.current;
		if (scroll) scroll.scrollTop = fileScrollPositions.get(scrollKey) ?? 0;
	}, [scrollKey]);
	return (
		<section className="relative flex h-full min-h-0 flex-col bg-background" data-testid="session-file-workspace">
			<div
				className="board-scrollbar min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain"
				data-testid="session-file-scroll"
				onScroll={(event) => rememberFileScrollPosition(scrollKey, event.currentTarget.scrollTop)}
				ref={scrollRef}
			>
				<FileContentPane
					annotation={annotation}
					commitSha={commitSha}
					initialEditing={initialEditing}
					initialLine={initialLine}
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

const fileScrollPositions = new Map<string, number>();
const maxRememberedFileScrollPositions = 256;

function rememberFileScrollPosition(key: string, scrollTop: number) {
	fileScrollPositions.delete(key);
	fileScrollPositions.set(key, scrollTop);
	if (fileScrollPositions.size <= maxRememberedFileScrollPositions) return;
	const oldestKey = fileScrollPositions.keys().next().value;
	if (oldestKey) fileScrollPositions.delete(oldestKey);
}
